// Package generate drives a wiki generation run with a model: a planner
// agent proposes the page queue, then one worker agent per page writes it and
// submits its Claims, then the run is finished deterministically.
package generate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Hoodoo/owcli/internal/agent"
	"github.com/Hoodoo/owcli/internal/claims"
	"github.com/Hoodoo/owcli/internal/llm"
	"github.com/Hoodoo/owcli/internal/okf"
	"github.com/Hoodoo/owcli/internal/run"
)

// Options configures Generate.
type Options struct {
	Env       run.Env
	Provider  llm.Provider
	Mode      run.Mode
	Message   string // the user's request, if any
	MaxTokens int    // per model call; 0 uses the provider default
	Progress  func(Progress)
}

// Progress reports what the run is doing.
type Progress struct {
	Stage string // "begin", "plan", "page", "skip", "finish"
	Page  string // wiki page for page stages
	Index int    // 1-based page number
	Total int
	Note  string
	Event *agent.Event // agent activity, when Stage is plan or page
}

// Result summarizes a run.
type Result struct {
	Noop    bool
	RunID   string
	GitHead string // source commit the run documented
	Begin   run.BeginResult
	Written []string
	Skipped []string
	Finish  run.FinishResult
	Usage   llm.Usage
}

// Generate runs (or resumes) a generation run to completion. An error leaves
// the run resumable: the next Generate with the same mode picks it up.
func Generate(ctx context.Context, o Options) (Result, error) {
	var res Result
	report := func(p Progress) {
		if o.Progress != nil {
			o.Progress(p)
		}
	}
	r, begin, err := run.Begin(o.Env, o.Mode, o.Message)
	if err != nil {
		return res, err
	}
	res.Begin = begin
	if begin.Noop {
		res.Noop = true
		return res, nil
	}
	res.RunID, res.GitHead = r.State().RunID, r.State().GitHead
	note := "starting"
	if begin.Resumed {
		note = "resuming an interrupted run"
	}
	report(Progress{Stage: "begin", Note: note})

	ws, err := agent.NewWorkspace(o.Env.Layout)
	if err != nil {
		return res, err
	}
	if r.State().Phase == run.Planning {
		if err := plan(ctx, o, r, ws, begin.Issues, &res, report); err != nil {
			return res, err
		}
	}

	var skipped []run.PageSnapshot
	total := len(r.State().Plan.Jobs)
	for {
		next, err := r.Next()
		if err != nil {
			return res, err
		}
		if next == nil {
			break
		}
		index := total - next.Remaining + 1
		page := strings.TrimPrefix(next.Page, "/")
		report(Progress{Stage: "page", Page: page, Index: index, Total: total, Note: "writing"})
		snap, err := r.Snapshot(next.ID)
		if err != nil {
			return res, err
		}
		werr := writePage(ctx, o, r, ws, next, &res, func(e agent.Event) {
			report(Progress{Stage: "page", Page: page, Index: index, Total: total, Event: &e})
		})
		if done := r.State(); jobDone(done, next.ID) {
			res.Written = append(res.Written, page)
			continue
		}
		// The worker did not complete the page: restore it and move on, or
		// stop if the failure will affect every page.
		if err := r.Skip(snap); err != nil {
			return res, errors.Join(werr, err)
		}
		skipped = append(skipped, snap)
		res.Skipped = append(res.Skipped, page)
		report(Progress{Stage: "skip", Page: page, Index: index, Total: total, Note: describe(werr)})
		if !recoverable(werr) {
			return res, fmt.Errorf("writing %s: %w", page, werr)
		}
	}

	report(Progress{Stage: "finish", Note: "finalizing"})
	res.Finish, err = r.Finish(skipped)
	return res, err
}

func jobDone(s *run.State, id string) bool {
	for _, j := range s.Plan.Jobs {
		if j.ID == id {
			return j.Status == run.Complete
		}
	}
	return false
}

// recoverable reports worker failures that only affect the current page.
func recoverable(err error) bool {
	return err == nil || errors.Is(err, agent.ErrStepLimit) || errors.Is(err, agent.ErrRefused) || errors.Is(err, errNoSubmit)
}

var errNoSubmit = errors.New("the worker stopped without submitting the page")

// maxNudges is how often an agent that ends its turn early is reminded of
// its task before the attempt counts as abandoned.
const maxNudges = 2

func nudge(limit int, text string) func(agent.Outcome) string {
	n := 0
	return func(agent.Outcome) string {
		if n >= limit {
			return ""
		}
		n++
		return text
	}
}

func describe(err error) string {
	if err == nil {
		return "skipped"
	}
	return "skipped: " + err.Error()
}

func plan(ctx context.Context, o Options, r *run.Run, ws *agent.Workspace, issues []claims.Issue, res *Result, report func(Progress)) error {
	var fatal error
	submit := agent.NewTool("submit_plan", "Install the wiki plan. Call exactly once, when your research is done.", submitPlanSchema,
		func(_ context.Context, in run.PlanInput) (agent.Result, error) {
			if err := r.SubmitPlan(in); err != nil {
				if run.IsCode(err, run.InvalidInput) {
					return agent.Result{}, err
				}
				fatal = err
				return agent.Result{Text: "internal error; stop", Stop: true}, nil
			}
			jobs := r.State().Plan.Jobs
			return agent.Result{Text: fmt.Sprintf("plan installed with %d page(s)", len(jobs)), Stop: true}, nil
		})
	prompt, err := plannerPrompt(o, r, issues)
	if err != nil {
		return err
	}
	out, err := agent.Run(ctx, o.Provider, agent.Options{
		System: plannerSystem, Prompt: prompt, Tools: append(agent.ReadTools(ws), submit),
		MaxSteps: plannerMaxSteps, MaxTokens: o.MaxTokens,
		Continue: nudge(maxNudges, "You have not submitted a plan yet. Continue your research if needed, then call submit_plan."),
		OnEvent:  func(e agent.Event) { report(Progress{Stage: "plan", Event: &e}) },
	})
	addUsage(&res.Usage, out.Usage)
	switch {
	case fatal != nil:
		return fatal
	case err != nil:
		return fmt.Errorf("planning: %w", err)
	case r.State().Plan == nil:
		return errors.New("planning: the planner stopped without submitting a plan")
	}
	return nil
}

func writePage(ctx context.Context, o Options, r *run.Run, ws *agent.Workspace, next *run.NextJob, res *Result, onEvent func(agent.Event)) error {
	rel := strings.TrimPrefix(next.Page, "/openwiki/")
	ws.Writable = map[string]bool{rel: true}
	defer func() { ws.Writable = map[string]bool{} }()

	var fatal error
	inspect := agent.NewTool("inspect_page_claims", "List every Claim your page currently owns, with ids, statements, evidence, and any issue.", `{"type":"object","properties":{},"additionalProperties":false}`,
		func(context.Context, struct{}) (agent.Result, error) {
			cs, err := r.InspectClaims(next.ID)
			if err != nil {
				return agent.Result{}, err
			}
			return agent.Result{Text: toJSON(cs)}, nil
		})
	submit := agent.NewTool("submit_page", "Submit your finished page with its Claim decisions. On success the page is complete and you should stop.", submitPageSchema,
		func(_ context.Context, p claims.Proposal) (agent.Result, error) {
			remaining, err := r.SubmitPage(next.ID, p)
			if err != nil {
				if run.IsCode(err, run.InvalidInput) {
					return agent.Result{}, err
				}
				fatal = err
				return agent.Result{Text: "internal error; stop", Stop: true}, nil
			}
			return agent.Result{Text: fmt.Sprintf("page accepted; %d page(s) remain for other writers", remaining), Stop: true}, nil
		})
	prompt, err := workerPrompt(o, r, next)
	if err != nil {
		return err
	}
	tools := append(append(agent.ReadTools(ws), agent.WriteTools(ws)...), inspect, submit)
	out, err := agent.Run(ctx, o.Provider, agent.Options{
		System: workerSystem, Prompt: prompt, Tools: tools,
		MaxSteps: workerMaxSteps, MaxTokens: o.MaxTokens, OnEvent: onEvent,
		Continue: nudge(maxNudges, fmt.Sprintf("You have not finished. Do not reply with the page as text: call write_file with path %s and the complete Markdown, then call submit_page with its Claims.", strings.TrimPrefix(next.Page, "/"))),
	})
	addUsage(&res.Usage, out.Usage)
	switch {
	case fatal != nil:
		return fatal
	case err != nil:
		return err
	case !out.Stopped:
		return errNoSubmit
	}
	return nil
}

func plannerPrompt(o Options, r *run.Run, issues []claims.Issue) (string, error) {
	s := r.State()
	var b strings.Builder
	if s.Mode == run.Init {
		b.WriteString("Plan a new wiki for this repository.\n")
	} else {
		b.WriteString("Plan an update of this repository's existing wiki.\n")
		lu, err := o.Env.Layout.LoadLastUpdate()
		if err != nil {
			return "", err
		}
		if lu != nil && lu.GitHead != "" {
			fmt.Fprintf(&b, "The wiki was last updated at commit %s (status: %s); HEAD is now %s. Use git_log to see what changed.\n", short(lu.GitHead), lu.Status, short(s.GitHead))
		}
		pages, err := pageSummaries(o)
		if err != nil {
			return "", err
		}
		b.WriteString("\nExisting pages:\n" + pages)
		if summary := issueSummary(issues); summary != "" {
			b.WriteString("\nPages whose Claims cite changed or missing source (added to the plan automatically):\n" + summary)
		}
	}
	if s.Message != "" {
		fmt.Fprintf(&b, "\nThe user asked: %s\n", s.Message)
	}
	if ins := instructions(o); ins != "" {
		b.WriteString("\nWiki instructions from INSTRUCTIONS.md:\n" + ins + "\n")
	}
	return b.String(), nil
}

func workerPrompt(o Options, r *run.Run, next *run.NextJob) (string, error) {
	s := r.State()
	var b strings.Builder
	page := strings.TrimPrefix(next.Page, "/")
	if next.Existing {
		fmt.Fprintf(&b, "Update the wiki page %s (read its current content first).\n", page)
	} else {
		fmt.Fprintf(&b, "Write the new wiki page %s.\n", page)
	}
	if next.Title != "" {
		fmt.Fprintf(&b, "Title: %s\n", next.Title)
	}
	if next.Purpose != "" {
		fmt.Fprintf(&b, "Purpose: %s\n", next.Purpose)
	}
	if next.Reason != "" {
		fmt.Fprintf(&b, "Why this page is being revisited: %s\n", next.Reason)
	}
	if len(next.SeedPaths) > 0 {
		fmt.Fprintf(&b, "Start your research at: %s\n", strings.Join(next.SeedPaths, ", "))
	}
	if next.ExistingClaimCount > 0 {
		fmt.Fprintf(&b, "\nThe page owns %d Claim(s).", next.ExistingClaimCount)
		if len(next.ClaimsRequiringAttention) > 0 {
			b.WriteString(" These need an explicit confirm, revise, or retract decision after you recheck the source:\n" + toJSON(next.ClaimsRequiringAttention) + "\n")
		} else {
			b.WriteString(" None needs attention; untouched Claims are kept automatically.\n")
		}
	}
	b.WriteString("\nThe whole wiki plan (link to these pages where relevant):\n")
	for _, j := range s.Plan.Jobs {
		line := "- " + strings.TrimPrefix(j.Page, "/openwiki/")
		if j.Title != "" {
			line += ": " + j.Title
		}
		if j.Purpose != "" {
			line += " — " + j.Purpose
		}
		b.WriteString(line + "\n")
	}
	if existing, err := pageSummaries(o); err == nil && existing != "" {
		b.WriteString("\nPages already in the wiki:\n" + existing)
	}
	if s.Plan.Instructions != "" {
		b.WriteString("\nGuidance from the planner: " + s.Plan.Instructions + "\n")
	}
	if s.Message != "" {
		fmt.Fprintf(&b, "\nThe user asked: %s\n", s.Message)
	}
	if ins := instructions(o); ins != "" {
		b.WriteString("\nWiki instructions from INSTRUCTIONS.md:\n" + ins + "\n")
	}
	if strings.HasSuffix(next.Page, "/quickstart.md") {
		b.WriteString("\nThis is the quickstart: orient a newcomer in a few paragraphs, then route each common task to the page that covers it.\n")
	}
	return b.String(), nil
}

// pageSummaries lists existing concept pages with their titles and
// descriptions.
func pageSummaries(o Options) (string, error) {
	w := okf.ForLayout(o.Env.Layout)
	pages, err := w.ConceptPages()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, p := range pages {
		content, err := w.Read(p)
		if err != nil {
			continue
		}
		f, _ := okf.Fields(content)
		line := "- " + strings.TrimPrefix(p, "/openwiki/")
		if t, ok := okf.StringField(f, "title"); ok {
			line += ": " + t
		}
		if d, ok := okf.StringField(f, "description"); ok {
			line += " — " + d
		}
		b.WriteString(line + "\n")
	}
	return b.String(), nil
}

// issueSummary counts grounding issues per page.
func issueSummary(issues []claims.Issue) string {
	counts := map[string]int{}
	var order []string
	for _, is := range issues {
		if counts[is.Page] == 0 {
			order = append(order, is.Page)
		}
		counts[is.Page]++
	}
	var b strings.Builder
	for _, p := range order {
		fmt.Fprintf(&b, "- %s: %d Claim(s)\n", strings.TrimPrefix(p, "/openwiki/"), counts[p])
	}
	return b.String()
}

func instructions(o Options) string {
	data, err := os.ReadFile(o.Env.Layout.InstructionsPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(okf.Body(string(data)))
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

func toJSON(v any) string {
	data, _ := json.MarshalIndent(v, "", "  ")
	return string(data)
}

func addUsage(total *llm.Usage, u llm.Usage) {
	total.InputTokens += u.InputTokens
	total.OutputTokens += u.OutputTokens
	total.CacheReadTokens += u.CacheReadTokens
	total.CacheWriteTokens += u.CacheWriteTokens
}

const submitPlanSchema = `{
  "type": "object",
  "properties": {
    "pages": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "path": {"type": "string", "description": "page path below openwiki/, e.g. architecture/overview.md"},
          "title": {"type": "string"},
          "purpose": {"type": "string", "description": "what the page must answer, in one sentence"},
          "seedPaths": {"type": "array", "items": {"type": "string"}, "description": "repository files or directories to start from"}
        },
        "required": ["path", "title", "purpose"],
        "additionalProperties": false
      }
    },
    "deletions": {"type": "array", "items": {"type": "string"}, "description": "existing pages to delete (updates only)"},
    "instructions": {"type": "string", "description": "short guidance for every page writer"}
  },
  "required": ["pages"],
  "additionalProperties": false
}`

const submitPageSchema = `{
  "type": "object",
  "properties": {
    "claims": {
      "type": "array",
      "description": "new Claims (no id) and revised existing Claims (with id)",
      "items": {
        "type": "object",
        "properties": {
          "id": {"type": "string"},
          "statement": {"type": "string"},
          "evidence": {"type": "array", "items": {"type": "string", "description": "repo://path or repo://path#Lstart-Lend"}}
        },
        "required": ["statement", "evidence"],
        "additionalProperties": false
      }
    },
    "confirmedClaimIds": {"type": "array", "items": {"type": "string"}, "description": "Claims needing attention that you rechecked and found still true"},
    "retractedClaimIds": {"type": "array", "items": {"type": "string"}, "description": "Claims that no longer hold or were removed from the page"}
  },
  "additionalProperties": false
}`

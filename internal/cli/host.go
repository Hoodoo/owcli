package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/Hoodoo/owcli/internal/claims"
	"github.com/Hoodoo/owcli/internal/okf"
	"github.com/Hoodoo/owcli/internal/run"
	"github.com/Hoodoo/owcli/internal/store"
	"github.com/Hoodoo/owcli/internal/version"
)

// hostModel is recorded as the model of agent-driven runs.
const hostModel = "host-agent"

// maxChangedPaths caps the changed-path list returned by begin.
const maxChangedPaths = 300

// ErrReported marks an error already printed (as JSON); main exits non-zero
// without printing it again.
var ErrReported = errors.New("error already reported")

func newRunCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Drive a generation run step by step from a coding agent (JSON in/out)",
		Long: `Let an interactive coding agent research and write the wiki itself, with
owcli enforcing the lifecycle, Claims, and OKF rules. No model is called.

  owcli run begin init|update [--external] [--message M]
  owcli run plan --file plan.json   (or JSON on stdin)
  owcli run next
  owcli run inspect <jobId>
  owcli run submit <jobId> --file claims.json
  owcli run skip <jobId>
  owcli run finish

Every command prints one JSON object; failures print {"error":{"code","message"}}
and exit non-zero. Runs are resumable: call begin again after an interruption.
See ` + "`owcli quickstart`" + ` for the full procedure and input formats.`,
	}
	cmd.AddCommand(newRunBegin(), newRunPlan(), newRunNext(), newRunInspect(), newRunSubmit(), newRunSkip(), newRunFinish())
	return cmd
}

func hostEnv(l store.Layout) run.Env {
	return run.Env{Layout: l, Producer: version.Producer(), Model: hostModel}
}

// jsonCmd wraps a handler: its value or error is printed as JSON.
func jsonCmd(fn func(cmd *cobra.Command, args []string) (any, error)) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		v, err := fn(cmd, args)
		if err != nil {
			code := "error"
			var re *run.Error
			if errors.As(err, &re) {
				code = string(re.Code)
			}
			_ = writeJSON(cmd.OutOrStdout(), map[string]any{"error": map[string]string{"code": code, "message": err.Error()}})
			cmd.SilenceErrors = true
			return ErrReported
		}
		return writeJSON(cmd.OutOrStdout(), v)
	}
}

func openRun() (*run.Run, store.Layout, error) {
	l, err := resolveLayout()
	if err != nil {
		return nil, l, err
	}
	r, err := run.Open(hostEnv(l))
	return r, l, err
}

type pageInfo struct {
	Path        string `json:"path"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

func wikiPages(l store.Layout) []pageInfo {
	w := okf.ForLayout(l)
	pages, _ := w.ConceptPages()
	out := []pageInfo{}
	for _, p := range pages {
		content, err := w.Read(p)
		if err != nil {
			continue
		}
		f, _ := okf.Fields(content)
		t, _ := okf.StringField(f, "title")
		d, _ := okf.StringField(f, "description")
		out = append(out, pageInfo{Path: rel(p), Title: t, Description: d})
	}
	return out
}

func newRunBegin() *cobra.Command {
	var (
		external bool
		wikiDir  string
		message  string
	)
	cmd := &cobra.Command{
		Use:   "begin init|update",
		Short: "Start, resume, or no-op a run",
		Args:  cobra.ExactArgs(1),
		RunE: jsonCmd(func(_ *cobra.Command, args []string) (any, error) {
			mode := run.Mode(args[0])
			if mode != run.Init && mode != run.Update {
				return nil, fmt.Errorf("mode must be init or update, got %q", args[0])
			}
			l, err := layoutFor(mode, external, wikiDir)
			if err != nil {
				return nil, err
			}
			lastUpdate, err := l.LoadLastUpdate()
			if err != nil {
				return nil, err
			}
			r, res, err := run.Begin(hostEnv(l), mode, message)
			if err != nil {
				return nil, err
			}
			out := map[string]any{"mode": mode, "wiki": l.WikiRoot, "layout": l.Kind}
			if lastUpdate != nil {
				out["lastUpdate"] = lastUpdate
			}
			if res.Noop {
				out["status"] = "noop"
				out["next"] = "Nothing to do: the wiki is current. Stop."
				return out, nil
			}
			s := r.State()
			out["status"] = s.Phase
			out["runId"] = s.RunID
			out["resumed"] = res.Resumed
			out["planInvalidated"] = res.PlanInvalidated
			out["head"] = s.GitHead
			if branch, def := branches(l.RepoRoot); branch != "" {
				out["branch"] = branch
				if def != "" {
					out["defaultBranch"] = def
					if branch != def {
						out["warning"] = fmt.Sprintf("The wiki documents the default branch; you are on %s. Update after merging into %s unless the user asked otherwise.", branch, def)
					}
				}
			}
			out["pages"] = wikiPages(l)
			if s.Message != "" {
				out["message"] = s.Message
			}
			if len(res.Issues) > 0 {
				out["claimIssues"] = issueCounts(res.Issues)
			}
			if lastUpdate != nil && mode == run.Update {
				changed, err := r.ChangedPaths(lastUpdate.GitHead)
				if err != nil {
					return nil, err
				}
				if len(changed) > maxChangedPaths {
					out["changedPathsTruncated"] = len(changed) - maxChangedPaths
					changed = changed[:maxChangedPaths]
				}
				out["changedPaths"] = changed
			}
			if ins := instructionsBody(l); ins != "" {
				out["instructions"] = ins
			}
			if s.Phase == run.Planning {
				out["next"] = "Research the repository, write the plan JSON to a file outside the repository, then run `owcli run plan --file <path>`."
			} else {
				out["next"] = "Continue with `owcli run next`."
			}
			return out, nil
		}),
	}
	cmd.Flags().BoolVar(&external, "external", false, "init only: if the repository is not bound yet, store the wiki outside it")
	cmd.Flags().StringVar(&wikiDir, "wiki-dir", "", "init only: if the repository is not bound yet, store the wiki in this directory")
	cmd.Flags().StringVar(&message, "message", "", "what this run should focus on")
	return cmd
}

// branches returns the current branch and the repository's default branch
// (origin's HEAD, else a local main or master).
func branches(repo string) (string, string) {
	branch, err := gitOutput(repo, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", ""
	}
	branch = strings.TrimSpace(branch)
	if ref, err := gitOutput(repo, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		return branch, strings.TrimPrefix(strings.TrimSpace(ref), "origin/")
	}
	for _, name := range []string{"main", "master"} {
		if _, err := gitOutput(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+name); err == nil {
			return branch, name
		}
	}
	return branch, ""
}

func issueCounts(issues []claims.Issue) []map[string]any {
	counts := map[string]map[string]int{}
	var order []string
	for _, is := range issues {
		p := rel(is.Page)
		if counts[p] == nil {
			counts[p] = map[string]int{}
			order = append(order, p)
		}
		counts[p][string(is.Kind)]++
	}
	out := []map[string]any{}
	for _, p := range order {
		out = append(out, map[string]any{"page": p, "stale": counts[p]["stale"], "unresolved": counts[p]["unresolved"]})
	}
	return out
}

func instructionsBody(l store.Layout) string {
	data, err := os.ReadFile(l.InstructionsPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(okf.Body(string(data)))
}

// readInput decodes JSON from --file or stdin. A terminal on stdin fails
// fast instead of waiting for input an agent may never send.
func readInput(cmd *cobra.Command, file string, v any) error {
	var r io.Reader = cmd.InOrStdin()
	if file == "" || file == "-" {
		if f, ok := r.(*os.File); ok {
			if isatty.IsTerminal(f.Fd()) {
				return &run.Error{Code: run.InvalidInput, Msg: "no JSON input: stdin is a terminal; write the JSON to a file outside the repository and pass --file <path>, or pipe it"}
			}
		}
	} else {
		f, err := os.Open(file)
		if err != nil {
			return err
		}
		defer f.Close()
		r = f
	}
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return &run.Error{Code: run.InvalidInput, Msg: "invalid JSON input", Err: err}
	}
	return nil
}

func newRunPlan() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Submit the page plan (JSON from --file or stdin)",
		Args:  cobra.NoArgs,
		RunE: jsonCmd(func(cmd *cobra.Command, _ []string) (any, error) {
			var in run.PlanInput
			if err := readInput(cmd, file, &in); err != nil {
				return nil, err
			}
			r, _, err := openRun()
			if err != nil {
				return nil, err
			}
			if err := r.SubmitPlan(in); err != nil {
				return nil, err
			}
			var order []string
			for _, j := range r.State().Plan.Jobs {
				order = append(order, rel(j.Page))
			}
			return map[string]any{"status": "accepted", "pages": order, "next": "Loop: `owcli run next`, write the page, `owcli run submit <jobId>`."}, nil
		}),
	}
	cmd.Flags().StringVar(&file, "file", "", "read JSON from this file (keep it outside the repository); - or empty reads stdin")
	return cmd
}

func newRunNext() *cobra.Command {
	return &cobra.Command{
		Use:   "next",
		Short: "Show the current page job (and snapshot the page before you edit it)",
		Args:  cobra.NoArgs,
		RunE: jsonCmd(func(*cobra.Command, []string) (any, error) {
			r, _, err := openRun()
			if err != nil {
				return nil, err
			}
			next, err := r.Next()
			if err != nil {
				return nil, err
			}
			if next == nil {
				return map[string]any{"status": "complete", "next": "All pages are done: run `owcli run finish`."}, nil
			}
			if err := r.EnsureSnapshot(next.ID); err != nil {
				return nil, err
			}
			s := r.State()
			job := map[string]any{
				"id": next.ID, "path": rel(next.Page), "existing": next.Existing,
				"existingClaimCount": next.ExistingClaimCount,
			}
			setIf := func(k string, v any, ok bool) {
				if ok {
					job[k] = v
				}
			}
			setIf("title", next.Title, next.Title != "")
			setIf("purpose", next.Purpose, next.Purpose != "")
			setIf("reason", next.Reason, next.Reason != "")
			setIf("seedPaths", next.SeedPaths, len(next.SeedPaths) > 0)
			setIf("claimsRequiringAttention", next.ClaimsRequiringAttention, len(next.ClaimsRequiringAttention) > 0)
			var related []string
			for _, p := range next.RelatedPages {
				related = append(related, rel(p))
			}
			setIf("relatedPages", related, len(related) > 0)
			var plan []map[string]string
			for _, j := range s.Plan.Jobs {
				plan = append(plan, map[string]string{"path": rel(j.Page), "title": j.Title, "status": string(j.Status)})
			}
			out := map[string]any{"status": "pending", "job": job, "remaining": next.Remaining, "plan": plan,
				"next": fmt.Sprintf("Research, write %s, then write Claim decisions to a file outside the repository and run `owcli run submit %s --file <path>`.", rel(next.Page), next.ID)}
			if s.Plan.Instructions != "" {
				out["instructions"] = s.Plan.Instructions
			}
			return out, nil
		}),
	}
}

func newRunInspect() *cobra.Command {
	return &cobra.Command{
		Use:   "inspect <jobId>",
		Short: "List every Claim the current page owns, with ids",
		Args:  cobra.ExactArgs(1),
		RunE: jsonCmd(func(_ *cobra.Command, args []string) (any, error) {
			r, _, err := openRun()
			if err != nil {
				return nil, err
			}
			cs, err := r.InspectClaims(args[0])
			if err != nil {
				return nil, err
			}
			return map[string]any{"claims": cs}, nil
		}),
	}
}

// proposalInput accepts evidence as strings or {"resource": ...} objects.
type proposalInput struct {
	ConfirmedClaimIDs []string `json:"confirmedClaimIds"`
	RetractedClaimIDs []string `json:"retractedClaimIds"`
	Claims            []struct {
		ID        string            `json:"id"`
		Statement string            `json:"statement"`
		Evidence  []json.RawMessage `json:"evidence"`
	} `json:"claims"`
}

func (p proposalInput) proposal() (claims.Proposal, error) {
	out := claims.Proposal{ConfirmedClaimIDs: p.ConfirmedClaimIDs, RetractedClaimIDs: p.RetractedClaimIDs}
	for _, c := range p.Claims {
		pc := claims.ProposedClaim{ID: c.ID, Statement: c.Statement}
		for _, raw := range c.Evidence {
			var s string
			if json.Unmarshal(raw, &s) == nil {
				pc.Evidence = append(pc.Evidence, s)
				continue
			}
			var obj struct {
				Resource string `json:"resource"`
			}
			if err := json.Unmarshal(raw, &obj); err != nil || obj.Resource == "" {
				return out, &run.Error{Code: run.InvalidInput, Msg: fmt.Sprintf("evidence must be \"repo://...\" or {\"resource\": \"repo://...\"}, got %s", raw)}
			}
			pc.Evidence = append(pc.Evidence, obj.Resource)
		}
		out.Claims = append(out.Claims, pc)
	}
	return out, nil
}

func newRunSubmit() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "submit <jobId>",
		Short: "Complete the current page with sparse Claim decisions (JSON from --file or stdin)",
		Args:  cobra.ExactArgs(1),
		RunE: jsonCmd(func(cmd *cobra.Command, args []string) (any, error) {
			var in proposalInput
			if err := readInput(cmd, file, &in); err != nil {
				return nil, err
			}
			p, err := in.proposal()
			if err != nil {
				return nil, err
			}
			r, _, err := openRun()
			if err != nil {
				return nil, err
			}
			remaining, err := r.SubmitPage(args[0], p)
			if err != nil {
				return nil, err
			}
			next := "Continue with `owcli run next`."
			if remaining == 0 {
				next = "All pages are done: run `owcli run finish`."
			}
			return map[string]any{"status": "complete", "remaining": remaining, "next": next}, nil
		}),
	}
	cmd.Flags().StringVar(&file, "file", "", "read JSON from this file (keep it outside the repository); - or empty reads stdin")
	return cmd
}

func newRunSkip() *cobra.Command {
	return &cobra.Command{
		Use:   "skip <jobId>",
		Short: "Abandon the current page: restore it and leave it for a later run",
		Args:  cobra.ExactArgs(1),
		RunE: jsonCmd(func(_ *cobra.Command, args []string) (any, error) {
			r, _, err := openRun()
			if err != nil {
				return nil, err
			}
			if err := r.SkipSaved(args[0]); err != nil {
				return nil, err
			}
			return map[string]any{"status": "skipped", "next": "Continue with `owcli run next`."}, nil
		}),
	}
}

func newRunFinish() *cobra.Command {
	return &cobra.Command{
		Use:   "finish",
		Short: "Finalize the run once no page is pending",
		Args:  cobra.NoArgs,
		RunE: jsonCmd(func(*cobra.Command, []string) (any, error) {
			r, l, err := openRun()
			if err != nil {
				return nil, err
			}
			s := r.State()
			res, err := r.FinishSaved()
			if err != nil {
				return nil, err
			}
			out := map[string]any{"status": res.Status, "sourceChanged": res.SourceChanged, "finishedAt": time.Now().UTC().Format(time.RFC3339)}
			written := 0
			for _, j := range s.Plan.Jobs {
				if j.Status == run.Complete {
					written++
				}
			}
			sha, err := commitExternal(l, s.Mode, res.Status, written, len(res.Skipped), s.RunID, s.GitHead)
			if err != nil {
				return nil, fmt.Errorf("the run finished but committing the wiki failed: %w", err)
			}
			if sha != "" {
				out["wikiCommit"] = sha
			}
			if len(res.Skipped) > 0 {
				out["skipped"] = res.Skipped
			}
			if len(res.Deleted) > 0 {
				out["deleted"] = res.Deleted
			}
			if n := len(res.Report.Links.Issues); n > 0 {
				out["brokenLinks"] = n
			}
			if m := res.Report.Metadata.MissingDescriptionPages; len(m) > 0 {
				out["pagesWithoutDescription"] = m
			}
			return out, nil
		}),
	}
}

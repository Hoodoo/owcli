package run

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"owcli/internal/claims"
	"owcli/internal/evidence"
	"owcli/internal/ignore"
	"owcli/internal/okf"
	"owcli/internal/store"
)

// Language is the only wiki language owcli writes.
const Language = "en"

// QuickstartPage is the mandatory entry page, generated last.
const QuickstartPage = "/openwiki/quickstart.md"

// DefaultInstructions seeds INSTRUCTIONS.md on init when the user has none.
const DefaultInstructions = `---
type: Repository guide
title: Wiki Instructions
description: Guidance for generating and maintaining this repository's wiki.
---

Write a practical engineering wiki for this repository. Cover a quickstart,
the architecture, a map of the source tree, the main workflows, the domain
concepts, operations and configuration, testing, and integration points.
Ground every statement in the current source and tests, and use the Git
history to explain why things are the way they are. Prefer concrete
navigation for engineers over generic summaries.
`

// Env is what a run needs from its caller.
type Env struct {
	Layout   store.Layout
	Producer string // actor stamped into provenance and verification, e.g. "owcli/0.1.0"
	Model    string // recorded in last-update metadata
	Now      func() time.Time
}

func (e Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e Env) stamp() string { return okf.Timestamp(e.now()) }

// Run is an active generation run.
type Run struct {
	env      Env
	state    *State
	ignore   *ignore.Matcher
	resolver evidence.Resolver
	store    *claims.Store
	rt       *claims.Runtime
	wiki     okf.Wiki
}

// BeginResult describes how Begin started.
type BeginResult struct {
	Noop            bool // a clean update: nothing to do, no run was started
	Resumed         bool
	PlanInvalidated bool // source changed since the plan was made
	Phase           Phase
	Issues          []claims.Issue // grounding issues found by preflight
}

// State returns a copy of the checkpointed state.
func (r *Run) State() *State { return r.state.clone() }

// Begin starts a fresh run, resumes an interrupted one, or reports that a
// clean update has nothing to do. message is the user's request, if any.
func Begin(env Env, mode Mode, message string) (*Run, BeginResult, error) {
	if mode != Init && mode != Update {
		return nil, BeginResult{}, newErr(InvalidInput, "unknown mode %q", mode)
	}
	if strings.TrimSpace(env.Producer) == "" {
		return nil, BeginResult{}, newErr(InvalidInput, "a producer actor is required")
	}
	l := env.Layout
	if err := os.MkdirAll(l.WikiRoot, 0o755); err != nil {
		return nil, BeginResult{}, err
	}
	m, err := ignore.Load(l.RepoRoot)
	if err != nil {
		return nil, BeginResult{}, err
	}
	m = m.Exclude(l.RepoExclusions()...)
	resolver, err := evidence.NewRepoResolver(l.RepoRoot, m)
	if err != nil {
		return nil, BeginResult{}, err
	}
	r := &Run{env: env, ignore: m, resolver: resolver, store: claims.NewStore(l), wiki: okf.ForLayout(l)}

	state, err := loadState(l)
	if err != nil {
		return nil, BeginResult{}, err
	}
	if state != nil {
		return r.resume(state, mode, message)
	}
	if mode == Init {
		return r.beginInit(message)
	}
	return r.beginUpdate(message)
}

func (r *Run) resume(s *State, mode Mode, message string) (*Run, BeginResult, error) {
	if s.Mode != mode {
		return nil, BeginResult{}, newErr(Conflict, "an interrupted %s run exists; resume it with `owcli %s` or remove %s", s.Mode, s.Mode, r.env.Layout.RunPath())
	}
	next := s.clone()
	if next.Plan != nil {
		for i := range next.Plan.Jobs {
			if next.Plan.Jobs[i].Status == Skipped {
				next.Plan.Jobs[i].Status = Pending
			}
		}
	}
	fp, err := fingerprint(r.env.Layout.RepoRoot, r.ignore)
	if err != nil {
		return nil, BeginResult{}, err
	}
	res := BeginResult{Resumed: true}
	if fp != next.SourceFingerprint {
		head, err := gitHead(r.env.Layout.RepoRoot)
		if err != nil {
			return nil, BeginResult{}, err
		}
		next.SourceFingerprint, next.GitHead = fp, head
		next.Plan, next.Phase = nil, Planning
		res.PlanInvalidated = true
	}
	if next.Plan == nil && message != "" {
		next.Message = message
	}
	next.Actor, next.Model = r.env.Producer, r.env.Model
	if err := r.prepareClaims(false); err != nil {
		return nil, BeginResult{}, err
	}
	if err := saveState(r.env.Layout, next); err != nil {
		return nil, BeginResult{}, err
	}
	r.state = next
	res.Phase, res.Issues = next.Phase, r.rt.Issues
	return r, res, nil
}

func (r *Run) beginUpdate(message string) (*Run, BeginResult, error) {
	l := r.env.Layout
	pages, err := r.store.DiscoverPages()
	if err != nil {
		return nil, BeginResult{}, err
	}
	if len(pages) == 0 {
		return nil, BeginResult{}, newErr(InvalidState, "there is no wiki to update yet; run `owcli init` first")
	}
	if err := r.wiki.Migrate(""); err != nil {
		return nil, BeginResult{}, err
	}
	if err := r.prepareClaims(false); err != nil {
		return nil, BeginResult{}, err
	}
	if message == "" {
		noop, err := r.cleanUpdate(pages)
		if err != nil {
			return nil, BeginResult{}, err
		}
		if noop {
			return nil, BeginResult{Noop: true}, nil
		}
	}
	s, err := r.newState(Update, message, pages)
	if err != nil {
		return nil, BeginResult{}, err
	}
	if err := saveState(l, s); err != nil {
		return nil, BeginResult{}, err
	}
	r.state = s
	return r, BeginResult{Phase: Planning, Issues: r.rt.Issues}, nil
}

// cleanUpdate reports whether an update has nothing to do: the last run
// completed at the current HEAD, the source tree is clean, no Claim has a
// grounding issue, and every page is covered by the manifest as written.
func (r *Run) cleanUpdate(pages []string) (bool, error) {
	l := r.env.Layout
	lu, err := l.LoadLastUpdate()
	if err != nil || lu == nil || lu.Status != store.StatusComplete || len(r.rt.Issues) > 0 {
		return false, err
	}
	head, err := gitHead(l.RepoRoot)
	if err != nil || head != lu.GitHead {
		return false, err
	}
	clean, err := worktreeClean(l.RepoRoot, r.ignore)
	if err != nil || !clean {
		return false, err
	}
	m, err := l.LoadManifest()
	if err != nil {
		return false, err
	}
	for _, p := range pages {
		e, ok := m.Pages[p]
		if !ok {
			return false, nil
		}
		hash, err := r.store.HashPage(p)
		if err != nil || hash != e.PageVersion {
			return false, err
		}
	}
	return true, nil
}

// beginInit replaces the wiki with a blank one (keeping INSTRUCTIONS.md).
// The previous wiki is backed up and restored if anything fails before the
// new checkpoint is durable.
func (r *Run) beginInit(message string) (*Run, BeginResult, error) {
	l := r.env.Layout
	backup, err := os.MkdirTemp("", "owcli-init-backup-")
	if err != nil {
		return nil, BeginResult{}, err
	}
	defer os.RemoveAll(backup)
	if err := copyTree(l.WikiRoot, backup); err != nil {
		return nil, BeginResult{}, fmt.Errorf("back up wiki: %w", err)
	}
	restore := func(cause error) (*Run, BeginResult, error) {
		if err := clearDir(l.WikiRoot, nil); err == nil {
			_ = copyTree(backup, l.WikiRoot)
		}
		return nil, BeginResult{}, cause
	}
	if err := clearDir(l.WikiRoot, map[string]bool{"INSTRUCTIONS.md": true}); err != nil {
		return restore(err)
	}
	if _, err := os.Stat(l.InstructionsPath()); errors.Is(err, fs.ErrNotExist) {
		if err := store.WriteFileAtomic(l.InstructionsPath(), []byte(DefaultInstructions), 0o644); err != nil {
			return restore(err)
		}
	}
	if err := r.prepareClaims(true); err != nil {
		return restore(err)
	}
	s, err := r.newState(Init, message, nil)
	if err != nil {
		return restore(err)
	}
	if err := saveState(l, s); err != nil {
		return restore(err)
	}
	if err := r.writeLastUpdate(s, store.StatusInterrupted); err != nil {
		_ = store.RemoveIfExists(l.RunPath())
		return restore(err)
	}
	r.state = s
	return r, BeginResult{Phase: Planning}, nil
}

func (r *Run) newState(mode Mode, message string, pages []string) (*State, error) {
	l := r.env.Layout
	fp, err := fingerprint(l.RepoRoot, r.ignore)
	if err != nil {
		return nil, err
	}
	head, err := gitHead(l.RepoRoot)
	if err != nil {
		return nil, err
	}
	snap, err := r.wiki.SnapshotProvenance()
	if err != nil {
		return nil, err
	}
	if pages == nil {
		pages = []string{}
	}
	return &State{
		SchemaVersion: stateSchemaVersion, Producer: stateProducer, RunID: newID(),
		Mode: mode, Phase: Planning, Language: Language, Actor: r.env.Producer, Model: r.env.Model,
		Message: message, StartedAt: r.env.stamp(), GitHead: head, SourceFingerprint: fp,
		InitialPages: pages, Provenance: snap,
	}, nil
}

// prepareClaims (re)builds the Claims runtime from durable state.
func (r *Run) prepareClaims(fresh bool) error {
	rt, err := claims.Prepare(r.store, r.resolver, r.env.Producer, fresh)
	if err != nil {
		return err
	}
	r.rt = rt
	return nil
}

// commit persists next and then makes it current.
func (r *Run) commit(next *State) error {
	if err := saveState(r.env.Layout, next); err != nil {
		return err
	}
	r.state = next
	return nil
}

// PlannedPage is one page in a proposed plan.
type PlannedPage struct {
	Path      string   `json:"path"`
	Title     string   `json:"title,omitempty"`
	Purpose   string   `json:"purpose,omitempty"`
	SeedPaths []string `json:"seedPaths,omitempty"`
}

// PlanInput is a proposed plan.
type PlanInput struct {
	Pages        []PlannedPage `json:"pages"`
	Deletions    []string      `json:"deletions,omitempty"`
	Instructions string        `json:"instructions,omitempty"`
}

// SubmitPlan validates and installs the page queue. Updates gain jobs for
// pages whose Claims need attention. Jobs are ordered by path with the
// quickstart last, so it can route to the pages written before it.
// Resubmitting the same plan is a no-op.
func (r *Run) SubmitPlan(in PlanInput) error {
	plan, err := r.normalizePlan(in)
	if err != nil {
		return err
	}
	if r.state.Phase == Generating {
		if samePlan(r.state.Plan, plan) {
			return nil
		}
		return newErr(InvalidState, "a different plan is already installed")
	}
	next := r.state.clone()
	next.Plan, next.Phase = plan, Generating
	return r.commit(next)
}

func (r *Run) normalizePlan(in PlanInput) (*Plan, error) {
	if len(in.Pages) == 0 {
		return nil, newErr(InvalidInput, "a plan needs at least one page")
	}
	existing := map[string]bool{}
	pages, err := r.store.DiscoverPages()
	if err != nil {
		return nil, err
	}
	for _, p := range pages {
		existing[p] = true
	}
	plan := &Plan{Instructions: strings.TrimSpace(in.Instructions)}
	seen := map[string]bool{}
	for _, pp := range in.Pages {
		page, err := claims.NormalizeToolPage(pp.Path)
		if err != nil {
			return nil, newErr(InvalidInput, "page %q: %v", pp.Path, err)
		}
		if seen[page] {
			continue
		}
		seen[page] = true
		plan.Jobs = append(plan.Jobs, Job{Page: page, Title: strings.TrimSpace(pp.Title), Purpose: strings.TrimSpace(pp.Purpose), SeedPaths: pp.SeedPaths, Status: Pending})
	}
	deleted := map[string]bool{}
	for _, d := range in.Deletions {
		page, err := claims.NormalizeToolPage(d)
		if err != nil {
			return nil, newErr(InvalidInput, "deletion %q: %v", d, err)
		}
		switch {
		case r.state.Mode == Init:
			return nil, newErr(InvalidInput, "an init plan cannot delete pages")
		case page == QuickstartPage:
			return nil, newErr(InvalidInput, "the quickstart page cannot be deleted")
		case seen[page]:
			return nil, newErr(InvalidInput, "%s cannot be both written and deleted", page)
		case !existing[page]:
			return nil, newErr(InvalidInput, "cannot delete %s: it does not exist", page)
		}
		if !deleted[page] {
			deleted[page] = true
			plan.Deletions = append(plan.Deletions, page)
		}
	}
	if r.state.Mode == Init && !seen[QuickstartPage] {
		return nil, newErr(InvalidInput, "an init plan must include %s", strings.TrimPrefix(QuickstartPage, "/"))
	}
	if r.state.Mode == Update {
		for _, page := range r.rt.Session.PagesWithIssues() {
			if !seen[page] && !deleted[page] {
				seen[page] = true
				plan.Jobs = append(plan.Jobs, Job{Page: page, Reason: "its Claims cite source that changed or disappeared", Status: Pending})
			}
		}
	}
	sort.SliceStable(plan.Jobs, func(i, j int) bool {
		a, b := plan.Jobs[i].Page, plan.Jobs[j].Page
		if (a == QuickstartPage) != (b == QuickstartPage) {
			return b == QuickstartPage
		}
		return a < b
	})
	sort.Strings(plan.Deletions)
	for i := range plan.Jobs {
		plan.Jobs[i].ID = newID()
	}
	return plan, nil
}

func samePlan(a, b *Plan) bool {
	if a == nil || b == nil || len(a.Jobs) != len(b.Jobs) || strings.Join(a.Deletions, "\n") != strings.Join(b.Deletions, "\n") || a.Instructions != b.Instructions {
		return false
	}
	for i := range a.Jobs {
		x, y := a.Jobs[i], b.Jobs[i]
		if x.Page != y.Page || x.Title != y.Title || x.Purpose != y.Purpose || strings.Join(x.SeedPaths, "\n") != strings.Join(y.SeedPaths, "\n") {
			return false
		}
	}
	return true
}

// NextJob is the current pending job with what a worker needs to know.
type NextJob struct {
	Job
	Existing                 bool                    `json:"existing"`
	ExistingClaimCount       int                     `json:"existingClaimCount"`
	ClaimsRequiringAttention []claims.InspectedClaim `json:"claimsRequiringAttention,omitempty"`
	Remaining                int                     `json:"remaining"`
}

// Next returns the first pending job without reserving it, or nil when no
// pending work remains.
func (r *Run) Next() (*NextJob, error) {
	if r.state.Phase != Generating {
		return nil, newErr(InvalidState, "submit a plan first")
	}
	j := r.state.pending()
	if j == nil {
		return nil, nil
	}
	inspected, err := r.rt.Session.Inspect(j.Page)
	if err != nil {
		return nil, err
	}
	n := &NextJob{Job: *j, ExistingClaimCount: len(inspected)}
	for _, c := range inspected {
		if c.Issue != nil {
			n.ClaimsRequiringAttention = append(n.ClaimsRequiringAttention, c)
		}
	}
	if _, err := r.store.ReadMarkdown(j.Page); err == nil {
		n.Existing = true
	}
	for _, job := range r.state.Plan.Jobs {
		if job.Status == Pending {
			n.Remaining++
		}
	}
	return n, nil
}

func (r *Run) current(jobID string) (*Job, error) {
	if r.state.Phase != Generating {
		return nil, newErr(InvalidState, "submit a plan first")
	}
	j := r.state.job(jobID)
	if j == nil {
		return nil, newErr(NotFound, "unknown job %q", jobID)
	}
	if p := r.state.pending(); p == nil || p.ID != jobID {
		return j, newErr(InvalidState, "job %s is not the current pending job", jobID)
	}
	return j, nil
}

// InspectClaims returns the current job page's complete Claim set.
func (r *Run) InspectClaims(jobID string) ([]claims.InspectedClaim, error) {
	j, err := r.current(jobID)
	if err != nil {
		return nil, err
	}
	return r.rt.Session.Inspect(j.Page)
}

// PageSnapshot is a page's state before a worker touched it.
type PageSnapshot struct {
	JobID    string  `json:"jobId"`
	Page     string  `json:"page"`
	Markdown *string `json:"markdown"` // nil when the page did not exist
	Sidecar  []byte  `json:"sidecar"`  // nil when there was no sidecar
}

func (r *Run) sidecarPath(page string) string {
	return filepath.Join(r.env.Layout.ClaimsDir(), filepath.FromSlash(strings.TrimSuffix(strings.TrimPrefix(page, "/openwiki/"), ".md")+".json"))
}

// Snapshot captures the current job's page before a worker runs.
func (r *Run) Snapshot(jobID string) (PageSnapshot, error) {
	j, err := r.current(jobID)
	if err != nil {
		return PageSnapshot{}, err
	}
	snap := PageSnapshot{JobID: j.ID, Page: j.Page}
	content, err := r.store.ReadMarkdown(j.Page)
	switch {
	case err == nil:
		snap.Markdown = &content
	case !errors.Is(err, claims.ErrPageMissing):
		return PageSnapshot{}, err
	}
	data, err := os.ReadFile(r.sidecarPath(j.Page))
	switch {
	case err == nil:
		snap.Sidecar = data
	case !errors.Is(err, fs.ErrNotExist):
		return PageSnapshot{}, err
	}
	return snap, nil
}

// SubmitPage completes the current job: it repairs the page's front matter,
// reconciles the sparse Claim decisions, persists and proves the page's
// Claims, records the page in the manifest, and only then marks the job
// complete. Errors with code InvalidInput are correctable by the worker.
func (r *Run) SubmitPage(jobID string, proposal claims.Proposal) (remaining int, err error) {
	if j := r.state.job(jobID); j != nil && j.Status == Complete {
		return r.remaining(), nil
	}
	j, err := r.current(jobID)
	if err != nil {
		return 0, err
	}
	content, err := r.store.ReadMarkdown(j.Page)
	if errors.Is(err, claims.ErrPageMissing) {
		return 0, newErr(InvalidInput, "%s has not been written yet", strings.TrimPrefix(j.Page, "/"))
	}
	if err != nil {
		return 0, err
	}
	if repaired, changed := okf.Repair(content, j.Page, ""); changed {
		if err := r.store.WriteMarkdown(j.Page, repaired); err != nil {
			return 0, err
		}
	}
	if err := claims.Reconcile(r.rt.Session, j.Page, proposal); err != nil {
		if errors.Is(err, claims.ErrInvalid) {
			return 0, &Error{Code: InvalidInput, Msg: "claims rejected", Err: err}
		}
		return 0, err
	}
	excluded := map[string]bool{}
	for _, other := range r.state.Plan.Jobs {
		if other.Status == Pending && other.ID != j.ID {
			excluded[other.Page] = true
		}
	}
	if err := r.rt.Finalize(r.env.stamp(), excluded); err != nil {
		// Start the next attempt from durable state.
		if perr := r.prepareClaims(false); perr != nil {
			return 0, errors.Join(err, perr)
		}
		return 0, &Error{Code: InvalidInput, Msg: "the page's Claims could not be made durable; recheck the cited source and resubmit", Err: err}
	}
	if err := r.rt.AssertPageDurable(j.Page); err != nil {
		return 0, &Error{Code: InvalidState, Msg: "page durability proof failed; retry submit", Err: err}
	}
	if err := r.recordManifest(j.Page); err != nil {
		return 0, err
	}
	next := r.state.clone()
	next.job(jobID).Status = Complete
	if err := r.commit(next); err != nil {
		return 0, err
	}
	return r.remaining(), nil
}

func (r *Run) remaining() int {
	n := 0
	for _, j := range r.state.Plan.Jobs {
		if j.Status == Pending {
			n++
		}
	}
	return n
}

func (r *Run) recordManifest(page string) error {
	l := r.env.Layout
	m, err := l.LoadManifest()
	if err != nil {
		return err
	}
	hash, err := r.store.HashPage(page)
	if err != nil {
		return err
	}
	m.Pages[page] = store.ManifestEntry{PageVersion: hash, CompletedBy: r.env.Producer, CompletedRunID: r.state.RunID, GitHead: r.state.GitHead, SourceFingerprint: r.state.SourceFingerprint}
	return l.SaveManifest(m)
}

// Skip abandons the current job: the page and its sidecar are restored from
// the snapshot taken before the worker ran, and the job is marked skipped so
// a later run retries it.
func (r *Run) Skip(snap PageSnapshot) error {
	j, err := r.current(snap.JobID)
	if err != nil {
		return err
	}
	if snap.Page != j.Page {
		return newErr(InvalidInput, "snapshot is for %s, not %s", snap.Page, j.Page)
	}
	if err := r.restore(snap); err != nil {
		return err
	}
	if err := r.prepareClaims(false); err != nil {
		return err
	}
	next := r.state.clone()
	next.job(j.ID).Status = Skipped
	if err := r.commit(next); err != nil {
		return err
	}
	return r.writeLastUpdate(next, store.StatusInterrupted)
}

func (r *Run) restore(snap PageSnapshot) error {
	pagePath := filepath.Join(r.env.Layout.WikiRoot, filepath.FromSlash(strings.TrimPrefix(snap.Page, "/openwiki/")))
	if snap.Markdown == nil {
		if err := store.RemoveIfExists(pagePath); err != nil {
			return err
		}
	} else if err := store.WriteFileAtomic(pagePath, []byte(*snap.Markdown), 0o644); err != nil {
		return err
	}
	sidecar := r.sidecarPath(snap.Page)
	if snap.Sidecar == nil {
		return store.RemoveIfExists(sidecar)
	}
	return store.WriteFileAtomic(sidecar, snap.Sidecar, 0o644)
}

// FinishResult summarizes a finished run.
type FinishResult struct {
	Status        string   // store.StatusComplete or store.StatusInterrupted
	SourceChanged bool     // the source changed during the run; a later update reconciles it
	Skipped       []string // pages left as they were
	Deleted       []string
	Report        okf.FinalizeReport
}

// Finish runs deterministic finalization once no job is pending: skipped
// pages are restored, abandoned and planned deletions applied, OKF passes
// and Claims finalization run, the whole wiki is proven durable, the
// manifest and last-update metadata are written, and the checkpoint is
// removed last so any earlier failure leaves the run resumable.
func (r *Run) Finish(skipped []PageSnapshot) (FinishResult, error) {
	var res FinishResult
	if r.state.Phase != Generating {
		return res, newErr(InvalidState, "submit a plan first")
	}
	if r.state.pending() != nil {
		return res, newErr(InvalidState, "%d page job(s) are still pending", r.remaining())
	}
	l := r.env.Layout
	skippedPages := map[string]bool{}
	bySnap := map[string]PageSnapshot{}
	for _, s := range skipped {
		bySnap[s.JobID] = s
	}
	planned := map[string]bool{}
	completed := map[string]bool{}
	for _, j := range r.state.Plan.Jobs {
		planned[j.Page] = true
		switch j.Status {
		case Skipped:
			s, ok := bySnap[j.ID]
			if !ok || s.Page != j.Page {
				return res, newErr(InvalidState, "skipped job %s (%s) has no matching snapshot", j.ID, j.Page)
			}
			skippedPages[j.Page] = true
			res.Skipped = append(res.Skipped, strings.TrimPrefix(j.Page, "/"))
		case Complete:
			completed[j.Page] = true
		}
	}
	if len(bySnap) != len(skippedPages) {
		return res, newErr(InvalidState, "snapshots do not match the skipped jobs")
	}

	fp, err := fingerprint(l.RepoRoot, r.ignore)
	if err != nil {
		return res, err
	}
	res.SourceChanged = fp != r.state.SourceFingerprint

	for _, s := range skipped {
		if err := r.restore(s); err != nil {
			return res, err
		}
	}
	if err := r.prepareClaims(false); err != nil {
		return res, err
	}

	initial := map[string]bool{}
	for _, p := range r.state.InitialPages {
		initial[p] = true
	}
	pages, err := r.store.DiscoverPages()
	if err != nil {
		return res, err
	}
	var deletions []string
	for _, p := range pages {
		if !initial[p] && !planned[p] {
			deletions = append(deletions, p) // left behind by a superseded plan
		}
	}
	deletions = append(deletions, r.state.Plan.Deletions...)
	for _, p := range deletions {
		if err := store.RemoveIfExists(filepath.Join(l.WikiRoot, filepath.FromSlash(strings.TrimPrefix(p, "/openwiki/")))); err != nil {
			return res, err
		}
		if err := r.rt.Session.RecordDeletion(p); err != nil {
			return res, err
		}
		res.Deleted = append(res.Deleted, strings.TrimPrefix(p, "/"))
	}

	now := r.env.stamp()
	resources := r.rt.Session.EvidenceResourcesByPage()
	for p := range skippedPages {
		delete(resources, p)
	}
	res.Report, err = r.wiki.Finalize(okf.FinalizeOptions{
		Provenance: r.state.Provenance, Now: now, Producer: r.env.Producer,
		ClaimSources: func() error { return claims.SyncSources(r.store, resources) },
	})
	if err != nil {
		return res, err
	}
	if err := r.rt.Finalize(now, skippedPages); err != nil {
		return res, err
	}
	if err := r.rt.AssertWikiDurable(skippedPages); err != nil {
		return res, err
	}
	if fp2, err := fingerprint(l.RepoRoot, r.ignore); err != nil {
		return res, err
	} else if fp2 != r.state.SourceFingerprint {
		res.SourceChanged = true
	}
	if err := r.rebuildManifest(completed, skippedPages); err != nil {
		return res, err
	}
	res.Status = store.StatusComplete
	if len(skippedPages) > 0 || res.SourceChanged {
		res.Status = store.StatusInterrupted
	}
	if err := r.writeLastUpdate(r.state, res.Status); err != nil {
		return res, err
	}
	if err := store.RemoveIfExists(l.RunPath()); err != nil {
		return res, err
	}
	return res, nil
}

// rebuildManifest stamps pages this run completed with its source
// checkpoint, refreshes the page version of other surviving pages, keeps
// skipped pages' entries untouched, and drops entries for missing pages.
func (r *Run) rebuildManifest(completed, skipped map[string]bool) error {
	l := r.env.Layout
	prior, err := l.LoadManifest()
	if err != nil {
		return err
	}
	pages, err := r.store.DiscoverPages()
	if err != nil {
		return err
	}
	next := store.NewManifest()
	for _, p := range pages {
		old, had := prior.Pages[p]
		if skipped[p] {
			if had {
				next.Pages[p] = old
			}
			continue
		}
		hash, err := r.store.HashPage(p)
		if err != nil {
			return err
		}
		switch {
		case completed[p]:
			next.Pages[p] = store.ManifestEntry{PageVersion: hash, CompletedBy: r.env.Producer, CompletedRunID: r.state.RunID, GitHead: r.state.GitHead, SourceFingerprint: r.state.SourceFingerprint}
		case had:
			old.PageVersion = hash
			next.Pages[p] = old
		}
	}
	return l.SaveManifest(next)
}

func (r *Run) writeLastUpdate(s *State, status string) error {
	return r.env.Layout.SaveLastUpdate(store.LastUpdate{
		UpdatedAt: r.env.stamp(), Command: string(s.Mode), GitHead: s.GitHead,
		Model: modelName(r.env.Model), Status: status, Language: Language,
	})
}

func modelName(m string) string {
	if m == "" {
		return "unknown"
	}
	return m
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// clearDir removes everything in dir except the named top-level entries.
func clearDir(dir string, keep map[string]bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if keep[e.Name()] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// copyTree copies regular files and directories from src into dst.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case d.Type().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(target, data, 0o644)
		}
		return nil
	})
}

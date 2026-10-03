package run

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Hoodoo/owcli/internal/store"
)

// Host-driven runs: an interactive agent drives the lifecycle one command
// at a time, each in a new process. Open reattaches to the checkpoint without
// changing it, and pre-worker snapshots are persisted next to it so skip and
// finish work across processes.

const snapshotDirName = ".run-snapshots"

// Open reattaches to the active run without modifying it. It reports
// NotFound when no run is active.
func Open(env Env) (*Run, error) {
	r, err := newRun(env)
	if err != nil {
		return nil, err
	}
	s, err := loadState(env.Layout)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, newErr(NotFound, "no run is active; start one with `owcli run begin`")
	}
	if err := r.prepareClaims(false); err != nil {
		return nil, err
	}
	r.state = s
	return r, nil
}

func (r *Run) snapshotDir() string { return filepath.Join(r.env.Layout.WikiRoot, snapshotDirName) }

func (r *Run) snapshotPath(jobID string) string {
	return filepath.Join(r.snapshotDir(), jobID+".json")
}

// EnsureSnapshot persists the current job's pre-worker snapshot unless one
// was already saved, so the first capture (taken before any edits) wins.
func (r *Run) EnsureSnapshot(jobID string) error {
	path := r.snapshotPath(jobID)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	snap, err := r.Snapshot(jobID)
	if err != nil {
		return err
	}
	return store.WriteJSONAtomic(path, snap)
}

// SavedSnapshot loads a persisted snapshot.
func (r *Run) SavedSnapshot(jobID string) (PageSnapshot, error) {
	var snap PageSnapshot
	found, err := store.ReadJSON(r.snapshotPath(jobID), &snap)
	if err != nil {
		return snap, err
	}
	if !found {
		return snap, newErr(InvalidState, "no snapshot was saved for job %s; call `owcli run next` before writing the page", jobID)
	}
	return snap, nil
}

// SkipSaved skips the current job using its persisted snapshot.
func (r *Run) SkipSaved(jobID string) error {
	snap, err := r.SavedSnapshot(jobID)
	if err != nil {
		return err
	}
	return r.Skip(snap)
}

// FinishSaved finishes the run using the persisted snapshots of skipped jobs.
func (r *Run) FinishSaved() (FinishResult, error) {
	var snaps []PageSnapshot
	if r.state.Plan != nil {
		for _, j := range r.state.Plan.Jobs {
			if j.Status != Skipped {
				continue
			}
			snap, err := r.SavedSnapshot(j.ID)
			if err != nil {
				return FinishResult{}, err
			}
			snaps = append(snaps, snap)
		}
	}
	return r.Finish(snaps)
}

// ChangedPaths lists source paths that differ from the given commit: commits
// since, plus uncommitted and untracked changes. Wiki and ignored paths are
// left out. An empty or unknown commit yields nil.
func (r *Run) ChangedPaths(since string) ([]string, error) {
	if since == "" {
		return nil, nil
	}
	repo := r.env.Layout.RepoRoot
	if _, err := git(repo, "cat-file", "-e", since+"^{commit}"); err != nil {
		return nil, nil
	}
	set := map[string]bool{}
	add := func(out []byte) {
		for _, p := range strings.Split(string(out), "\x00") {
			if p != "" && !r.ignore.Ignores(p, false) {
				set[p] = true
			}
		}
	}
	out, err := git(repo, "diff", "-z", "--name-only", since)
	if err != nil {
		return nil, err
	}
	add(out)
	out, err = git(repo, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	add(out)
	paths := make([]string, 0, len(set))
	for p := range set {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths, nil
}

// Package run implements the resumable wiki generation lifecycle: begin,
// submit a plan, then for each page job next/submit (or skip), and finish.
// All durable state lives in the wiki's .run.json checkpoint, written before
// in-memory state advances, so an interrupted run resumes page by page.
package run

import (
	"errors"
	"fmt"

	"github.com/Hoodoo/owcli/internal/okf"
	"github.com/Hoodoo/owcli/internal/store"
)

const (
	stateSchemaVersion = 1
	stateProducer      = "owcli"
)

// Mode is the kind of run.
type Mode string

// Modes.
const (
	Init   Mode = "init"
	Update Mode = "update"
)

// Phase is where a run is.
type Phase string

// Phases.
const (
	Planning   Phase = "planning"
	Generating Phase = "generating"
)

// JobStatus is one page job's progress.
type JobStatus string

// Job statuses. Skipped is not terminal: resuming resets it to pending.
const (
	Pending  JobStatus = "pending"
	Complete JobStatus = "complete"
	Skipped  JobStatus = "skipped"
)

// Job is one page to write.
type Job struct {
	ID           string    `json:"id"`
	Page         string    `json:"page"` // canonical "/openwiki/<path>.md"
	Title        string    `json:"title,omitempty"`
	Purpose      string    `json:"purpose,omitempty"`
	SeedPaths    []string  `json:"seedPaths,omitempty"`
	RelatedPages []string  `json:"relatedPages,omitempty"`
	Reason       string    `json:"reason,omitempty"` // why a job was added automatically
	Status       JobStatus `json:"status"`
}

// Plan is the installed, ordered page queue.
type Plan struct {
	Jobs         []Job    `json:"jobs"`
	Deletions    []string `json:"deletions,omitempty"`
	Instructions string   `json:"instructions,omitempty"` // wiki-wide guidance for workers
}

// State is the .run.json checkpoint.
type State struct {
	SchemaVersion     int                    `json:"schemaVersion"`
	Producer          string                 `json:"producer"`
	RunID             string                 `json:"runId"`
	Mode              Mode                   `json:"mode"`
	Phase             Phase                  `json:"phase"`
	Language          string                 `json:"language"`
	Actor             string                 `json:"actor"`
	Model             string                 `json:"model"`
	Message           string                 `json:"message,omitempty"` // the user's request for this run
	StartedAt         string                 `json:"startedAt"`
	GitHead           string                 `json:"gitHead"`
	SourceFingerprint string                 `json:"sourceFingerprint"`
	InitialPages      []string               `json:"initialPages"`
	Provenance        okf.ProvenanceSnapshot `json:"provenance"`
	Plan              *Plan                  `json:"plan,omitempty"`
}

// ErrorCode classifies lifecycle failures.
type ErrorCode string

// Error codes.
const (
	InvalidInput ErrorCode = "invalid_input" // the caller (or model) can fix the request
	InvalidState ErrorCode = "invalid_state" // the operation does not fit the run's state
	Conflict     ErrorCode = "conflict"      // a different run owns the checkpoint
	NotFound     ErrorCode = "not_found"
)

// Error is a lifecycle error.
type Error struct {
	Code ErrorCode
	Msg  string
	Err  error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Msg, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Msg)
}

func (e *Error) Unwrap() error { return e.Err }

func newErr(code ErrorCode, format string, args ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// IsCode reports whether err is a lifecycle error with the given code.
func IsCode(err error, code ErrorCode) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

func loadState(l store.Layout) (*State, error) {
	var s State
	found, err := store.ReadJSON(l.RunPath(), &s)
	if err != nil {
		return nil, &Error{Code: InvalidState, Msg: "run checkpoint is unreadable; fix or remove " + l.RunPath(), Err: err}
	}
	if !found {
		return nil, nil
	}
	if s.SchemaVersion != stateSchemaVersion || s.Producer != stateProducer {
		return nil, newErr(InvalidState, "run checkpoint %s was written by another tool or version (producer %q, schema %d)", l.RunPath(), s.Producer, s.SchemaVersion)
	}
	if s.RunID == "" || (s.Mode != Init && s.Mode != Update) || (s.Phase != Planning && s.Phase != Generating) || (s.Phase == Generating) != (s.Plan != nil) {
		return nil, newErr(InvalidState, "run checkpoint %s is inconsistent", l.RunPath())
	}
	return &s, nil
}

func saveState(l store.Layout, s *State) error {
	return store.WriteJSONAtomic(l.RunPath(), s)
}

// pending returns the first pending job, if any.
func (s *State) pending() *Job {
	if s.Plan == nil {
		return nil
	}
	for i := range s.Plan.Jobs {
		if s.Plan.Jobs[i].Status == Pending {
			return &s.Plan.Jobs[i]
		}
	}
	return nil
}

func (s *State) job(id string) *Job {
	if s.Plan == nil {
		return nil
	}
	for i := range s.Plan.Jobs {
		if s.Plan.Jobs[i].ID == id {
			return &s.Plan.Jobs[i]
		}
	}
	return nil
}

// clone deep-copies the state so mutations can be persisted before they
// replace the in-memory state.
func (s *State) clone() *State {
	c := *s
	c.InitialPages = append([]string(nil), s.InitialPages...)
	c.Provenance = append(okf.ProvenanceSnapshot(nil), s.Provenance...)
	if s.Plan != nil {
		p := *s.Plan
		p.Jobs = append([]Job(nil), s.Plan.Jobs...)
		for i := range p.Jobs {
			p.Jobs[i].SeedPaths = append([]string(nil), p.Jobs[i].SeedPaths...)
			p.Jobs[i].RelatedPages = append([]string(nil), p.Jobs[i].RelatedPages...)
		}
		p.Deletions = append([]string(nil), s.Plan.Deletions...)
		c.Plan = &p
	}
	return &c
}

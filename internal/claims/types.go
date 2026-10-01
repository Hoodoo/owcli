// Package claims grounds wiki pages in versioned repository evidence.
//
// A Claim is one atomic, independently verifiable proposition backed by one
// or more evidence citations. Each grounded page owns a complete Claim set,
// persisted in a JSON sidecar under the wiki's .claims directory. The package
// splits responsibilities into a Store (persistence), a Session (run-scoped
// working state and mutations), and a Runtime (preflight, finalization, and
// projection into OKF front matter).
package claims

import (
	"errors"
	"fmt"

	"owcli/internal/evidence"
)

// SchemaVersion is the sidecar format version.
const SchemaVersion = 1

// Claim is one grounded proposition.
type Claim struct {
	ID        string              `json:"id"`
	Statement string              `json:"statement"`
	Evidence  []evidence.Evidence `json:"evidence"`
}

// Verification records who durably verified a page's Claims and when.
type Verification struct {
	By string `json:"by"`
	At string `json:"at"`
}

// PageClaims is a persisted sidecar.
type PageClaims struct {
	SchemaVersion int           `json:"schemaVersion"`
	PageVersion   string        `json:"pageVersion"` // "sha256:<hex>" of the page's bytes
	Claims        []Claim       `json:"claims"`
	Verification  *Verification `json:"verification,omitempty"`
}

// OpKind names a mutation.
type OpKind string

// Mutation kinds.
const (
	OpAdd     OpKind = "add"
	OpConfirm OpKind = "confirm"
	OpUpdate  OpKind = "update"
	OpRetract OpKind = "retract"
)

// Operation is one mutation. Add uses Statement and Evidence; confirm and
// retract use ID; update uses ID plus a new Statement and/or Evidence.
type Operation struct {
	Op        OpKind
	ID        string
	Statement *string
	Evidence  []string // resources; nil means "unchanged" for update
}

// IssueKind classifies a preflight finding.
type IssueKind string

// Issue kinds. Unresolved takes precedence over stale.
const (
	Stale      IssueKind = "stale"
	Unresolved IssueKind = "unresolved"
)

// Issue says a Claim's evidence must be rechecked against current source.
type Issue struct {
	Page      string    `json:"page"`
	Kind      IssueKind `json:"kind"`
	ClaimID   string    `json:"claimId"`
	Resources []string  `json:"resources"`
}

// InspectedClaim is the model-facing view: no opaque versions.
type InspectedClaim struct {
	ID        string    `json:"id"`
	Statement string    `json:"statement"`
	Evidence  []string  `json:"evidence"`
	Issue     *IssueRef `json:"issue,omitempty"`
}

// IssueRef is the issue attached to an inspected Claim.
type IssueRef struct {
	Kind      IssueKind `json:"kind"`
	Resources []string  `json:"resources"`
}

// Error classes. ErrInvalid is correctable by whoever proposed the change;
// ErrSecurity is never recoverable.
var (
	ErrInvalid     = errors.New("invalid claims input")
	ErrPersistence = errors.New("claims persistence failure")
	ErrPageMissing = errors.New("wiki page missing")
	ErrSecurity    = errors.New("claims containment violation")
)

func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

func persistencef(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrPersistence, fmt.Sprintf(format, args...))
}

func cloneClaims(in []Claim) []Claim {
	out := make([]Claim, len(in))
	for i, c := range in {
		out[i] = Claim{ID: c.ID, Statement: c.Statement, Evidence: append([]evidence.Evidence(nil), c.Evidence...)}
	}
	return out
}

// resources returns a Claim's evidence resources in stored order.
func (c Claim) resources() []string {
	out := make([]string, len(c.Evidence))
	for i, e := range c.Evidence {
		out[i] = e.Resource
	}
	return out
}

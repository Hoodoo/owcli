package claims

import (
	"errors"
	"fmt"
	"sort"

	"github.com/Hoodoo/owcli/internal/evidence"
)

// pageState is one page's run-scoped Claim state.
type pageState struct {
	claims       []Claim
	verification *Verification
	persisted    bool // a sidecar on disk matches claims
	dirty        bool // claims changed since last persist
	deleted      bool
	issues       []Issue
}

// Session holds mutable Claim state for one run. Claim ids are unique across
// all pages. It is not safe for concurrent use.
type Session struct {
	resolver evidence.Resolver
	pages    map[string]*pageState
	owners   map[string]string // claim id -> page
	orphans  []string
	newID    func() string
}

// NewSession builds a session from persisted sidecars, preflight issues, and
// sidecars whose pages no longer exist. newID may be nil.
func NewSession(resolver evidence.Resolver, persisted map[string]*PageClaims, issues []Issue, orphans []string, newID func() string) (*Session, error) {
	if newID == nil {
		newID = NewClaimID
	}
	s := &Session{resolver: resolver, pages: map[string]*pageState{}, owners: map[string]string{}, newID: newID}
	seen := map[string]bool{}
	for _, o := range orphans {
		p, err := NormalizePage(o)
		if err != nil {
			return nil, err
		}
		if !seen[p] {
			seen[p] = true
			s.orphans = append(s.orphans, p)
		}
	}
	sort.Strings(s.orphans)
	for _, in := range sortedKeys(persisted) {
		page, err := NormalizePage(in)
		if err != nil {
			return nil, err
		}
		if _, dup := s.pages[page]; dup {
			return nil, invalidf("duplicate persisted claim page: %s", page)
		}
		pc := persisted[in]
		if err := s.checkOwnership(page, pc.Claims); err != nil {
			return nil, err
		}
		for _, c := range pc.Claims {
			s.owners[c.ID] = page
		}
		st := &pageState{claims: cloneClaims(pc.Claims), persisted: true}
		if pc.Verification != nil {
			v := *pc.Verification
			st.verification = &v
		}
		for _, is := range issues {
			if is.Page == page {
				st.issues = append(st.issues, cloneIssue(is))
			}
		}
		s.pages[page] = st
	}
	return s, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func cloneIssue(is Issue) Issue {
	is.Resources = append([]string(nil), is.Resources...)
	return is
}

func (s *Session) page(page string) *pageState {
	st, ok := s.pages[page]
	if !ok {
		st = &pageState{}
		s.pages[page] = st
	}
	return st
}

// Result reports the id each operation targeted or allocated.
type Result struct {
	Op OpKind
	ID string
}

// Mutate applies a batch to one page. On success the page becomes dirty and
// grounding issues of the targeted Claims are cleared.
func (s *Session) Mutate(pageIn string, ops []Operation) ([]Result, error) {
	page, err := NormalizePage(pageIn)
	if err != nil {
		return nil, err
	}
	st := s.page(page)
	before := map[string]bool{}
	for _, c := range st.claims {
		before[c.ID] = true
	}
	next, err := Apply(st.claims, ops, s.resolver, s.allocateID)
	if err != nil {
		return nil, err
	}
	if err := s.checkOwnership(page, next); err != nil {
		return nil, err
	}
	s.replaceOwnership(page, st.claims, next)
	st.claims = next

	var added []string
	for _, c := range next {
		if !before[c.ID] {
			added = append(added, c.ID)
		}
	}
	results := make([]Result, len(ops))
	targeted := map[string]bool{}
	for i, op := range ops {
		id := op.ID
		if op.Op == OpAdd {
			id, added = added[0], added[1:]
		}
		results[i] = Result{Op: op.Op, ID: id}
		targeted[id] = true
	}
	st.dirty, st.deleted = true, false
	kept := st.issues[:0]
	for _, is := range st.issues {
		if !targeted[is.ClaimID] {
			kept = append(kept, is)
		}
	}
	st.issues = kept
	return results, nil
}

// Inspect returns a page's Claims for a model, with issues attached and
// versions stripped. It has no side effects beyond registering the page.
func (s *Session) Inspect(pageIn string) ([]InspectedClaim, error) {
	page, err := NormalizePage(pageIn)
	if err != nil {
		return nil, err
	}
	st := s.page(page)
	if st.deleted {
		return []InspectedClaim{}, nil
	}
	out := make([]InspectedClaim, 0, len(st.claims))
	for _, c := range st.claims {
		ic := InspectedClaim{ID: c.ID, Statement: c.Statement, Evidence: c.resources()}
		for _, is := range st.issues {
			if is.ClaimID == c.ID {
				ic.Issue = &IssueRef{Kind: is.Kind, Resources: append([]string(nil), is.Resources...)}
				break
			}
		}
		out = append(out, ic)
	}
	return out, nil
}

// Issues returns the outstanding grounding issues of a page.
func (s *Session) Issues(page string) []Issue {
	st, ok := s.pages[page]
	if !ok {
		return nil
	}
	out := make([]Issue, len(st.issues))
	for i, is := range st.issues {
		out[i] = cloneIssue(is)
	}
	return out
}

// PagesWithIssues lists pages that still carry grounding issues.
func (s *Session) PagesWithIssues() []string {
	var out []string
	for _, p := range sortedKeys(s.pages) {
		if st := s.pages[p]; !st.deleted && len(st.issues) > 0 {
			out = append(out, p)
		}
	}
	return out
}

// HasClaim reports whether any page owns id.
func (s *Session) HasClaim(id string) bool { _, ok := s.owners[id]; return ok }

// EvidenceResourcesByPage maps every live page to its sorted, unique evidence
// resources (empty for pages with no Claims).
func (s *Session) EvidenceResourcesByPage() map[string][]string {
	out := map[string][]string{}
	for page, st := range s.pages {
		if st.deleted {
			continue
		}
		set := map[string]bool{}
		for _, c := range st.claims {
			for _, e := range c.Evidence {
				set[e.Resource] = true
			}
		}
		out[page] = sortedKeys(set)
	}
	return out
}

// RecordDeletion drops a page's Claims; finalize removes its sidecar.
func (s *Session) RecordDeletion(pageIn string) error {
	page, err := NormalizePage(pageIn)
	if err != nil {
		return err
	}
	st := s.page(page)
	s.replaceOwnership(page, st.claims, nil)
	st.deleted, st.dirty, st.issues = true, false, nil
	return nil
}

// FinalizeResult is the outcome of Finalize.
type FinalizeResult struct {
	// Verification maps each live page to its active durable verification,
	// or nil when the page is not eligible for a machine stamp.
	Verification map[string]*Verification
	// Warnings are recoverable per-page failures. Callers must treat any
	// warning as a durability failure.
	Warnings []string
}

// Finalize persists every dirty page that has no evidence debt and whose
// evidence is still current, removes sidecars of orphaned, missing, and
// deleted pages, and reports which pages may carry a verification stamp.
// Pages in excluded are left entirely untouched.
func (s *Session) Finalize(st *Store, v Verification, excluded map[string]bool) (FinalizeResult, error) {
	resolver := evidence.Cached(s.resolver)
	res := FinalizeResult{Verification: map[string]*Verification{}}
	warn := func(page, action string, err error) error {
		if !recoverable(err) {
			return err
		}
		res.Warnings = append(res.Warnings, fmt.Sprintf("could not %s Claims for %s; its sidecar was left unchanged: %v", action, page, err))
		return nil
	}

	type ready struct {
		page string
		st   *pageState
		hash string
	}
	var readyPages []ready
	var missing []string
	for _, page := range sortedKeys(s.pages) {
		ps := s.pages[page]
		if excluded[page] || ps.deleted || !ps.dirty {
			continue
		}
		err := func() error {
			if len(ps.issues) > 0 {
				return invalidf("unresolved evidence debt remains for %s", page)
			}
			if err := assertCurrent(page, ps.claims, resolver); err != nil {
				return err
			}
			hash, err := st.HashPage(page)
			if err != nil {
				return err
			}
			readyPages = append(readyPages, ready{page, ps, hash})
			return nil
		}()
		if errors.Is(err, ErrPageMissing) {
			missing = append(missing, page)
			res.Warnings = append(res.Warnings, fmt.Sprintf("could not verify Claims for %s because its Markdown disappeared; its sidecar will be removed", page))
			continue
		}
		if err != nil {
			if err := warn(page, "verify", err); err != nil {
				return res, err
			}
		}
	}

	var removals []string
	removals = append(removals, s.orphans...)
	removals = append(removals, missing...)
	for _, page := range sortedKeys(s.pages) {
		if s.pages[page].deleted {
			removals = append(removals, page)
		}
	}
	for _, page := range removals {
		if excluded[page] {
			continue
		}
		if err := st.Delete(page); err != nil {
			if err := warn(page, "remove", err); err != nil {
				return res, err
			}
		}
	}

	for _, r := range readyPages {
		var next *Verification
		if len(r.st.claims) > 0 {
			vv := v
			next = &vv
		}
		err := st.Write(r.page, PageClaims{SchemaVersion: SchemaVersion, PageVersion: r.hash, Claims: cloneClaims(r.st.claims), Verification: next})
		if err != nil {
			if err := warn(r.page, "persist", err); err != nil {
				return res, err
			}
			continue
		}
		r.st.verification, r.st.persisted, r.st.dirty = next, true, false
	}

	for _, page := range sortedKeys(s.pages) {
		ps := s.pages[page]
		if excluded[page] || ps.deleted {
			continue
		}
		if ps.persisted && !ps.dirty && len(ps.claims) > 0 && len(ps.issues) == 0 && ps.verification != nil {
			vv := *ps.verification
			res.Verification[page] = &vv
		} else {
			res.Verification[page] = nil
		}
	}
	return res, nil
}

// RefreshPageVersions rewrites the pageVersion of clean persisted pages after
// deterministic passes changed their bytes. It returns pages it could not
// refresh plus warnings.
func (s *Session) RefreshPageVersions(st *Store, pages []string) (failed []string, warnings []string, err error) {
	set := map[string]bool{}
	for _, p := range pages {
		set[p] = true
	}
	for _, page := range sortedKeys(set) {
		ps, ok := s.pages[page]
		if !ok || !ps.persisted || ps.deleted || ps.dirty {
			continue
		}
		e := func() error {
			hash, err := st.HashPage(page)
			if err != nil {
				return err
			}
			return st.Write(page, PageClaims{SchemaVersion: SchemaVersion, PageVersion: hash, Claims: cloneClaims(ps.claims), Verification: ps.verification})
		}()
		if e != nil {
			if !recoverable(e) {
				return failed, warnings, e
			}
			failed = append(failed, page)
			warnings = append(warnings, fmt.Sprintf("could not synchronize Claims for %s: %v", page, e))
		}
	}
	return failed, warnings, nil
}

func assertCurrent(page string, claims []Claim, r evidence.Resolver) error {
	for _, c := range claims {
		for _, e := range c.Evidence {
			cur, err := r.Resolve(e.Resource, e.Version)
			if err != nil {
				if errors.Is(err, evidence.ErrInvalidResource) {
					return invalidf("%v", err)
				}
				return err
			}
			if cur == nil {
				return invalidf("evidence disappeared before finalizing %s: %s", page, e.Resource)
			}
			if cur.Evidence.Version != e.Version {
				return invalidf("evidence changed before finalizing %s: %s", page, e.Resource)
			}
		}
	}
	return nil
}

// recoverable errors become finalization warnings; security errors and
// unexpected failures abort.
func recoverable(err error) bool {
	if errors.Is(err, ErrSecurity) || errors.Is(err, evidence.ErrSecurity) {
		return false
	}
	return errors.Is(err, ErrInvalid) || errors.Is(err, ErrPersistence) || errors.Is(err, ErrPageMissing)
}

func (s *Session) allocateID() string {
	for i := 0; i < 32; i++ {
		if id := s.newID(); !s.HasClaim(id) {
			return id
		}
	}
	return "" // rejected as empty by Apply's canonical id check
}

func (s *Session) checkOwnership(page string, claims []Claim) error {
	ids := map[string]bool{}
	for _, c := range claims {
		if ids[c.ID] {
			return invalidf("duplicate claim id %s within %s", c.ID, page)
		}
		ids[c.ID] = true
		if owner, ok := s.owners[c.ID]; ok && owner != page {
			return invalidf("duplicate claim id %s across %s and %s", c.ID, owner, page)
		}
	}
	return nil
}

func (s *Session) replaceOwnership(page string, prev, next []Claim) {
	keep := map[string]bool{}
	for _, c := range next {
		keep[c.ID] = true
	}
	for _, c := range prev {
		if !keep[c.ID] && s.owners[c.ID] == page {
			delete(s.owners, c.ID)
		}
	}
	for _, c := range next {
		s.owners[c.ID] = page
	}
}

package claims

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"owcli/internal/evidence"
	"owcli/internal/okf"
)

// Runtime wires a store, resolver, and session for one generation run.
type Runtime struct {
	Store    *Store
	Session  *Session
	Issues   []Issue // preflight issues at start
	producer string
}

// Prepare builds a Runtime. A fresh init starts from empty Claim state but
// still treats existing sidecars as orphans to clean up; anything else runs
// preflight first.
func Prepare(st *Store, r evidence.Resolver, producer string, freshInit bool) (*Runtime, error) {
	if strings.TrimSpace(producer) == "" {
		return nil, errors.New("claims runtime requires a producer actor")
	}
	if freshInit {
		orphans, err := st.DiscoverSidecarPages()
		if err != nil {
			return nil, err
		}
		s, err := NewSession(r, nil, nil, orphans, nil)
		if err != nil {
			return nil, err
		}
		return &Runtime{Store: st, Session: s, producer: producer}, nil
	}
	pf, err := RunPreflight(st, r)
	if err != nil {
		return nil, err
	}
	s, err := NewSession(r, pf.Persisted, pf.Issues, pf.Orphans, nil)
	if err != nil {
		return nil, err
	}
	return &Runtime{Store: st, Session: s, Issues: pf.Issues, producer: producer}, nil
}

// Finalize persists Claims, projects verification into front matter, and
// refreshes page versions so sidecars describe final bytes. Any per-page
// warning is returned as an error: finalization is all-or-nothing from the
// caller's point of view, though work already persisted stays persisted.
func (rt *Runtime) Finalize(at string, excluded map[string]bool) error {
	res, err := rt.Session.Finalize(rt.Store, Verification{By: rt.producer, At: at}, excluded)
	if err != nil {
		return err
	}
	warnings := res.Warnings

	originals, err := SyncVerification(rt.Store, res.Verification, excluded)
	if err != nil {
		return err
	}
	pages := make([]string, 0, len(res.Verification))
	for p := range res.Verification {
		pages = append(pages, p)
	}
	sort.Strings(pages)
	failed, w, err := rt.Session.RefreshPageVersions(rt.Store, pages)
	if err != nil {
		return err
	}
	warnings = append(warnings, w...)
	var unsafe []string
	for _, p := range failed {
		if res.Verification[p] != nil {
			unsafe = append(unsafe, p)
		}
	}
	if len(unsafe) > 0 {
		if err := RollbackVerification(rt.Store, originals, unsafe); err != nil {
			return err
		}
	}
	if len(warnings) > 0 {
		return fmt.Errorf("%w: claims finalization was not fully durable: %s", ErrPersistence, strings.Join(warnings, "; "))
	}
	return nil
}

// AssertPageDurable re-reads a page's sidecar and proves it matches the
// session: persisted, hashed against current bytes, verified, verification
// projected into front matter, and the same Claims with the same evidence.
func (rt *Runtime) AssertPageDurable(page string) error {
	pc, err := rt.Store.Load(page)
	if err != nil {
		return err
	}
	if pc == nil {
		return fmt.Errorf("%w: claims for %s were not durably persisted", ErrPersistence, page)
	}
	content, err := rt.Store.ReadMarkdown(page)
	if err != nil {
		return err
	}
	if pc.PageVersion != HashContent(content) {
		return fmt.Errorf("%w: claims for %s do not match the current Markdown bytes", ErrPersistence, page)
	}
	if pc.Verification == nil {
		return fmt.Errorf("%w: claims for %s were not verified", ErrPersistence, page)
	}
	fields, _ := okf.Fields(content)
	projected := false
	for _, e := range verificationEvents(fields) {
		if at, _ := e["at"].(string); e["by"] == pc.Verification.By && at == pc.Verification.At {
			projected = true
			break
		}
	}
	if !projected {
		return fmt.Errorf("%w: claims verification for %s was not durably projected", ErrPersistence, page)
	}
	expected, err := rt.Session.Inspect(page)
	if err != nil {
		return err
	}
	durable := map[string]Claim{}
	for _, c := range pc.Claims {
		durable[c.ID] = c
	}
	if len(durable) != len(expected) {
		return fmt.Errorf("%w: claims for %s were only partially persisted", ErrPersistence, page)
	}
	for _, c := range expected {
		d, ok := durable[c.ID]
		if !ok || d.Statement != c.Statement || !sameSet(d.resources(), c.Evidence) {
			return fmt.Errorf("%w: claims for %s were only partially persisted", ErrPersistence, page)
		}
	}
	return nil
}

// AssertWikiDurable proves no sidecar outlives its page and every page with a
// non-empty Claim set is durable. Excluded pages are skipped.
func (rt *Runtime) AssertWikiDurable(excluded map[string]bool) error {
	pages, err := rt.Store.DiscoverPages()
	if err != nil {
		return err
	}
	present := map[string]bool{}
	for _, p := range pages {
		present[p] = true
	}
	sidecars, err := rt.Store.DiscoverSidecarPages()
	if err != nil {
		return err
	}
	for _, p := range sidecars {
		if !present[p] {
			return fmt.Errorf("%w: deleted or missing page %s still has a claims sidecar", ErrPersistence, p)
		}
	}
	for _, page := range sortedKeys(rt.Session.EvidenceResourcesByPage()) {
		if excluded[page] {
			continue
		}
		claims, err := rt.Session.Inspect(page)
		if err != nil {
			return err
		}
		if len(claims) == 0 {
			continue
		}
		if !present[page] {
			return fmt.Errorf("%w: claimed page %s is missing", ErrPersistence, page)
		}
		if err := rt.AssertPageDurable(page); err != nil {
			return err
		}
	}
	return nil
}

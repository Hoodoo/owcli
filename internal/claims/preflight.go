package claims

import (
	"errors"
	"sort"

	"github.com/Hoodoo/owcli/internal/evidence"
)

// Preflight is the result of checking every persisted Claim against current
// source.
type Preflight struct {
	Issues    []Issue
	Persisted map[string]*PageClaims
	Orphans   []string // sidecars whose pages no longer exist
}

// RunPreflight re-resolves all persisted evidence. A Claim is unresolved when
// any resource no longer resolves (including containment violations and
// resources that became invalid, e.g. newly ignored) and stale when all
// resolve but at least one version changed. It creates no work by itself.
func RunPreflight(st *Store, r evidence.Resolver) (Preflight, error) {
	pages, err := st.DiscoverPages()
	if err != nil {
		return Preflight{}, err
	}
	persisted, err := st.LoadAll(pages)
	if err != nil {
		return Preflight{}, err
	}
	present := map[string]bool{}
	for _, p := range pages {
		present[p] = true
	}
	sidecars, err := st.DiscoverSidecarPages()
	if err != nil {
		return Preflight{}, err
	}
	out := Preflight{Persisted: persisted}
	for _, p := range sidecars {
		if !present[p] {
			out.Orphans = append(out.Orphans, p)
		}
	}

	r = evidence.Cached(r)
	for _, page := range pages {
		pc := persisted[page]
		if pc == nil {
			continue
		}
		for _, c := range pc.Claims {
			var changed, unresolved []string
			for _, e := range c.Evidence {
				cur, err := r.Resolve(e.Resource, e.Version)
				if errors.Is(err, evidence.ErrSecurity) || errors.Is(err, evidence.ErrInvalidResource) {
					cur, err = nil, nil
				}
				if err != nil {
					return Preflight{}, err
				}
				switch {
				case cur == nil:
					unresolved = append(unresolved, e.Resource)
				case cur.Evidence.Version != e.Version:
					changed = append(changed, e.Resource)
				}
			}
			switch {
			case len(unresolved) > 0:
				sort.Strings(unresolved)
				out.Issues = append(out.Issues, Issue{Page: page, Kind: Unresolved, ClaimID: c.ID, Resources: unresolved})
			case len(changed) > 0:
				sort.Strings(changed)
				out.Issues = append(out.Issues, Issue{Page: page, Kind: Stale, ClaimID: c.ID, Resources: changed})
			}
		}
	}
	sort.SliceStable(out.Issues, func(i, j int) bool {
		a, b := out.Issues[i], out.Issues[j]
		if a.Page != b.Page {
			return a.Page < b.Page
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.ClaimID < b.ClaimID
	})
	return out, nil
}

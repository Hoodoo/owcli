package claims

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/Hoodoo/owcli/internal/evidence"
)

// ProposedClaim is a revised existing Claim (with ID) or a new one (without).
type ProposedClaim struct {
	ID        string   `json:"id,omitempty"`
	Statement string   `json:"statement"`
	Evidence  []string `json:"evidence"`
}

// Proposal is a page worker's sparse Claim decisions at page completion.
// Existing issue-free Claims it does not mention are kept as they are.
type Proposal struct {
	ConfirmedClaimIDs []string        `json:"confirmedClaimIds,omitempty"`
	Claims            []ProposedClaim `json:"claims,omitempty"`
	RetractedClaimIDs []string        `json:"retractedClaimIds,omitempty"`
}

// Reconcile turns sparse decisions into a complete operation batch and applies
// it to the page:
//
//   - confirmed ids are rechecked and kept;
//   - a proposed Claim with an id updates that Claim, or confirms it when
//     nothing changed; one without an id confirms an identical existing Claim
//     or is added;
//   - retracted ids are removed; retracting an id no page owns is tolerated
//     so retries stay safe;
//   - omitted issue-free Claims are confirmed; an omitted Claim with a
//     grounding issue rejects the proposal;
//   - a page may not end with zero Claims.
func Reconcile(s *Session, pageIn string, p Proposal) error {
	page, err := NormalizePage(pageIn)
	if err != nil {
		return err
	}
	existing, err := s.Inspect(page)
	if err != nil {
		return err
	}
	byID := map[string]InspectedClaim{}
	for _, c := range existing {
		byID[c.ID] = c
	}
	targeted := map[string]bool{}
	target := func(raw, decision string) (InspectedClaim, error) {
		id := strings.TrimSpace(raw)
		cur, ok := byID[id]
		if !ok {
			if id == "" {
				id = "(empty)"
			}
			return InspectedClaim{}, invalidf("claim %s is not owned by %s", id, page)
		}
		if targeted[id] {
			return InspectedClaim{}, invalidf("claim %s has more than one reconciliation decision for %s (%s)", id, page, decision)
		}
		targeted[id] = true
		return cur, nil
	}

	var ops []Operation
	for _, id := range p.ConfirmedClaimIDs {
		cur, err := target(id, "confirm")
		if err != nil {
			return err
		}
		ops = append(ops, Operation{Op: OpConfirm, ID: cur.ID})
	}

	fingerprints := map[string]bool{}
	for _, raw := range p.Claims {
		pc, err := normalizeProposal(raw)
		if err != nil {
			return err
		}
		fp := fingerprint(pc.Statement, pc.Evidence)
		if fingerprints[fp] {
			return invalidf("duplicate proposed claim for %s: %s", page, pc.Statement)
		}
		fingerprints[fp] = true
		if pc.ID != "" {
			cur, err := target(pc.ID, "update")
			if err != nil {
				return err
			}
			if sameClaim(cur, pc) {
				ops = append(ops, Operation{Op: OpConfirm, ID: cur.ID})
				continue
			}
			op := Operation{Op: OpUpdate, ID: cur.ID}
			if cur.Statement != pc.Statement {
				stmt := pc.Statement
				op.Statement = &stmt
			}
			if !sameSet(cur.Evidence, pc.Evidence) {
				op.Evidence = pc.Evidence
			}
			ops = append(ops, op)
			continue
		}
		matched := false
		for _, cand := range existing {
			if sameClaim(cand, pc) {
				cur, err := target(cand.ID, "confirm")
				if err != nil {
					return err
				}
				ops = append(ops, Operation{Op: OpConfirm, ID: cur.ID})
				matched = true
				break
			}
		}
		if !matched {
			stmt := pc.Statement
			ops = append(ops, Operation{Op: OpAdd, Statement: &stmt, Evidence: pc.Evidence})
		}
	}

	for _, raw := range p.RetractedClaimIDs {
		id := strings.TrimSpace(raw)
		if _, owned := byID[id]; !owned && id != "" && !s.HasClaim(id) {
			continue // already gone: a retried submission
		}
		cur, err := target(id, "retract")
		if err != nil {
			return err
		}
		ops = append(ops, Operation{Op: OpRetract, ID: cur.ID})
	}

	for _, c := range existing {
		if targeted[c.ID] {
			continue
		}
		if c.Issue != nil {
			return invalidf("claim %s is %s and requires an explicit confirm, update, or retract decision", c.ID, c.Issue.Kind)
		}
		ops = append(ops, Operation{Op: OpConfirm, ID: c.ID})
	}

	remaining := len(existing)
	for _, op := range ops {
		switch op.Op {
		case OpRetract:
			remaining--
		case OpAdd:
			remaining++
		}
	}
	if remaining == 0 {
		return invalidf("completed factual page %s must retain or establish at least one material claim", page)
	}
	_, err = s.Mutate(page, ops)
	return err
}

func normalizeProposal(c ProposedClaim) (ProposedClaim, error) {
	stmt := strings.TrimSpace(c.Statement)
	if stmt == "" {
		return ProposedClaim{}, invalidf("claim statement is empty")
	}
	set := map[string]bool{}
	for _, r := range c.Evidence {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if canon, err := evidence.Canonical(r); err == nil {
			r = canon
		}
		set[r] = true
	}
	if len(set) == 0 {
		return ProposedClaim{}, invalidf("claim requires repository evidence: %s", stmt)
	}
	return ProposedClaim{ID: strings.TrimSpace(c.ID), Statement: stmt, Evidence: sortedKeys(set)}, nil
}

func sameClaim(cur InspectedClaim, p ProposedClaim) bool {
	return cur.Statement == p.Statement && sameSet(cur.Evidence, p.Evidence)
}

func sameSet(a, b []string) bool {
	norm := func(v []string) []string {
		set := map[string]bool{}
		for _, s := range v {
			set[s] = true
		}
		return sortedKeys(set)
	}
	x, y := norm(a), norm(b)
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

func fingerprint(statement string, resources []string) string {
	r := append([]string(nil), resources...)
	sort.Strings(r)
	b, _ := json.Marshal([]any{statement, r})
	return string(b)
}

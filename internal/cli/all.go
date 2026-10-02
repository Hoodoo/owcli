package cli

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"owcli/internal/claims"
	"owcli/internal/store"
)

// wikiHealth summarizes one known wiki for status --all and check --all.
type wikiHealth struct {
	ID         string            `json:"id"`
	RepoRoot   string            `json:"repoRoot"`
	WikiDir    string            `json:"wikiDir,omitempty"`
	Problem    string            `json:"problem,omitempty"` // the wiki could not be inspected
	LastUpdate *store.LastUpdate `json:"lastUpdate,omitempty"`
	Head       string            `json:"head,omitempty"`
	HeadMoved  bool              `json:"headMoved"` // HEAD differs from the last documented commit
	Pending    string            `json:"pending,omitempty"`
	Pages      int               `json:"pages"`
	Claims     int               `json:"claims"`
	Stale      int               `json:"stale"`
	Unresolved int               `json:"unresolved"`
	Orphans    int               `json:"orphanedSidecars"`
	OKF        int               `json:"okfIssues"`
	Links      int               `json:"brokenLinks"`
	Mermaid    int               `json:"mermaidIssues"`
	Edited     int               `json:"editedOutsideRun"`
	Problems   int               `json:"problems"` // what `owcli check` counts, plus 1 for Problem

	state *wikiState
}

// allHealth inspects every wiki the registries know. A wiki that cannot be
// inspected is reported, not fatal.
func allHealth() ([]wikiHealth, error) {
	dirs, err := store.DefaultDirs()
	if err != nil {
		return nil, err
	}
	known, err := dirs.KnownWikis()
	if err != nil {
		return nil, err
	}
	out := make([]wikiHealth, 0, len(known))
	for _, k := range known {
		out = append(out, healthOf(k))
	}
	return out, nil
}

// healthOf inspects one known wiki.
func healthOf(k store.KnownWiki) wikiHealth {
	h := wikiHealth{ID: k.Wiki.ID, RepoRoot: k.Root, Problem: k.Problem}
	if h.Problem == "" {
		h.WikiDir = k.Wiki.Layout.WikiRoot
		s, err := inspectWiki(k.Wiki.Layout)
		if err != nil {
			h.Problem = err.Error()
		} else {
			h.fill(s)
		}
	}
	if h.Problem != "" {
		h.Problems++
	}
	return h
}

func (h *wikiHealth) fill(s *wikiState) {
	h.state = s
	h.LastUpdate, h.Head = s.LastUpdate, s.Head
	h.HeadMoved = s.LastUpdate != nil && s.Head != "" && s.LastUpdate.GitHead != s.Head
	h.Pending = strings.TrimSpace(s.RunPhase + " " + s.RunProgress)
	h.Pages, h.Claims, h.Edited = len(s.Pages), s.Claims, len(s.Untracked)
	for _, is := range s.Issues {
		if is.Kind == claims.Stale {
			h.Stale++
		} else {
			h.Unresolved++
		}
	}
	h.Orphans, h.Links, h.Mermaid = len(s.Orphans), len(s.Links), len(s.Mermaid)
	for _, issues := range s.OKFIssues {
		h.OKF += len(issues)
	}
	h.Problems = h.Stale + h.Unresolved + h.Orphans + h.OKF + h.Links + h.Mermaid
}

// printStatusAll prints one block per wiki.
func printStatusAll(out io.Writer, all []wikiHealth) {
	if len(all) == 0 {
		fmt.Fprintln(out, "no known wikis; bind repositories or add them to a workspace")
		return
	}
	for _, h := range all {
		fmt.Fprintf(out, "%s  %s\n", h.ID, h.RepoRoot)
		if h.Problem != "" {
			fmt.Fprintf(out, "  unavailable: %s\n", h.Problem)
			continue
		}
		last := "none"
		if lu := h.LastUpdate; lu != nil {
			last = fmt.Sprintf("%s %s at %s (commit %s)", lu.Command, lu.Status, lu.UpdatedAt, short(lu.GitHead))
			if h.HeadMoved {
				last += fmt.Sprintf("; HEAD moved to %s", short(h.Head))
			}
		}
		fmt.Fprintf(out, "  last run:  %s\n", last)
		if h.Pending != "" {
			fmt.Fprintf(out, "  pending:   %s\n", h.Pending)
		}
		fmt.Fprintf(out, "  pages:     %d, %d Claim(s); %s\n", h.Pages, h.Claims, issueLine(h.state.Issues))
		if h.Edited > 0 {
			fmt.Fprintf(out, "  edited:    %d page(s) changed outside a run\n", h.Edited)
		}
	}
}

// printCheckAll prints each wiki's check result and returns how many wikis
// have problems.
func printCheckAll(out io.Writer, all []wikiHealth) int {
	failing := 0
	for _, h := range all {
		if h.Problems == 0 {
			fmt.Fprintf(out, "ok    %s  %d page(s), %d Claim(s)\n", h.ID, h.Pages, h.Claims)
			continue
		}
		failing++
		fmt.Fprintf(out, "FAIL  %s  %s\n", h.ID, h.RepoRoot)
		if h.Problem != "" {
			fmt.Fprintf(out, "  unavailable: %s\n", h.Problem)
			continue
		}
		var buf bytes.Buffer
		printCheck(&buf, h.state)
		for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
			fmt.Fprintf(out, "  %s\n", line)
		}
	}
	fmt.Fprintf(out, "%d wiki(s) checked, %d with problems\n", len(all), failing)
	return failing
}

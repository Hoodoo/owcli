package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"owcli/internal/claims"
	"owcli/internal/evidence"
	"owcli/internal/ignore"
	"owcli/internal/okf"
	"owcli/internal/store"
)

// wikiState is a read-only snapshot of a wiki's health.
type wikiState struct {
	Layout      store.Layout
	LastUpdate  *store.LastUpdate
	Head        string
	Pages       []string
	Claims      int
	Issues      []claims.Issue
	Orphans     []string
	Untracked   []string // pages without a manifest entry or whose bytes changed since
	OKFIssues   map[string][]okf.Issue
	Links       []okf.LinkIssue
	Mermaid     map[string]string // page -> first heuristic problem
	RunPhase    string
	RunProgress string
}

func inspectWiki(l store.Layout) (*wikiState, error) {
	s := &wikiState{Layout: l, OKFIssues: map[string][]okf.Issue{}, Mermaid: map[string]string{}}
	var err error
	if s.LastUpdate, err = l.LoadLastUpdate(); err != nil {
		return nil, err
	}
	if out, err := gitOutput(l.RepoRoot, "rev-parse", "--verify", "--quiet", "HEAD"); err == nil {
		s.Head = strings.TrimSpace(out)
	}
	m, err := ignore.Load(l.RepoRoot)
	if err != nil {
		return nil, err
	}
	resolver, err := evidence.NewRepoResolver(l.RepoRoot, m.Exclude(l.RepoExclusions()...))
	if err != nil {
		return nil, err
	}
	st := claims.NewStore(l)
	pf, err := claims.RunPreflight(st, resolver)
	if err != nil {
		return nil, err
	}
	s.Issues, s.Orphans = pf.Issues, pf.Orphans
	for _, pc := range pf.Persisted {
		s.Claims += len(pc.Claims)
	}
	if s.Pages, err = st.DiscoverPages(); err != nil {
		return nil, err
	}
	manifest, err := l.LoadManifest()
	if err != nil {
		return nil, err
	}
	w := okf.ForLayout(l)
	for _, p := range s.Pages {
		content, err := st.ReadMarkdown(p)
		if err != nil {
			return nil, err
		}
		if e, ok := manifest.Pages[p]; !ok || e.PageVersion != claims.HashContent(content) {
			s.Untracked = append(s.Untracked, p)
		}
		if issues := okf.Validate(content); issues != nil {
			s.OKFIssues[p] = issues
		}
		for _, f := range okf.ExtractMermaid(content) {
			if msg := okf.MermaidHeuristic(f.Body); msg != "" {
				s.Mermaid[p] = msg
				break
			}
		}
	}
	links, err := w.CheckLinks()
	if err != nil {
		return nil, err
	}
	s.Links = links.Issues
	var rs struct {
		Phase string `json:"phase"`
		Mode  string `json:"mode"`
		Plan  *struct {
			Jobs []struct {
				Status string `json:"status"`
			} `json:"jobs"`
		} `json:"plan"`
	}
	if found, err := store.ReadJSON(l.RunPath(), &rs); err != nil {
		s.RunPhase = "unreadable checkpoint"
	} else if found {
		s.RunPhase = rs.Mode + " " + rs.Phase
		if rs.Plan != nil {
			done := 0
			for _, j := range rs.Plan.Jobs {
				if j.Status == "complete" {
					done++
				}
			}
			s.RunProgress = fmt.Sprintf("%d/%d pages complete", done, len(rs.Plan.Jobs))
		}
	}
	return s, nil
}

func newStatusCommand() *cobra.Command {
	var (
		wiki   string
		all    bool
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show binding, last update, pending run, and claim health",
		Long: `Show the current repository's wiki: binding, last run, pending run, and
Claim health. --wiki targets a registered wiki from anywhere; --all lists
every bound repository and workspace member (--json for scripts).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if all {
				if wiki != "" {
					return fmt.Errorf("give --all or --wiki, not both")
				}
				health, err := allHealth()
				if err != nil {
					return err
				}
				if asJSON {
					return writeJSON(cmd.OutOrStdout(), map[string]any{"wikis": health})
				}
				printStatusAll(cmd.OutOrStdout(), health)
				return nil
			}
			if asJSON {
				return fmt.Errorf("--json needs --all")
			}
			l, err := resolveLayoutRef(wiki)
			if err != nil {
				return err
			}
			s, err := inspectWiki(l)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Repository: %s\nWiki:       %s (%s)\n", l.RepoRoot, l.WikiRoot, l.Kind)
			if lu := s.LastUpdate; lu != nil {
				fmt.Fprintf(out, "Last run:   %s %s at %s (commit %s, model %s)\n", lu.Command, lu.Status, lu.UpdatedAt, short(lu.GitHead), lu.Model)
				if s.Head != "" && lu.GitHead != s.Head {
					fmt.Fprintf(out, "            HEAD has moved to %s since\n", short(s.Head))
				}
			} else {
				fmt.Fprintln(out, "Last run:   none")
			}
			if s.RunPhase != "" {
				fmt.Fprintf(out, "Pending:    %s %s (resume with the same command)\n", s.RunPhase, s.RunProgress)
			}
			fmt.Fprintf(out, "Pages:      %d, %d Claim(s)\n", len(s.Pages), s.Claims)
			fmt.Fprintf(out, "Grounding:  %s\n", issueLine(s.Issues))
			if n := len(s.Untracked); n > 0 {
				fmt.Fprintf(out, "Edited:     %d page(s) changed outside a run\n", n)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&wiki, "wiki", "", "target a registered wiki by ID or repository name instead of the current repository")
	cmd.Flags().BoolVar(&all, "all", false, "every bound repository and workspace member")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON (with --all)")
	return cmd
}

func issueLine(issues []claims.Issue) string {
	if len(issues) == 0 {
		return "all Claims current"
	}
	stale, unresolved := 0, 0
	for _, is := range issues {
		if is.Kind == claims.Stale {
			stale++
		} else {
			unresolved++
		}
	}
	return fmt.Sprintf("%d stale, %d unresolved Claim(s); run `owcli update`", stale, unresolved)
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	if h == "" {
		return "none"
	}
	return h
}

// errCheckFailed makes `owcli check` exit non-zero without extra output.
var errCheckFailed = errors.New("check found problems")

func newCheckCommand() *cobra.Command {
	var (
		wiki   string
		all    bool
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Validate the wiki without a model: Claims, OKF front matter, links, diagrams",
		Long: `Run every deterministic check without changing anything: Claims preflight
(stale or unresolved evidence), orphaned sidecars, OKF front matter validity,
broken internal links, and suspicious Mermaid diagrams. Exits non-zero when
something needs attention.

--wiki checks a registered wiki from anywhere. --all checks every bound
repository and workspace member and exits non-zero if any has problems,
including a repository or wiki that is missing (--json for scripts).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if all {
				if wiki != "" {
					return fmt.Errorf("give --all or --wiki, not both")
				}
				health, err := allHealth()
				if err != nil {
					return err
				}
				failing := 0
				for _, h := range health {
					if h.Problems > 0 {
						failing++
					}
				}
				if asJSON {
					if err := writeJSON(cmd.OutOrStdout(), map[string]any{"wikis": health, "failing": failing}); err != nil {
						return err
					}
				} else {
					printCheckAll(cmd.OutOrStdout(), health)
				}
				if failing > 0 {
					cmd.SilenceErrors = true
					return errCheckFailed
				}
				return nil
			}
			if asJSON {
				return fmt.Errorf("--json needs --all")
			}
			l, err := resolveLayoutRef(wiki)
			if err != nil {
				return err
			}
			s, err := inspectWiki(l)
			if err != nil {
				return err
			}
			problems := printCheck(cmd.OutOrStdout(), s)
			if problems > 0 {
				cmd.SilenceErrors = true
				return errCheckFailed
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&wiki, "wiki", "", "target a registered wiki by ID or repository name instead of the current repository")
	cmd.Flags().BoolVar(&all, "all", false, "every bound repository and workspace member")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON (with --all)")
	return cmd
}

func printCheck(out io.Writer, s *wikiState) int {
	problems := 0
	section := func(title string, lines []string) {
		if len(lines) == 0 {
			return
		}
		problems += len(lines)
		fmt.Fprintf(out, "%s (%d):\n", title, len(lines))
		for _, l := range lines {
			fmt.Fprintf(out, "  %s\n", l)
		}
	}
	var lines []string
	for _, is := range s.Issues {
		lines = append(lines, fmt.Sprintf("%s %s %s: %s", rel(is.Page), is.ClaimID, is.Kind, strings.Join(is.Resources, ", ")))
	}
	section("Claims needing recheck", lines)
	lines = nil
	for _, p := range s.Orphans {
		lines = append(lines, rel(p)+": sidecar without a page")
	}
	section("Orphaned sidecars", lines)
	lines = nil
	for _, p := range s.Pages {
		for _, is := range s.OKFIssues[p] {
			lines = append(lines, rel(p)+": "+is.String())
		}
	}
	section("OKF front matter", lines)
	lines = nil
	for _, is := range s.Links {
		lines = append(lines, fmt.Sprintf("%s:%d [%s] %s", rel(is.Page), is.Line, is.Href, is.Message))
	}
	section("Broken links", lines)
	lines = nil
	for _, p := range s.Pages {
		if msg, ok := s.Mermaid[p]; ok {
			lines = append(lines, rel(p)+": "+msg)
		}
	}
	section("Mermaid diagrams", lines)
	if problems == 0 {
		fmt.Fprintf(out, "OK: %d page(s), %d Claim(s), no problems found.\n", len(s.Pages), s.Claims)
	}
	return problems
}

func rel(page string) string { return strings.TrimPrefix(page, "/") }

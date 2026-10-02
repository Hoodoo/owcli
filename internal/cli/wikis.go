package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"owcli/internal/store"
)

// The `owcli wikis` JSON is a contract for clients such as editor
// integrations or a viewer: one call returns every known wiki and every
// workspace, with membership on both sides. Add fields; do not rename them.

type wikiMembership struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Active bool   `json:"active"` // the wiki's active workspace
}

type wikiEntry struct {
	ID         string            `json:"id"` // pass to --wiki
	Name       string            `json:"name"`
	RepoRoot   string            `json:"repoRoot"`
	WikiDir    string            `json:"wikiDir,omitempty"`
	Layout     store.Kind        `json:"layout,omitempty"` // in-repo or external
	Bound      bool              `json:"bound"`            // false: only a workspace member
	Workspaces []wikiMembership  `json:"workspaces"`
	LastUpdate *store.LastUpdate `json:"lastUpdate,omitempty"`
	Problem    string            `json:"problem,omitempty"` // why the wiki cannot be read
	Health     *wikiHealth       `json:"health,omitempty"`  // with --health
}

type workspaceEntry struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Wikis []string `json:"wikis"` // wiki IDs
}

type wikisListing struct {
	Wikis      []wikiEntry      `json:"wikis"`
	Workspaces []workspaceEntry `json:"workspaces"`
}

func listWikis(health bool) (wikisListing, error) {
	dirs, err := store.DefaultDirs()
	if err != nil {
		return wikisListing{}, err
	}
	known, err := dirs.KnownWikis()
	if err != nil {
		return wikisListing{}, err
	}
	reg, err := dirs.LoadWorkspaces()
	if err != nil {
		return wikisListing{}, err
	}
	out := wikisListing{Wikis: []wikiEntry{}, Workspaces: []workspaceEntry{}}
	for _, ws := range reg.Workspaces {
		out.Workspaces = append(out.Workspaces, workspaceEntry{ID: ws.ID, Name: ws.Name, Wikis: append([]string{}, ws.Wikis...)})
	}
	for _, k := range known {
		e := wikiEntry{ID: k.Wiki.ID, Name: k.Wiki.Name, RepoRoot: k.Root, Bound: k.Bound, Problem: k.Problem, Workspaces: []wikiMembership{}}
		if k.Problem == "" {
			l := k.Wiki.Layout
			e.WikiDir, e.Layout = l.WikiRoot, l.Kind
			if e.LastUpdate, err = l.LoadLastUpdate(); err != nil {
				e.Problem = err.Error()
			}
		}
		if w, ok := reg.WikiByRoot(k.Root); ok {
			active, _ := reg.ActiveFor(w.ID)
			for _, ws := range reg.Workspaces {
				if contains(ws.Wikis, w.ID) {
					e.Workspaces = append(e.Workspaces, wikiMembership{ID: ws.ID, Name: ws.Name, Active: ws.ID == active})
				}
			}
		}
		if health {
			h := healthOf(k)
			e.Health = &h
		}
		out.Wikis = append(out.Wikis, e)
	}
	return out, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func newWikisCommand() *cobra.Command {
	var asJSON, health bool
	cmd := &cobra.Command{
		Use:   "wikis",
		Short: "List every wiki and workspace owcli knows, from anywhere",
		Long: `List every bound repository and workspace member with its wiki ID (for
--wiki), location, layout, workspaces (* marks the active one), and last run,
followed by every workspace and its members. owcli does not scan the disk: a
wiki appears here once it is bound or added to a workspace.

--health adds the counts owcli check --all reports (slower: it runs the
Claims preflight on every wiki). --json returns the whole picture in one
object for scripts and editor integrations.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			listing, err := listWikis(health)
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), listing)
			}
			printWikis(cmd.OutOrStdout(), listing)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	cmd.Flags().BoolVar(&health, "health", false, "include check results (runs the Claims preflight on every wiki)")
	return cmd
}

func printWikis(out io.Writer, l wikisListing) {
	if len(l.Wikis) == 0 {
		fmt.Fprintln(out, "no known wikis; bind a repository (owcli bind) or add it to a workspace")
	}
	for _, e := range l.Wikis {
		fmt.Fprintf(out, "%s  %s\n", e.ID, e.RepoRoot)
		if e.Problem != "" {
			fmt.Fprintf(out, "  unavailable: %s\n", e.Problem)
		} else {
			how := string(e.Layout)
			if !e.Bound {
				how += ", workspace member only"
			}
			fmt.Fprintf(out, "  wiki:       %s (%s)\n", e.WikiDir, how)
		}
		if len(e.Workspaces) > 0 {
			var names []string
			for _, w := range e.Workspaces {
				mark := ""
				if w.Active {
					mark = "*"
				}
				names = append(names, w.ID+mark)
			}
			fmt.Fprintf(out, "  workspaces: %s\n", strings.Join(names, ", "))
		}
		if lu := e.LastUpdate; lu != nil {
			fmt.Fprintf(out, "  last run:   %s %s at %s (commit %s)\n", lu.Command, lu.Status, lu.UpdatedAt, short(lu.GitHead))
		} else if e.Problem == "" {
			fmt.Fprintln(out, "  last run:   none")
		}
		if h := e.Health; h != nil {
			if h.Problems == 0 {
				fmt.Fprintf(out, "  health:     ok, %d page(s), %d Claim(s)\n", h.Pages, h.Claims)
			} else {
				fmt.Fprintf(out, "  health:     %d problem(s); run `owcli check --wiki %s`\n", h.Problems, e.ID)
			}
		}
	}
	if len(l.Workspaces) > 0 {
		fmt.Fprintln(out, "\nworkspaces:")
		for _, w := range l.Workspaces {
			fmt.Fprintf(out, "  %s  %s: %s\n", w.ID, w.Name, strings.Join(w.Wikis, ", "))
		}
	}
}

// Package cli defines the owcli command tree.
package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"owcli/internal/claims"
	"owcli/internal/config"
	"owcli/internal/run"
	"owcli/internal/search"
	"owcli/internal/store"
	"owcli/internal/version"
)

// options holds flags shared by every command.
type options struct {
	configPath string
	model      config.Config
}

// NewRootCommand builds the owcli command tree.
func NewRootCommand() *cobra.Command {
	opts := &options{}
	root := &cobra.Command{
		Use:           "owcli",
		Short:         "Generate, ground, and search a repository wiki",
		Version:       version.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	pf := root.PersistentFlags()
	var chdir string
	pf.StringVarP(&chdir, "chdir", "C", "", "run as if owcli was started in this directory, like git -C (relative paths in other arguments resolve from it)")
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if chdir == "" {
			return nil
		}
		if err := os.Chdir(chdir); err != nil {
			err = fmt.Errorf("-C %s: %w", chdir, err)
			if p := cmd.Parent(); p != nil && p.Name() == "run" {
				// run steps promise JSON errors.
				_ = writeJSON(cmd.OutOrStdout(), map[string]any{"error": map[string]string{"code": "invalid_input", "message": err.Error()}})
				return ErrReported
			}
			return err
		}
		return nil
	}
	pf.StringVar(&opts.configPath, "config", "", "config file (default $XDG_CONFIG_HOME/owcli/config.toml)")
	pf.StringVar(&opts.model.Provider, "provider", "", "model provider: anthropic or openai")
	pf.StringVar(&opts.model.Model, "model", "", "model id")
	pf.StringVar(&opts.model.BaseURL, "base-url", "", "provider API base URL")
	pf.StringVar(&opts.model.APIKeyEnv, "api-key-env", "", "environment variable holding the API key")
	pf.StringVar(&opts.model.Effort, "effort", "", "Anthropic effort: low, medium, high, xhigh, max")

	root.AddCommand(
		newBindCommand(),
		newBindingsCommand(),
		newUnbindCommand(),
		newGenerateCommand(opts, run.Init),
		newGenerateCommand(opts, run.Update),
		newStatusCommand(),
		newCheckCommand(),
		newSearchCommand(),
		newReadCommand(),
		newWorkspaceCommand(),
		newWikisCommand(),
		newServeCommand(),
		newRunCommand(),
		newQuickstartCommand(),
		newAgentsMDCommand(),
	)
	return root
}

func newBindCommand() *cobra.Command {
	var (
		external bool
		wikiDir  string
	)
	cmd := &cobra.Command{
		Use:   "bind [path]",
		Short: "Register a repository with an in-repo or external wiki",
		Long: `Register the Git repository containing path (default: current directory).

By default the wiki lives in <repo>/openwiki. With --external it lives under
$XDG_DATA_HOME/owcli/wikis/ (or in --wiki-dir) and owcli writes nothing into
the repository, which suits exploring projects you do not own. External
wikis are versioned with Git: owcli commits after every finished run, in the
wiki's own repository or, when --wiki-dir is inside an existing repository
such as a shared knowledge base, in that repository (only the wiki's files).

The registry is $XDG_CONFIG_HOME/owcli/bindings.json. To reattach a wiki after
moving a clone, pass the existing directory that contains openwiki/ with
--wiki-dir. If its previous repository no longer exists, the binding is moved
to the new canonical repository path. Use "owcli bindings" to find paths.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dirs, err := store.DefaultDirs()
			if err != nil {
				return err
			}
			kind := store.InRepo
			if external || wikiDir != "" {
				kind = store.External
			}
			l, err := dirs.Bind(pathArg(args), kind, wikiDir, time.Now())
			if err != nil {
				return err
			}
			if err := l.EnsureWikiRepo(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "bound %s (%s)\nwiki: %s\n", l.RepoRoot, l.Kind, l.WikiRoot)
			return nil
		},
	}
	cmd.Flags().BoolVar(&external, "external", false, "store the wiki outside the repository; write nothing into it")
	cmd.Flags().StringVar(&wikiDir, "wiki-dir", "", "external wiki location (implies --external); e.g. a directory in a knowledge-base repository")
	return cmd
}

func newBindingsCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "bindings",
		Short: "List repository bindings and orphaned managed wikis",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dirs, err := store.DefaultDirs()
			if err != nil {
				return err
			}
			inv, err := dirs.ListBindings()
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), inv)
			}
			out := cmd.OutOrStdout()
			if len(inv.Bindings) == 0 {
				fmt.Fprintln(out, "no bindings")
			}
			for _, b := range inv.Bindings {
				state := "ok"
				if !b.RepoExists {
					state = "missing repository"
				}
				last := "none"
				if b.LastUpdate != nil {
					last = fmt.Sprintf("%s at %s (source %s)", b.LastUpdate.Status, b.LastUpdate.UpdatedAt, short(b.LastUpdate.GitHead))
				}
				fmt.Fprintf(out, "%s\n  id: %s\n  kind: %s\n  wiki: %s\n  state: %s\n  last run: %s\n", b.RepoRoot, b.ID, b.Kind, b.WikiDir, state, last)
			}
			for _, orphan := range inv.Orphans {
				fmt.Fprintf(out, "orphan: %s\n", orphan)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func newUnbindCommand() *cobra.Command {
	var purge bool
	cmd := &cobra.Command{
		Use:   "unbind [path]",
		Short: "Forget a repository binding",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dirs, err := store.DefaultDirs()
			if err != nil {
				return err
			}
			l, err := dirs.Unbind(pathArg(args), purge)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "unbound %s\n", l.RepoRoot)
			if purge {
				fmt.Fprintf(cmd.OutOrStdout(), "deleted %s\n", filepath.Dir(l.WikiRoot))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "also delete an external wiki")
	return cmd
}

func pathArg(args []string) string {
	if len(args) == 0 {
		return "."
	}
	return args[0]
}

func newSearchCommand() *cobra.Command {
	var (
		paths     []string
		limit     int
		asJSON    bool
		workspace string
		wiki      string
	)
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search wiki sections",
		Long: `Rank wiki sections for a question, behavior, or concept.

Search is lexical (SQLite FTS5 BM25 with stemming), matching upstream
OpenWiki. Results are "page#anchor" refs; pass them to "owcli read".

A repository in a workspace searches every wiki of that workspace in one
ranking, and each result names its wiki: pass it to "owcli read --wiki". In
several workspaces it uses the active one (owcli workspace use); without one,
search exits non-zero with status "workspace_required" and the choices.

Outside any repository (or in one with no wiki and no workspace), name the
target: --workspace W searches a workspace, --wiki ID a single registered
wiki. Wiki IDs are shown by "owcli bindings" and "owcli workspace list"; a
repository name works when it is unambiguous.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dirs, err := store.DefaultDirs()
			if err != nil {
				return err
			}
			var scope store.SearchScope
			if wiki != "" {
				if workspace != "" {
					return fmt.Errorf("give --wiki or --workspace, not both")
				}
				w, err := dirs.ResolveReadableWiki(".", wiki)
				if err != nil {
					return err
				}
				scope = store.SearchScope{Status: store.ScopeReady, Wikis: []store.ScopedWiki{w}}
			} else if scope, err = dirs.ResolveSearchScope(".", workspace); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if scope.Status == store.ScopeWorkspaceRequired {
				if asJSON {
					if err := writeJSON(out, map[string]any{"status": scope.Status, "wiki": scope.Current, "workspaces": scope.Choices}); err != nil {
						return err
					}
				} else {
					fmt.Fprintf(out, "%s is in several workspaces and none is active; choose one with --workspace <id>, or set it with `owcli workspace use <id>`:\n", scope.Current.Name)
					for _, w := range scope.Choices {
						fmt.Fprintf(out, "  %s  %s (%d wikis)\n", w.ID, w.Name, w.WikiCount)
					}
				}
				return ErrReported
			}
			var sources []search.Source
			var wikis []store.WikiIdentity
			for _, w := range scope.Wikis {
				src := search.Source{Store: claims.NewStore(w.Layout)}
				if wiki != "" {
					src.Wiki = w.ID
				}
				if scope.Workspace != nil {
					src.Wiki = w.ID
					wikis = append(wikis, w.WikiIdentity)
				}
				sources = append(sources, src)
			}
			results, err := search.SearchWikis(sources, search.Request{Query: strings.Join(args, " "), Paths: paths, Limit: limit}, search.Options{})
			if err != nil {
				return err
			}
			if asJSON {
				resp := map[string]any{"results": results}
				if scope.Workspace != nil {
					resp["workspace"], resp["wikis"] = scope.Workspace, wikis
					if len(scope.Skipped) > 0 {
						resp["skipped"] = scope.Skipped
					}
				}
				return writeJSON(out, resp)
			}
			if scope.Workspace != nil {
				fmt.Fprintf(out, "workspace %s: %d wiki(s) searched\n", scope.Workspace.Name, len(scope.Wikis))
				for _, m := range scope.Skipped {
					fmt.Fprintf(out, "  skipped %s: %s\n", m.Wiki.ID, m.Problem)
				}
			}
			if len(results) == 0 {
				fmt.Fprintln(out, "no results")
			}
			for i, r := range results {
				if r.Wiki != "" {
					fmt.Fprintf(out, "%d. [%s] %s\n", i+1, r.Wiki, r.Ref[0])
				} else {
					fmt.Fprintf(out, "%d. %s\n", i+1, r.Ref[0])
				}
				for _, line := range strings.Split(r.Content, "\n") {
					fmt.Fprintf(out, "   %s\n", line)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&paths, "path", nil, "repository-relative source path hint (repeatable)")
	cmd.Flags().IntVar(&limit, "limit", search.DefaultResults, fmt.Sprintf("maximum results (1-%d)", search.MaxResults))
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	cmd.Flags().StringVar(&workspace, "workspace", "", "search this workspace (ID or name) instead of the automatic choice")
	cmd.Flags().StringVar(&wiki, "wiki", "", "search only this wiki (ID or repository name); outside a repository any registered wiki")
	return cmd
}

func newReadCommand() *cobra.Command {
	var (
		asJSON bool
		wiki   string
	)
	cmd := &cobra.Command{
		Use:   "read <page> <anchor>...",
		Short: "Print wiki sections",
		Long: `Print complete sections of one wiki page. Accepts a search ref
("openwiki/concepts/x.md#anchor") or a page plus anchors. With --wiki, read
a wiki from a workspace this repository belongs to (the "wiki" of a search
result); outside any repository, any registered wiki.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			page, anchors := args[0], args[1:]
			if p, a, ok := strings.Cut(page, "#"); ok {
				page, anchors = p, append([]string{a}, anchors...)
			}
			if len(anchors) == 0 {
				return fmt.Errorf("give at least one section anchor")
			}
			dirs, err := store.DefaultDirs()
			if err != nil {
				return err
			}
			w, err := dirs.ResolveReadableWiki(".", wiki)
			if err != nil {
				return err
			}
			p, secs, err := search.Read(claims.NewStore(w.Layout), page, anchors)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				resp := map[string]any{"page": p, "sections": secs}
				if w.ID != "" {
					resp["wiki"] = w.ID
				}
				return writeJSON(out, resp)
			}
			for i, s := range secs {
				if i > 0 {
					fmt.Fprintln(out)
				}
				fmt.Fprintln(out, s.Content)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	cmd.Flags().StringVar(&wiki, "wiki", "", "wiki ID from a workspace search result (default: this repository's wiki)")
	return cmd
}

// resolveLayoutRef resolves --wiki from the registries, or the current
// repository when it is empty.
func resolveLayoutRef(ref string) (store.Layout, error) {
	dirs, err := store.DefaultDirs()
	if err != nil {
		return store.Layout{}, err
	}
	return dirs.ResolveLayoutRef(".", ref)
}

// resolveLayout finds the wiki of the repository containing the working
// directory.
func resolveLayout() (store.Layout, error) {
	dirs, err := store.DefaultDirs()
	if err != nil {
		return store.Layout{}, err
	}
	return dirs.Resolve(".")
}

func writeJSON(w io.Writer, v any) error {
	data, err := store.MarshalJSON(v)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// Package cli defines the owcli command tree.
package cli

import (
	"fmt"
	"io"
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
	pf.StringVar(&opts.configPath, "config", "", "config file (default $XDG_CONFIG_HOME/owcli/config.toml)")
	pf.StringVar(&opts.model.Provider, "provider", "", "model provider: anthropic or openai")
	pf.StringVar(&opts.model.Model, "model", "", "model id")
	pf.StringVar(&opts.model.BaseURL, "base-url", "", "provider API base URL")
	pf.StringVar(&opts.model.APIKeyEnv, "api-key-env", "", "environment variable holding the API key")
	pf.StringVar(&opts.model.Effort, "effort", "", "Anthropic effort: low, medium, high, xhigh, max")

	root.AddCommand(
		newBindCommand(),
		newUnbindCommand(),
		newGenerateCommand(opts, run.Init),
		newGenerateCommand(opts, run.Update),
		newStatusCommand(),
		newCheckCommand(),
		newSearchCommand(),
		newReadCommand(),
		newRunCommand(),
		newQuickstartCommand(),
		newAgentsMDCommand(),
	)
	return root
}

func newBindCommand() *cobra.Command {
	var external bool
	cmd := &cobra.Command{
		Use:   "bind [path]",
		Short: "Register a repository with an in-repo or external wiki",
		Long: `Register the Git repository containing path (default: current directory).

By default the wiki lives in <repo>/openwiki. With --external it lives under
$XDG_DATA_HOME/owcli/wikis/ and owcli writes nothing into the repository,
which suits exploring projects you do not own.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dirs, err := store.DefaultDirs()
			if err != nil {
				return err
			}
			kind := store.InRepo
			if external {
				kind = store.External
			}
			l, err := dirs.Bind(pathArg(args), kind, time.Now())
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "bound %s (%s)\nwiki: %s\n", l.RepoRoot, l.Kind, l.WikiRoot)
			return nil
		},
	}
	cmd.Flags().BoolVar(&external, "external", false, "store the wiki outside the repository; write nothing into it")
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
		paths  []string
		limit  int
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search wiki sections",
		Long: `Rank wiki sections for a question, behavior, or concept.

Search is lexical (SQLite FTS5 BM25 with stemming), matching upstream
OpenWiki. Results are "page#anchor" refs; pass them to "owcli read".`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			l, err := resolveLayout()
			if err != nil {
				return err
			}
			results, err := search.Search(claims.NewStore(l), search.Request{Query: strings.Join(args, " "), Paths: paths, Limit: limit}, search.Options{})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				return writeJSON(out, map[string]any{"results": results})
			}
			if len(results) == 0 {
				fmt.Fprintln(out, "no results")
			}
			for i, r := range results {
				fmt.Fprintf(out, "%d. %s\n", i+1, r.Ref[0])
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
	return cmd
}

func newReadCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "read <page> <anchor>...",
		Short: "Print wiki sections",
		Long: `Print complete sections of one wiki page. Accepts a search ref
("openwiki/concepts/x.md#anchor") or a page plus anchors.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			page, anchors := args[0], args[1:]
			if p, a, ok := strings.Cut(page, "#"); ok {
				page, anchors = p, append([]string{a}, anchors...)
			}
			if len(anchors) == 0 {
				return fmt.Errorf("give at least one section anchor")
			}
			l, err := resolveLayout()
			if err != nil {
				return err
			}
			p, secs, err := search.Read(claims.NewStore(l), page, anchors)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				return writeJSON(out, map[string]any{"page": p, "sections": secs})
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
	return cmd
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

// Package cli defines the owcli command tree.
package cli

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"owcli/internal/config"
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

	root.AddCommand(
		newBindCommand(),
		newUnbindCommand(),
		newGenerateCommand(opts, "init", "Generate a wiki from scratch"),
		newGenerateCommand(opts, "update", "Update the wiki for source drift and claim issues"),
		newStatusCommand(),
		newCheckCommand(),
		newSearchCommand(),
		newReadCommand(),
	)
	return root
}

// notImplemented marks a command whose implementation is tracked in Kata.
func notImplemented(issue string) error {
	return fmt.Errorf("not implemented yet (kata %s)", issue)
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

func newGenerateCommand(opts *options, name, short string) *cobra.Command {
	return &cobra.Command{
		Use:   name + " [message]",
		Short: short,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(*cobra.Command, []string) error {
			if _, err := config.Load(opts.configPath, opts.model); err != nil {
				return err
			}
			return notImplemented("e564")
		},
	}
}

func newStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show binding, last update, pending run, and claim issues",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return notImplemented("e564")
		},
	}
}

func newCheckCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Run claims preflight and OKF validation without a model",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return notImplemented("e564")
		},
	}
}

func newSearchCommand() *cobra.Command {
	var (
		paths []string
		limit int
	)
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search wiki sections",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(*cobra.Command, []string) error {
			return notImplemented("t1k2")
		},
	}
	cmd.Flags().StringArrayVar(&paths, "path", nil, "repository-relative source path hint (repeatable)")
	cmd.Flags().IntVar(&limit, "limit", 5, "maximum results (1-20)")
	return cmd
}

func newReadCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "read <page> <anchor>...",
		Short: "Print wiki sections",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(*cobra.Command, []string) error {
			return notImplemented("t1k2")
		},
	}
}

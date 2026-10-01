// Package cli defines the owcli command tree.
package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"owcli/internal/config"
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
		Args:  cobra.MaximumNArgs(1),
		RunE: func(*cobra.Command, []string) error {
			return notImplemented("4eq6")
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
		RunE: func(*cobra.Command, []string) error {
			return notImplemented("4eq6")
		},
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "also delete an external wiki")
	return cmd
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

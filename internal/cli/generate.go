package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"owcli/internal/agent"
	"owcli/internal/config"
	"owcli/internal/generate"
	"owcli/internal/llm"
	"owcli/internal/run"
	"owcli/internal/store"
	"owcli/internal/version"
)

// providerFactory builds the model provider; tests replace it.
var providerFactory = llm.New

func newGenerateCommand(opts *options, mode run.Mode) *cobra.Command {
	var (
		external, agentsMD, verbose bool
		wikiDir                     string
	)
	short := "Generate a wiki from scratch (resumes an interrupted init)"
	if mode == run.Update {
		short = "Update the wiki for source changes and stale Claims (resumes an interrupted update)"
	}
	cmd := &cobra.Command{
		Use:   string(mode) + " [message]",
		Short: short,
		Long: short + `.

The optional message tells the planner what to focus on. Interrupting a run
(Ctrl-C) keeps completed pages; run the same command again to continue.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(opts.configPath, opts.model)
			if err != nil {
				return err
			}
			l, err := layoutFor(mode, external, wikiDir)
			if err != nil {
				return err
			}
			provider, err := providerFactory(cfg)
			if err != nil {
				return err
			}
			if agentsMD {
				if l.Kind != store.InRepo {
					return errors.New("--agents-md only applies to in-repo wikis; an external binding writes nothing into the repository")
				}
				changed, err := ensureAgentsBlock(l.RepoRoot)
				if err != nil {
					return err
				}
				for _, f := range changed {
					fmt.Fprintf(cmd.ErrOrStderr(), "updated %s\n", f)
				}
			}
			message := ""
			if len(args) == 1 {
				message = args[0]
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			start := time.Now()
			res, err := generate.Generate(ctx, generate.Options{
				Env:      run.Env{Layout: l, Producer: version.Producer(), Model: provider.Name()},
				Provider: provider, Mode: mode, Message: message,
				Progress: progressPrinter(cmd.ErrOrStderr(), verbose),
			})
			out := cmd.OutOrStdout()
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return fmt.Errorf("interrupted; run `owcli %s` again to resume", mode)
				}
				return err
			}
			if res.Noop {
				fmt.Fprintln(out, "The wiki is up to date; nothing to do.")
				return nil
			}
			fmt.Fprintf(out, "%s %s in %s: %d page(s) written", strings.ToUpper(string(mode[:1]))+string(mode[1:]), res.Finish.Status, time.Since(start).Round(time.Second), len(res.Written))
			if len(res.Skipped) > 0 {
				fmt.Fprintf(out, ", %d skipped (%s)", len(res.Skipped), strings.Join(res.Skipped, ", "))
			}
			if len(res.Finish.Deleted) > 0 {
				fmt.Fprintf(out, ", %d deleted", len(res.Finish.Deleted))
			}
			fmt.Fprintf(out, ".\nWiki: %s\n", l.WikiRoot)
			if sha, err := commitExternal(l, mode, res.Finish.Status, len(res.Written), len(res.Skipped), res.RunID, res.GitHead); err != nil {
				return fmt.Errorf("the run finished but committing the wiki failed: %w", err)
			} else if sha != "" {
				fmt.Fprintf(out, "Committed wiki as %s.\n", sha)
			}
			fmt.Fprintf(out, "Tokens: %d input (%d cached), %d output.\n", res.Usage.InputTokens+res.Usage.CacheReadTokens+res.Usage.CacheWriteTokens, res.Usage.CacheReadTokens, res.Usage.OutputTokens)
			if res.Finish.SourceChanged {
				fmt.Fprintln(out, "The source changed during the run; run `owcli update` to reconcile.")
			}
			if n := len(res.Finish.Report.Links.Issues); n > 0 {
				fmt.Fprintf(out, "%d broken link(s) were marked in the pages for the next update.\n", n)
			}
			return nil
		},
	}
	if mode == run.Init {
		cmd.Flags().BoolVar(&external, "external", false, "if the repository is not bound yet, store the wiki outside it")
		cmd.Flags().StringVar(&wikiDir, "wiki-dir", "", "if the repository is not bound yet, store the wiki in this directory (implies --external)")
	}
	cmd.Flags().BoolVar(&agentsMD, "agents-md", false, "add or refresh a pointer to the wiki in AGENTS.md (in-repo wikis only)")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "show every tool call")
	return cmd
}

// layoutFor resolves the wiki location. init binds an unbound repository
// (in-repo unless --external); update requires an existing binding.
func layoutFor(mode run.Mode, external bool, wikiDir string) (store.Layout, error) {
	dirs, err := store.DefaultDirs()
	if err != nil {
		return store.Layout{}, err
	}
	l, err := dirs.Resolve(".")
	if err == nil {
		if (external || wikiDir != "") && l.Kind != store.External {
			return l, fmt.Errorf("%s is already bound %s; use `owcli unbind` first to switch", l.RepoRoot, l.Kind)
		}
		return l, nil
	}
	if !errors.Is(err, store.ErrUnbound) || mode != run.Init {
		return l, err
	}
	kind := store.InRepo
	if external || wikiDir != "" {
		kind = store.External
	}
	l, err = dirs.Bind(".", kind, wikiDir, time.Now())
	if err != nil {
		return l, err
	}
	return l, l.EnsureWikiRepo()
}

// commitExternal versions an external wiki after a finished run.
func commitExternal(l store.Layout, mode run.Mode, status string, written, skipped int, runID, head string) (string, error) {
	subject := fmt.Sprintf("owcli %s %s: %d page(s) written", mode, status, written)
	if skipped > 0 {
		subject += fmt.Sprintf(", %d skipped", skipped)
	}
	subject += fmt.Sprintf(" @ %s %s", filepath.Base(l.RepoRoot), short(head))
	body := fmt.Sprintf("Repository: %s\nSource commit: %s\nRun: %s", l.RepoRoot, head, runID)
	return l.CommitWiki(subject, body)
}

func progressPrinter(w io.Writer, verbose bool) func(generate.Progress) {
	return func(p generate.Progress) {
		if p.Event != nil {
			switch {
			case !verbose:
			case p.Event.Kind == "tool" || p.Event.Kind == "tool_error":
				fmt.Fprintf(w, "    %s %s%s\n", p.Event.Tool, summarize(p.Event.Input), errSuffix(p.Event))
			case p.Event.Kind == "text":
				fmt.Fprintf(w, "    > %s\n", firstLine(strings.TrimSpace(p.Event.Text)))
			}
			return
		}
		switch p.Stage {
		case "begin":
			fmt.Fprintf(w, "owcli: %s\n", p.Note)
		case "page":
			fmt.Fprintf(w, "[%d/%d] %s\n", p.Index, p.Total, p.Page)
		case "skip":
			fmt.Fprintf(w, "[%d/%d] %s %s\n", p.Index, p.Total, p.Page, p.Note)
		case "finish":
			fmt.Fprintln(w, "owcli: finalizing")
		}
	}
}

func errSuffix(e *agent.Event) string {
	if e.Kind == "tool_error" {
		return " (error: " + firstLine(e.Text) + ")"
	}
	return ""
}

// summarize renders a tool input compactly for progress output.
func summarize(raw json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	for _, k := range []string{"path", "pattern"} {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

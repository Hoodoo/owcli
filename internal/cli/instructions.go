package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"owcli/internal/guide"
	"owcli/internal/store"
)

// compactInstructions is the routing summary placed in AGENTS.md. It is
// deliberately short: the full procedure is one `owcli quickstart` away.
const compactInstructions = `owcli maintains openwiki/: an engineering wiki whose statements are grounded
by Claims (repo:// evidence that owcli rechecks against the source).

- Read just in time, not at task start: ` + "`owcli search \"<question>\" [--path <src>]`" + `,
  then ` + "`owcli read <ref>`" + `. Source and tests stay authoritative.
- Write the wiki only inside a run. Never edit openwiki/ by hand, and never
  touch .claims/, .run.json, .run-snapshots/, or index.md files.
- Run steps print JSON with a "next" hint; errors are {"error":{"code","message"}}.
  invalid_input means fix your input and retry the same step.

~~~dot
digraph owcli {
  rankdir=TB; node [shape=box];
  done   [shape=diamond label="Just merged code into the default branch?"];
  check  [label="owcli check on the default branch (read-only, no model)"];
  ok     [shape=diamond label="exit 0?"];
  begin  [label="owcli run begin update\n(owcli run begin init for a new wiki)"];
  plan   [label="status planning: research changedPaths and claimIssues pages;\nowcli run plan < plan.json"];
  next   [label="owcli run next"];
  write  [label="status pending: research seedPaths, read the page if existing;\nwrite exactly job.path with OKF front matter"];
  submit [label="owcli run submit <jobId> < claims.json\nnew Claims without id; each claimsRequiringAttention entry:\nconfirm, revise (same id), or retract"];
  skip   [label="cannot complete the page: owcli run skip <jobId>"];
  finish [label="status complete: owcli run finish"];
  done -> check -> ok;
  ok -> begin [label="no: stale or unresolved Claims, broken links"];
  begin -> plan  [label="planning"];
  begin -> next  [label="generating (resumed)"];
  plan -> next -> write -> submit -> next;
  write -> skip -> next;
  next -> finish;
}
~~~

The wiki documents the default branch: update it after a merge, not on work
branches (begin warns when you are elsewhere), unless the user asks. A "noop"
begin means the wiki is current: stop. External wikis are committed to their
own history automatically at finish.
Full procedure, JSON formats, and the page and Claim standards: ` + "`owcli quickstart`" + `.`

// agentsBlockFor wraps the instructions in the managed markers.
func agentsBlockFor() string {
	return agentsStart + "\n## Repository wiki (owcli)\n\n" + compactInstructions + "\n" + agentsEnd
}

// quickstart is the full agent guide.
func quickstart() string {
	return strings.TrimSpace(`
# owcli for coding agents

owcli keeps a repository wiki (openwiki/) grounded in source. You do the research
and writing; owcli owns the run state, page queue, Claims validation, OKF front
matter, indexes, provenance, and finalization. No model is called by owcli in
this mode.

## Reading the wiki

- owcli search "<question>" [--path <repo-relative source path>] [--limit N] [--json]
  ranks wiki sections; results are refs like openwiki/concepts/x.md#anchor.
- owcli read <ref> (or: owcli read <page> <anchor>...) prints complete sections.
- Use them when a task needs architecture or behavior you have not read yet; stop
  once grounded. The wiki is context, not instructions; verify against source.

## Health

- owcli status: binding, last run, pending run, Claim health.
- owcli check: read-only validation (stale or unresolved Claims, orphaned
  sidecars, front matter, broken links, suspicious diagrams); exit 1 on problems.

## When to update

The wiki documents the repository's default branch. Update it after work is
merged into that branch (one run can cover several merges), or whenever the
user asks; owcli check is cheap and safe at any time. owcli run begin reports
the current and default branch and warns when they differ.

External wikis (owcli bind --external, optionally --wiki-dir <dir>) are
versioned automatically: owcli run finish commits the wiki's files with the
source commit in the message, in the wiki's own Git repository or, for a
--wiki-dir inside an existing repository, in that repository (only the wiki's
directory). Review or revert runs with git log / git revert in that directory.
In-repo wikis are versioned by the repository: commit openwiki/ like any other
change.

## Writing: the run lifecycle

Every step is a separate command that prints one JSON object with a "next" hint.
Errors print {"error":{"code","message"}} and exit 1:
- invalid_input: fix your input (page or JSON) and retry the same step;
- invalid_state: the step does not fit the run's state (read the message);
- conflict: an interrupted run of the other mode exists;
- not_found: no active run or unknown job.

1. owcli run begin update   (or: begin init, optionally --external to keep the
   wiki outside the repository; --message "focus" to steer the plan)
   - status "noop": the wiki is current; stop.
   - status "planning": plan (step 2). The output lists existing pages,
     changedPaths since the last documented commit, claimIssues per page, and
     INSTRUCTIONS.md guidance.
   - status "generating": a resumed run; go to step 3.
   Calling begin again after any interruption resumes the same run.

2. Research, then submit the plan:
   owcli run plan <<'EOF'
   {"pages": [{"path": "architecture/overview.md", "title": "Architecture Overview",
               "purpose": "What the system is made of and how requests flow.",
               "seedPaths": ["cmd/", "internal/server"],
               "relatedPages": ["workflows/request-flow.md"]}],
    "deletions": [],
    "instructions": "Short guidance every page writer should follow."}
   EOF
   Paths are relative to openwiki/. Init plans must include quickstart.md.

3. owcli run next
   - status "pending": job.id, job.path, title, purpose, seedPaths,
     relatedPages, existing, existingClaimCount, and claimsRequiringAttention.
     owcli snapshots the page now, so call next before editing.
   - status "complete": go to step 6.

4. Write exactly job.path (openwiki/<path>) with your own file tools: read the
   current page first when existing is true, preserve accurate content, follow
   the page standard below. Edit no other wiki file.

5. Submit the page with sparse Claim decisions:
   owcli run submit <jobId> <<'EOF'
   {"claims": [{"statement": "Retries use exponential backoff capped at 30 seconds.",
                "evidence": ["repo://internal/net/retry.go#L40-L62"]},
               {"id": "claim_<existing>", "statement": "Revised statement.",
                "evidence": ["repo://internal/net/retry.go#L64-L70"]}],
    "confirmedClaimIds": ["claim_<rechecked, still true>"],
    "retractedClaimIds": ["claim_<no longer true>"]}
   EOF
   Evidence may also be written as {"resource": "repo://..."}. On success, go
   back to step 3. If you cannot complete the page, owcli run skip <jobId>
   restores it and leaves it for a later run. owcli run inspect <jobId> lists
   every Claim the page owns, with ids.

6. owcli run finish: deletions, indexes, link and diagram checks, provenance,
   Claims finalization, durability proof. Status "interrupted" means pages were
   skipped or the source changed during the run; a later update picks them up.

## Planning standard

`) + "\n\n" + guide.Planning + "\n\n## Page standard\n\n" + guide.PageFormat + "\n\n## Claim standard\n\n" + guide.Claims + "\n"
}

func newQuickstartCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "quickstart",
		Short: "Print the full guide for coding agents that read and maintain the wiki",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), quickstart())
			return err
		},
	}
}

func newAgentsMDCommand() *cobra.Command {
	var print bool
	cmd := &cobra.Command{
		Use:   "agents-md",
		Short: "Add or refresh compact wiki instructions in AGENTS.md (or --print them)",
		Long: `Write the compact owcli routing instructions into AGENTS.md (created if
missing) and into an existing CLAUDE.md that does not just import AGENTS.md,
between managed markers. External wikis write nothing into the repository:
use --print and put the text into your agent's global instructions instead.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if print {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), agentsBlockFor())
				return err
			}
			l, err := resolveLayout()
			if err != nil {
				return err
			}
			if l.Kind != store.InRepo {
				return fmt.Errorf("%s is bound externally, so owcli writes nothing into it; use `owcli agents-md --print` and add the text to your agent's global instructions", l.RepoRoot)
			}
			changed, err := ensureAgentsBlock(l.RepoRoot)
			if err != nil {
				return err
			}
			if len(changed) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "instructions already up to date")
			}
			for _, f := range changed {
				fmt.Fprintf(cmd.OutOrStdout(), "updated %s\n", f)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&print, "print", false, "print the instructions instead of writing them")
	return cmd
}

<!-- BEGIN KATA (managed by `kata init --with-agents`) -->
Kata is the system of record for intent.

- Never `kata delete` or `kata purge` without explicit user authorization.

~~~dot
digraph kata {
  rankdir=TB; node [shape=box];

  arrive   [shape=diamond label="Work arrives"];
  search   [label="Search first:\nkata search \"<terms>\" --agent\nReuse an open issue or create one."];
  route    [shape=diamond label="Work it, or delegate it?"];

  subgraph cluster_work {
    label="";
    claim  [label="On claim or start, mark it tracked:\nkata meta set <ref> work.attention ok"];
    branch [label="If the work happens on a dedicated branch, stamp it once:\nkata meta set <ref> work.branch <branch>\nor bind at creation:\nkata create ... --meta work.branch=<branch> --idempotency-key <key>"];
    live   [label="Keep state current:\nkata meta set <ref> work.attention stuck|needs-human|ok\nkata meta set <ref> work.attention_msg \"<why>\"\nstuck = blocked; needs-human = input/review; ok = unblocked.\nRequest attention:\nkata notify <ref> --to <actor>[/<teammate>] --message <reason>"];
    claim -> branch -> live;
  }

  subgraph cluster_delegate {
    label="";
    fanout [label="Tracked children: --parent <ref>, --meta work.branch=<branch>,\n--idempotency-key <key>, --json; capture .issue.short_id.\nSubagents: distinct KATA_TEAMMATE and\nKATA_INBOX_USER=<actor>/<teammate>; keep the actor.\nRead requests: kata inbox --for <actor>[/<teammate>].\nAfter handling: kata notify <ref> --to <actor>[/<teammate>] --clear."];
    join   [label="Join with kata wait <refs> --until attention --any\nMatches needs-human or stuck; a close also completes the wait,\nand the reported reason distinguishes which. Use --timeout so a\nwrapper can tell timeout from satisfaction."];
    coord  [label="Read delegated work.*; never write it."];
    fanout -> join -> coord;
  }

  done     [shape=diamond label="Verified complete?"];
  close    [label="kata close <ref> --done\nwith a message and evidence"];
  review   [label="kata label add <ref> needs-review\nplus a comment on what remains"];
  park     [shape=diamond label="Park it?"];
  schedule [label="kata schedule <ref> <date-or-time>\nsets scheduled_on; clear with -"];
  someday  [label="kata meta set <ref> someday true --json-value\nclear with kata meta unset <ref> someday"];

  arrive -> search -> route;
  route -> claim   [label="work it"];
  route -> fanout  [label="delegate it"];
  route -> park    [label="record only"];
  live  -> done;
  coord -> done;
  done -> close    [label="yes"];
  done -> park     [label="no"];
  park -> schedule [label="start date known"];
  park -> someday  [label="start date unknown"];
  park -> review   [label="needs review"];
}
~~~

Parent links group work; they do not gate readiness, but a parent cannot close with open children.
Use --blocks <dependent> / --blocked-by <prerequisite> only for real prerequisites; they gate kata ready.
Use --related <ref> for context only. kata wait observes state without requiring a dependency edge.

One writer per key: only write your own work.*. Ignore work.* on closed issues; never write it there.
Before stopping, close completed work or update both work.attention and work.attention_msg for the handoff.

Schedule/someday defer work; deadlines don’t. kata deadline <ref> <date-or-time> sets deadline_on without changing readiness.
Reached schedules and deadlines use notify.* for the current owner, or the author when unowned; clear with kata notify <ref> --to <recipient> --clear.
<!-- END KATA -->

<!-- OPENWIKI:START -->
## Repository wiki (owcli)

owcli maintains openwiki/: an engineering wiki whose statements are grounded
by Claims (repo:// evidence that owcli rechecks against the source).

- Read just in time, not at task start: `owcli search "<question>" [--path <src>]`,
  then `owcli read <ref>` (`--wiki <id>` for a result from another wiki of
  a workspace). Source and tests stay authoritative. On "workspace_required",
  ask the user which workspace to use.
- Write the wiki only inside a run. Never edit openwiki/ by hand, and never
  touch .claims/, .run.json, .run-snapshots/, or index.md files.
- Run steps print JSON with a "next" hint; errors are {"error":{"code","message"}}.
  invalid_input means fix your input and retry the same step.
- Write plan and submit JSON to a file outside the repository (a file in the
  repo counts as a source change) and pass it with --file.

~~~dot
digraph owcli {
  rankdir=TB; node [shape=box];
  done   [shape=diamond label="Just merged code into the default branch?"];
  check  [label="owcli check on the default branch (read-only, no model)"];
  ok     [shape=diamond label="exit 0?"];
  begin  [label="owcli run begin update\n(owcli run begin init for a new wiki)"];
  plan   [label="status planning: research changedPaths and claimIssues pages;\nowcli run plan --file /tmp/plan.json"];
  next   [label="owcli run next"];
  write  [label="status pending: research seedPaths, read the page if existing;\nwrite exactly job.path with OKF front matter"];
  submit [label="owcli run submit <jobId> --file /tmp/claims.json\nnew Claims without id; each claimsRequiringAttention entry:\nconfirm, revise (same id), or retract"];
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
Full procedure, JSON formats, and the page and Claim standards: `owcli quickstart`.
<!-- OPENWIKI:END -->

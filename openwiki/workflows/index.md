# Files

- [Agent-Driven Runs and Agent Instructions](agent-driven-runs.md) - How an interactive coding agent drives owcli init and update itself through the owcli run commands (JSON in and out, no model calls by owcli), how snapshots and resumption work across processes, and how owcli agents-md and owcli quickstart deliver Kata-style instructions.
- [Generation Run Lifecycle](generation-run.md) - How owcli init and update run - the resumable begin/plan/next/submit/skip/finish lifecycle and its checkpoint, source fingerprinting and clean-update detection, the planner and page-worker agents with their confined workspace and tools, and how failures are rolled back or resumed.
- [Browser Viewer](viewer.md) - How owcli serve shows wikis in a browser - the loopback server and its default scope, the JSON API, how the page graph is built, page rendering with Claims, the embedded UI and its navigation rules, and how the viewer is tested.

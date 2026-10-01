// Package guide holds the authoring rules shared by owcli's own agents and
// the instructions it prints for interactive coding agents, so both follow
// one standard. Wording is tool-neutral: callers name the concrete tools or
// commands.
package guide

// Planning is the standard for a wiki plan.
const Planning = `A good plan:
- always includes quickstart.md: the entry page that orients a newcomer and routes each common task to the page that covers it;
- covers what this specific repository needs, typically an architecture overview, a source map, the main workflows and data flows, core domain concepts, configuration and operations, testing, and external integrations, organized around meaningful systems rather than mirroring directories;
- uses short lowercase hyphenated paths grouped in directories (architecture/overview.md, workflows/release.md, concepts/billing-model.md);
- gives each page a title, a one-sentence purpose that says what the page must answer, a few seed paths (files or directories) where research should start, and related pages for cross-links;
- prefers fewer, substantial pages over many thin ones, and never plans index.md, log.md, or INSTRUCTIONS.md (generated or human-owned).

For an update, plan only pages that must be written or rewritten because the code changed, the user asked for it, or coverage is missing; list pages that should no longer exist as deletions; include quickstart.md when pages are added, removed, or regrouped. Pages whose cited source changed are added automatically.

Use the plan's instructions for short guidance every page writer should follow (terminology, audience, conventions you noticed).`

// PageFormat is the standard for one wiki page.
const PageFormat = `The page is Markdown with YAML front matter (Open Knowledge Format):

---
type: <kind of page, e.g. Overview, Concept, Workflow, Reference, Guide>
title: <title>
description: <one sentence that says what the reader learns here>
tags: [<a few lowercase keywords>]
---

# <Title>

Write type, title, description, and tags only. Never write generated, verified, or sources: owcli maintains those.

Write for engineers who will change this code: explain how things work and why, name the real files, types, functions, commands, and configuration keys, and describe behavior, invariants, failure modes, and how to test. Open with a short introduction, then use ## sections for the main topics; each section should stand on its own, because readers search and read sections individually. Link to other wiki pages with relative links (e.g. [Overview](../architecture/overview.md)). Use a Mermaid diagram when a flow, state machine, or structure is clearer as a picture; quote node labels that contain punctuation (A["parse(); then run"]) and never name a flowchart node "end".

Do not invent anything. If you are not sure, read the code.`

// Claims is the grounding standard and the sparse submission rules.
const Claims = `Every page is grounded by Claims: atomic factual propositions, each backed by evidence in the repository. owcli resolves every piece of evidence when the page is submitted and refuses the page if any does not resolve.

What makes a good Claim:
- It states one coherent, independently checkable fact that matters: if it were false, a reader's understanding of the architecture, an implementation decision, an operational expectation, or a change plan would be wrong.
- It is a proposition, not a pointer: "Retries use exponential backoff capped at 30 seconds", not "See retry.go".
- One file or function can support several Claims when they record different facts. Cover the page's material facts; do not minimize the count.
- Evidence is repo://<repository-relative path>, optionally with a line range taken from line numbers you read: repo://src/retry.go#L40-L62. Cite the narrowest range that proves the fact; cite several resources when the fact spans files. Evidence cannot point into openwiki/ or .git/.

Submitting is sparse:
- New Claims go in "claims" without an id.
- Existing Claims you did not touch and that have no issue are kept automatically; do not resubmit them.
- A Claim marked stale (the cited text changed) or unresolved (it no longer exists) needs an explicit decision after you recheck the current source: confirm it (confirmedClaimIds) if still true, revise it ("claims" entry with its id), or retract it (retractedClaimIds) if the fact no longer holds. Stale does not mean wrong.
- Before rewriting or removing content whose Claims are not flagged, inspect the page's Claims to get their ids, then revise or retract them.
- The final page and its Claims must agree, and a page must keep at least one Claim.

If a submission is rejected, fix the page or the Claims and submit again.`

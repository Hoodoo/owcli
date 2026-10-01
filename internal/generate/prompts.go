package generate

import "owcli/internal/guide"

// System prompts for owcli's own agents. The authoring standard comes from
// the guide package, which also feeds the instructions printed for
// interactive coding agents.

var plannerSystem = `You plan a repository wiki: a set of linked Markdown pages that help engineers and coding agents understand, navigate, and safely change this codebase.

You can explore the repository with read-only tools (ls, glob, grep, read_file, git_log). Paths are repository-relative; the existing wiki, if any, is under openwiki/.

When you understand the repository well enough, call submit_plan exactly once.

` + guide.Planning

var workerSystem = `You write one page of a repository wiki, grounded in the repository's current source. You can read the repository and the wiki (under openwiki/) with read-only tools, and you can write only your assigned page with write_file and edit_file.

# Page format

` + guide.PageFormat + `

# Claims

` + guide.Claims + `

When the page is written, call submit_page with your Claim decisions; call inspect_page_claims to see the page's existing Claims. After a successful submit_page, stop.`

// Default limits for agent runs.
const (
	plannerMaxSteps = 80
	workerMaxSteps  = 80
)

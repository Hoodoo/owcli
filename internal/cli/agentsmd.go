package cli

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"owcli/internal/store"
)

// The managed block uses upstream's markers so a repository never ends up
// with two competing blocks.
const (
	agentsStart = "<!-- OPENWIKI:START -->"
	agentsEnd   = "<!-- OPENWIKI:END -->"
	agentsBlock = agentsStart + `
## Repository wiki

This repository has an engineering wiki under [openwiki/](openwiki/quickstart.md),
kept grounded in the source by owcli. Start with openwiki/quickstart.md, follow
its links to the relevant pages, and search it with ` + "`owcli search \"<question>\"`" + `.
` + agentsEnd
)

// ensureAgentsBlock adds or refreshes the managed block in AGENTS.md
// (created if missing) and in an existing CLAUDE.md that does not simply
// import AGENTS.md. It returns the files it changed.
func ensureAgentsBlock(repo string) ([]string, error) {
	var changed []string
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		p := filepath.Join(repo, name)
		data, err := os.ReadFile(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if name == "CLAUDE.md" {
				continue
			}
		case err != nil:
			return changed, err
		}
		content := string(data)
		if name == "CLAUDE.md" && strings.TrimSpace(content) == "@AGENTS.md" {
			continue
		}
		next := withBlock(content)
		if next == content {
			continue
		}
		if err := store.WriteFileAtomic(p, []byte(next), 0o644); err != nil {
			return changed, err
		}
		changed = append(changed, name)
	}
	return changed, nil
}

func withBlock(content string) string {
	start, end := strings.Index(content, agentsStart), strings.Index(content, agentsEnd)
	if start >= 0 && end > start {
		return content[:start] + agentsBlock + content[end+len(agentsEnd):]
	}
	if strings.TrimSpace(content) == "" {
		return agentsBlock + "\n"
	}
	return strings.TrimRight(content, "\n") + "\n\n" + agentsBlock + "\n"
}

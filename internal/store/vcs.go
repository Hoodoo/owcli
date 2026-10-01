package store

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// External wikis are versioned with Git so runs can be reviewed, reverted,
// and shared. The repository is Home (wikis/<id> by default) unless Home
// already lies inside a Git work tree the user owns, such as a shared
// knowledge-base repository; then only Home is committed there. In-repo
// wikis are versioned by the documented repository itself and are never
// committed by owcli.

// wikiIgnore keeps transient run state out of history.
const wikiIgnore = ".run.json\n.run-snapshots/\n"

func gitIn(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// EnsureWikiRepo makes an external wiki versionable: it initializes a Git
// repository in Home unless Home is already inside one, and adds an ignore
// file for transient run state. It is a no-op for in-repo wikis.
func (l Layout) EnsureWikiRepo() error {
	if l.Kind != External {
		return nil
	}
	if err := os.MkdirAll(l.Home, 0o755); err != nil {
		return err
	}
	if _, err := gitIn(l.Home, "rev-parse", "--show-toplevel"); err != nil {
		if _, err := gitIn(l.Home, "init", "-q"); err != nil {
			return err
		}
	}
	ignorePath := filepath.Join(l.Home, ".gitignore")
	if _, err := os.Stat(ignorePath); errors.Is(err, fs.ErrNotExist) {
		return WriteFileAtomic(ignorePath, []byte(wikiIgnore), 0o644)
	}
	return nil
}

// CommitWiki commits the current state of an external wiki and returns the
// new commit's short hash, or "" when nothing changed. Only paths under Home
// are staged and committed, so unrelated changes in a shared repository are
// left alone. When no Git identity is configured, owcli's own is used.
func (l Layout) CommitWiki(subject, body string) (string, error) {
	if l.Kind != External {
		return "", nil
	}
	if err := l.EnsureWikiRepo(); err != nil {
		return "", err
	}
	if _, err := gitIn(l.Home, "add", "-A", "--", "."); err != nil {
		return "", err
	}
	if _, err := gitIn(l.Home, "diff", "--cached", "--quiet", "--", "."); err == nil {
		return "", nil
	}
	args := []string{}
	if email, _ := gitIn(l.Home, "config", "user.email"); email == "" {
		args = append(args, "-c", "user.name=owcli", "-c", "user.email=owcli@localhost")
	}
	msg := subject
	if body != "" {
		msg += "\n\n" + body
	}
	args = append(args, "commit", "-q", "-m", msg, "--", ".")
	if _, err := gitIn(l.Home, args...); err != nil {
		return "", err
	}
	return gitIn(l.Home, "rev-parse", "--short", "HEAD")
}

package run

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Hoodoo/owcli/internal/ignore"
)

func git(repo string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// gitHead returns the HEAD commit, or "" in a repository without commits.
func gitHead(repo string) (string, error) {
	out, err := git(repo, "rev-parse", "--verify", "--quiet", "HEAD")
	if err != nil {
		if _, verr := git(repo, "rev-parse", "--git-dir"); verr == nil {
			return "", nil // no commits yet
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// sourceFiles lists tracked and untracked, not git-ignored files that are
// visible as source: outside the wiki and not excluded by .openwikiignore.
func sourceFiles(repo string, m *ignore.Matcher) ([]string, error) {
	out, err := git(repo, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var files []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p == "" || seen[p] || m.Ignores(p, false) {
			continue
		}
		seen[p] = true
		files = append(files, p)
	}
	sort.Strings(files)
	return files, nil
}

// fingerprint hashes every model-visible source input: HEAD plus the path
// and content of each source file. Any failure is an error, never a guess,
// because the fingerprint gates resuming.
func fingerprint(repo string, m *ignore.Matcher) (string, error) {
	head, err := gitHead(repo)
	if err != nil {
		return "", err
	}
	files, err := sourceFiles(repo, m)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	fmt.Fprintf(h, "head %s\n", head)
	for _, p := range files {
		abs := filepath.Join(repo, filepath.FromSlash(p))
		fi, err := os.Lstat(abs)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			fmt.Fprintf(h, "%s\x00deleted\n", p)
			continue
		case err != nil:
			return "", err
		case fi.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(abs)
			if err != nil {
				return "", err
			}
			fmt.Fprintf(h, "%s\x00link %s\n", p, target)
			continue
		case !fi.Mode().IsRegular():
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256(data)
		fmt.Fprintf(h, "%s\x00%s\n", p, hex.EncodeToString(sum[:]))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// worktreeClean reports whether git sees no changes to source files (the
// wiki and ignored paths do not count).
func worktreeClean(repo string, m *ignore.Matcher) (bool, error) {
	out, err := git(repo, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return false, err
	}
	entries := strings.Split(string(out), "\x00")
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) < 4 {
			continue
		}
		status, p := e[:2], e[3:]
		if status[0] == 'R' || status[0] == 'C' {
			i++ // the next entry is the original path
		}
		if !m.Ignores(p, false) {
			return false, nil
		}
	}
	return true, nil
}

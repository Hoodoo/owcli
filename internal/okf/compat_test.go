package okf

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUpstreamWikiCompat checks every page of an upstream-generated wiki.
// Set OWCLI_UPSTREAM_DIR to a checkout of langchain-ai/openwiki to run it.
func TestUpstreamWikiCompat(t *testing.T) {
	dir := os.Getenv("OWCLI_UPSTREAM_DIR")
	if dir == "" {
		t.Skip("OWCLI_UPSTREAM_DIR not set")
	}
	wiki := filepath.Join(dir, "openwiki")
	n := 0
	err := filepath.WalkDir(wiki, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
			return err
		}
		rel, _ := filepath.Rel(wiki, p)
		if strings.HasPrefix(rel, ".") || filepath.Base(rel) == "index.md" {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		n++
		content := string(data)
		if issues := Validate(content); issues != nil {
			t.Errorf("%s: %v", rel, issues)
		}
		if out, changed := Repair(content, rel, ""); changed || out != content {
			t.Errorf("%s: Repair changed a valid upstream page", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no pages found")
	}
	t.Logf("checked %d upstream pages", n)
}

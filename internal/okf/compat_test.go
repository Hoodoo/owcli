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

// TestUpstreamFinalizeIsNoop copies an upstream wiki, runs every
// finalization pass over it, and expects no byte to change: indexes,
// provenance, mermaid, and links already conform.
func TestUpstreamFinalizeIsNoop(t *testing.T) {
	dir := os.Getenv("OWCLI_UPSTREAM_DIR")
	if dir == "" {
		t.Skip("OWCLI_UPSTREAM_DIR not set")
	}
	src := filepath.Join(dir, "openwiki")
	dst := filepath.Join(t.TempDir(), "openwiki")
	original := map[string]string{}
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		original[rel] = string(data)
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	w := Wiki{Root: dst, Repo: dir}
	snap, err := w.SnapshotProvenance()
	if err != nil {
		t.Fatal(err)
	}
	report, err := w.Finalize(FinalizeOptions{Provenance: snap, Now: "2030-01-01T00:00:00.000Z", Producer: "owcli/compat"})
	if err != nil {
		t.Fatal(err)
	}
	for _, is := range report.Links.Issues {
		t.Logf("link issue: %s:%d %s %s", is.Page, is.Line, is.Href, is.Message)
	}
	changed := 0
	for rel, before := range original {
		after, err := os.ReadFile(filepath.Join(dst, rel))
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		if string(after) != before {
			changed++
			t.Errorf("%s changed", rel)
		}
	}
	t.Logf("%d files, %d changed; %d links checked, %d fences checked", len(original), changed, report.Links.LinksChecked, report.Mermaid.FencesChecked)
}

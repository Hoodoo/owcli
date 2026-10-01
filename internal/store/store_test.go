package store

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", ".")
	git(t, dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "init")
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func testDirs(t *testing.T) Dirs {
	base := t.TempDir()
	return Dirs{Config: filepath.Join(base, "config"), Data: filepath.Join(base, "data")}
}

func TestExternalBindWritesNothingIntoRepo(t *testing.T) {
	repo := gitRepo(t)
	d := testDirs(t)
	sub := filepath.Join(repo, "pkg")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	l, err := d.Bind(sub, External, now)
	if err != nil {
		t.Fatal(err)
	}
	if l.RepoRoot != repo {
		t.Errorf("RepoRoot = %s, want %s", l.RepoRoot, repo)
	}
	if strings.HasPrefix(l.WikiRoot, repo) {
		t.Errorf("external wiki %s is inside the repo", l.WikiRoot)
	}
	if filepath.Base(l.WikiRoot) != WikiDirName {
		t.Errorf("WikiRoot %s should end in %s", l.WikiRoot, WikiDirName)
	}
	// Write state through the layout, as a run would.
	if err := l.SaveManifest(NewManifest()); err != nil {
		t.Fatal(err)
	}
	if status := git(t, repo, "status", "--porcelain", "--ignored"); status != "" {
		t.Errorf("repo not clean after external bind:\n%s", status)
	}

	got, err := d.Resolve(repo)
	if err != nil || got != l {
		t.Fatalf("Resolve = %+v, %v; want %+v", got, err, l)
	}
}

func TestResolveImplicitInRepoAndUnbound(t *testing.T) {
	repo := gitRepo(t)
	d := testDirs(t)
	if _, err := d.Resolve(repo); !errors.Is(err, ErrUnbound) {
		t.Fatalf("want ErrUnbound, got %v", err)
	}
	if err := os.Mkdir(filepath.Join(repo, WikiDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := d.Resolve(repo)
	if err != nil {
		t.Fatal(err)
	}
	if l.Kind != InRepo || l.WikiRoot != filepath.Join(repo, WikiDirName) {
		t.Fatalf("got %+v", l)
	}
}

func TestExplicitBindingWinsOverInRepoWiki(t *testing.T) {
	repo := gitRepo(t)
	d := testDirs(t)
	if err := os.Mkdir(filepath.Join(repo, WikiDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Bind(repo, External, now); err != nil {
		t.Fatal(err)
	}
	l, err := d.Resolve(repo)
	if err != nil || l.Kind != External {
		t.Fatalf("got %+v, %v", l, err)
	}
}

func TestBindIdempotentAndKindSwitchRefused(t *testing.T) {
	repo := gitRepo(t)
	d := testDirs(t)
	a, err := d.Bind(repo, External, now)
	if err != nil {
		t.Fatal(err)
	}
	b, err := d.Bind(repo, External, now.Add(time.Hour))
	if err != nil || a != b {
		t.Fatalf("rebind changed layout: %+v vs %+v (%v)", a, b, err)
	}
	if _, err := d.Bind(repo, InRepo, now); err == nil {
		t.Fatal("switching kinds without unbind should fail")
	}
}

func TestUnbind(t *testing.T) {
	repo := gitRepo(t)
	d := testDirs(t)
	l, err := d.Bind(repo, External, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Unbind(repo, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(l.WikiRoot); err != nil {
		t.Error("unbind without purge must keep the external wiki")
	}
	if _, err := d.Unbind(repo, false); !errors.Is(err, ErrUnbound) {
		t.Errorf("second unbind: want ErrUnbound, got %v", err)
	}

	if _, err := d.Bind(repo, External, now); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Unbind(repo, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(l.WikiRoot)); !os.IsNotExist(err) {
		t.Error("purge should delete the external wiki")
	}

	if _, err := d.Bind(repo, InRepo, now); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Unbind(repo, true); err == nil {
		t.Error("purging an in-repo wiki must be refused")
	}
}

func TestRegistryValidation(t *testing.T) {
	repo := gitRepo(t)
	d := testDirs(t)
	for _, content := range []string{
		`{"schemaVersion": 2, "bindings": {}}`,
		`{"schemaVersion": 1, "bindings": {"relative": {"kind": "in-repo"}}}`,
		`{"schemaVersion": 1, "bindings": {"/x": {"kind": "external"}}}`,
		`{"schemaVersion": 1, "bindings": {"/x": {"kind": "weird"}}}`,
		`{"schemaVersion": 1} {}`,
		`not json`,
	} {
		if err := WriteFileAtomic(d.registryPath(), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Resolve(repo); !errors.Is(err, ErrInvalidState) {
			t.Errorf("registry %s: want ErrInvalidState, got %v", content, err)
		}
	}
}

func TestRepoRootOutsideGit(t *testing.T) {
	if _, err := RepoRoot(t.TempDir()); err == nil {
		t.Fatal("want error outside a Git repository")
	}
}

func TestExternalID(t *testing.T) {
	a := ExternalID("/home/u/My Project!")
	if !strings.HasPrefix(a, "my-project-") || len(a) != len("my-project-")+12 {
		t.Errorf("unexpected id %q", a)
	}
	if a == ExternalID("/other/My Project!") {
		t.Error("ids for different roots must differ")
	}
	if a != ExternalID("/home/u/My Project!") {
		t.Error("ids must be stable")
	}
	if !strings.HasPrefix(ExternalID("/"), "repo-") {
		t.Error("empty slug should fall back to repo")
	}
}

func TestPageIDs(t *testing.T) {
	if got := PageID("concepts/x.md"); got != "/openwiki/concepts/x.md" {
		t.Errorf("PageID = %s", got)
	}
	if got := PageID("../../etc/passwd"); got != "/openwiki/etc/passwd" {
		t.Errorf("PageID must not escape: %s", got)
	}
	for _, bad := range []string{"/openwiki/", "/other/x.md", "/openwiki/../x.md", "/openwiki/a//b.md"} {
		if _, ok := PageRel(bad); ok {
			t.Errorf("PageRel(%q) accepted", bad)
		}
	}
	l := Layout{WikiRoot: "/w/openwiki"}
	if p, err := l.PagePath("/openwiki/a/b.md"); err != nil || p != filepath.FromSlash("/w/openwiki/a/b.md") {
		t.Errorf("PagePath = %s, %v", p, err)
	}
}

func TestManifestRoundTripAndValidation(t *testing.T) {
	l := Layout{WikiRoot: t.TempDir()}
	m, err := l.LoadManifest()
	if err != nil || len(m.Pages) != 0 {
		t.Fatalf("missing manifest: %+v, %v", m, err)
	}
	m.Pages["/openwiki/quickstart.md"] = ManifestEntry{PageVersion: "sha256:ab", GitHead: "h"}
	if err := l.SaveManifest(m); err != nil {
		t.Fatal(err)
	}
	got, err := l.LoadManifest()
	if err != nil || got.Pages["/openwiki/quickstart.md"].GitHead != "h" {
		t.Fatalf("round trip: %+v, %v", got, err)
	}

	for _, content := range []string{
		`{"schemaVersion": 1, "pages": {"/elsewhere/x.md": {"pageVersion": "v"}}}`,
		`{"schemaVersion": 1, "pages": {"/openwiki/x.md": {}}}`,
		`{"schemaVersion": 9, "pages": {}}`,
	} {
		if err := WriteFileAtomic(l.ManifestPath(), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := l.LoadManifest(); !errors.Is(err, ErrInvalidState) {
			t.Errorf("manifest %s: want ErrInvalidState, got %v", content, err)
		}
	}
}

func TestReadsUpstreamManifestFields(t *testing.T) {
	l := Layout{WikiRoot: t.TempDir()}
	upstream := `{
  "schemaVersion": 1,
  "pages": {
    "/openwiki/quickstart.md": {
      "pageVersion": "sha256:9814",
      "completedBy": "openwiki/0.6.1",
      "completedRunId": "2254b53e",
      "gitHead": "fab24e7",
      "sourceFingerprint": "sha256:3938",
      "futureField": true
    }
  }
}`
	if err := WriteFileAtomic(l.ManifestPath(), []byte(upstream), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := l.LoadManifest()
	if err != nil {
		t.Fatal(err)
	}
	want := ManifestEntry{PageVersion: "sha256:9814", CompletedBy: "openwiki/0.6.1", CompletedRunID: "2254b53e", GitHead: "fab24e7", SourceFingerprint: "sha256:3938"}
	if m.Pages["/openwiki/quickstart.md"] != want {
		t.Errorf("got %+v", m.Pages["/openwiki/quickstart.md"])
	}
}

func TestLastUpdate(t *testing.T) {
	l := Layout{WikiRoot: t.TempDir()}
	if u, err := l.LoadLastUpdate(); u != nil || err != nil {
		t.Fatalf("missing: %+v, %v", u, err)
	}
	if err := WriteFileAtomic(l.LastUpdatePath(), []byte(`{"updatedAt":"2026-09-30T08:43:24.089Z","command":"update","model":"m"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := l.LoadLastUpdate()
	if err != nil || u.Status != StatusComplete {
		t.Fatalf("legacy record should default to complete: %+v, %v", u, err)
	}
	if err := l.SaveLastUpdate(LastUpdate{UpdatedAt: "t", Command: "deploy", Model: "m", Status: StatusComplete}); err == nil {
		t.Error("invalid command should be refused on save")
	}
	if err := WriteFileAtomic(l.LastUpdatePath(), []byte(`{"updatedAt":"t","command":"init","model":"m","status":"weird"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := l.LoadLastUpdate(); !errors.Is(err, ErrInvalidState) {
		t.Errorf("want ErrInvalidState, got %v", err)
	}
}

func TestWriteFileAtomicLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "state.json")
	for i := 0; i < 3; i++ {
		if err := WriteJSONAtomic(path, map[string]int{"n": i}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("expected only the target file, found %d entries", len(entries))
	}
	var v map[string]int
	if found, err := ReadJSON(path, &v); !found || err != nil || v["n"] != 2 {
		t.Errorf("ReadJSON = %v, %v, %v", v, found, err)
	}
	if err := RemoveIfExists(path); err != nil {
		t.Fatal(err)
	}
	if err := RemoveIfExists(path); err != nil {
		t.Error("removing a missing file should succeed")
	}
}

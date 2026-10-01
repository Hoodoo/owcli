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

	l, err := d.Bind(sub, External, "", now)
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
	if _, err := d.Bind(repo, External, "", now); err != nil {
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
	a, err := d.Bind(repo, External, "", now)
	if err != nil {
		t.Fatal(err)
	}
	b, err := d.Bind(repo, External, "", now.Add(time.Hour))
	if err != nil || a != b {
		t.Fatalf("rebind changed layout: %+v vs %+v (%v)", a, b, err)
	}
	if _, err := d.Bind(repo, InRepo, "", now); err == nil {
		t.Fatal("switching kinds without unbind should fail")
	}
}

func TestUnbind(t *testing.T) {
	repo := gitRepo(t)
	d := testDirs(t)
	l, err := d.Bind(repo, External, "", now)
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

	if _, err := d.Bind(repo, External, "", now); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Unbind(repo, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(l.WikiRoot)); !os.IsNotExist(err) {
		t.Error("purge should delete the external wiki")
	}

	if _, err := d.Bind(repo, InRepo, "", now); err != nil {
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

func TestCustomWikiDir(t *testing.T) {
	repo := gitRepo(t)
	d := testDirs(t)
	kb := filepath.Join(t.TempDir(), "kb", "projects", "foo")
	l, err := d.Bind(repo, External, kb, now)
	if err != nil {
		t.Fatal(err)
	}
	realKB, _ := filepath.EvalSymlinks(kb)
	if !l.CustomHome || l.Home != realKB || l.WikiRoot != filepath.Join(realKB, WikiDirName) {
		t.Fatalf("layout %+v", l)
	}
	if got, err := d.Resolve(repo); err != nil || got != l {
		t.Fatalf("resolve %+v %v", got, err)
	}
	if _, err := d.Bind(repo, External, t.TempDir(), now); err == nil {
		t.Error("rebinding to another directory must be refused")
	}
	if _, err := d.Unbind(repo, true); err == nil || !strings.Contains(err.Error(), "delete it yourself") {
		t.Errorf("purging a chosen directory must be refused: %v", err)
	}
	if _, err := d.Unbind(repo, false); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Bind(repo, External, filepath.Join(repo, "docs", "wiki"), now); err == nil || !strings.Contains(err.Error(), "inside the repository") {
		t.Errorf("a wiki dir inside the repository must be refused: %v", err)
	}
	if _, err := d.Bind(repo, InRepo, kb, now); err == nil {
		t.Error("a wiki dir only applies to external bindings")
	}
}

func TestListBindingsAndReattachStaleWiki(t *testing.T) {
	d := testDirs(t)
	oldRepo := gitRepo(t)
	oldLayout, err := d.Bind(oldRepo, External, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := oldLayout.SaveLastUpdate(LastUpdate{UpdatedAt: now.Format(time.RFC3339), Command: "init", GitHead: "abcdef", Model: "test", Status: StatusComplete}); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(d.Data, "wikis", "orphan")
	if err := os.MkdirAll(orphan, 0o755); err != nil {
		t.Fatal(err)
	}
	inv, err := d.ListBindings()
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Bindings) != 1 || !inv.Bindings[0].RepoExists || inv.Bindings[0].LastUpdate.GitHead != "abcdef" {
		t.Fatalf("inventory: %+v", inv)
	}
	if len(inv.Orphans) != 1 || inv.Orphans[0] != orphan {
		t.Fatalf("orphans: %v", inv.Orphans)
	}

	newRepo := gitRepo(t)
	if _, err := d.Bind(newRepo, External, oldLayout.Home, now); err == nil || !strings.Contains(err.Error(), "already bound") {
		t.Fatalf("must not steal live binding: %v", err)
	}
	if err := os.RemoveAll(oldRepo); err != nil {
		t.Fatal(err)
	}
	newLayout, err := d.Bind(newRepo, External, oldLayout.Home, now)
	if err != nil {
		t.Fatal(err)
	}
	if newLayout.Home != oldLayout.Home {
		t.Fatalf("reattached home %s, want %s", newLayout.Home, oldLayout.Home)
	}
	inv, err = d.ListBindings()
	if err != nil || len(inv.Bindings) != 1 || inv.Bindings[0].RepoRoot != newRepo || len(inv.Orphans) != 1 || inv.Orphans[0] != orphan {
		t.Fatalf("relocated inventory: %+v, %v", inv, err)
	}
}

func TestCommitWikiOwnRepository(t *testing.T) {
	repo := gitRepo(t)
	d := testDirs(t)
	l, err := d.Bind(repo, External, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(l.WikiRoot, "quickstart.md"), []byte("# Q\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.RunPath(), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha, err := l.CommitWiki("owcli init complete", "Source commit: abc")
	if err != nil || sha == "" {
		t.Fatalf("commit: %q %v", sha, err)
	}
	files := git(t, l.Home, "ls-files")
	if !strings.Contains(files, "openwiki/quickstart.md") || strings.Contains(files, ".run.json") {
		t.Errorf("tracked files:\n%s", files)
	}
	if msg := git(t, l.Home, "log", "-1", "--format=%B"); !strings.Contains(msg, "owcli init complete") || !strings.Contains(msg, "Source commit: abc") {
		t.Errorf("message %q", msg)
	}
	if sha, err := l.CommitWiki("again", ""); err != nil || sha != "" {
		t.Errorf("nothing changed: %q %v", sha, err)
	}
	if status := git(t, repo, "status", "--porcelain", "--ignored"); status != "" {
		t.Errorf("explored repository changed:\n%s", status)
	}
	if sha, err := (Layout{Kind: InRepo, Home: repo}).CommitWiki("x", ""); err != nil || sha != "" {
		t.Error("in-repo wikis are never committed by owcli")
	}
}

func TestCommitWikiInSharedRepository(t *testing.T) {
	repo := gitRepo(t)
	kb := gitRepo(t) // a knowledge-base repository with unrelated work in progress
	if err := os.WriteFile(filepath.Join(kb, "notes.txt"), []byte("draft\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, kb, "add", "notes.txt")
	d := testDirs(t)
	l, err := d.Bind(repo, External, filepath.Join(kb, "projects", "foo"), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.EnsureWikiRepo(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(l.Home, ".git")); !os.IsNotExist(err) {
		t.Fatal("a wiki inside an existing repository must not get its own repository")
	}
	if err := os.WriteFile(filepath.Join(l.WikiRoot, "quickstart.md"), []byte("# Q\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := l.CommitWiki("owcli update", ""); err != nil {
		t.Fatal(err)
	}
	committed := git(t, kb, "show", "--name-only", "--format=", "HEAD")
	if !strings.Contains(committed, "projects/foo/openwiki/quickstart.md") || strings.Contains(committed, "notes.txt") {
		t.Errorf("commit touched:\n%s", committed)
	}
	if staged := git(t, kb, "diff", "--cached", "--name-only"); strings.TrimSpace(staged) != "notes.txt" {
		t.Errorf("the user's staged work must stay staged: %q", staged)
	}
}

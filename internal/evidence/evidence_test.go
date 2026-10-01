package evidence

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"owcli/internal/ignore"
)

func TestParseAndFormat(t *testing.T) {
	good := map[string]string{
		"repo://src/a.go":              "repo://src/a.go",
		"repo://src/a.go#L8":           "repo://src/a.go#L8-L8",
		"repo://src/a.go#L3-L9":        "repo://src/a.go#L3-L9",
		"repo://./src//x/../a.go":      "repo://src/a.go",
		`repo://src\win.go`:            "repo://src/win.go",
		"repo://dir/with%20space.md":   "repo://dir/with%20space.md",
		"repo://dir/with space.md":     "repo://dir/with%20space.md",
		"repo://ünï/çödé.go":           "repo://%C3%BCn%C3%AF/%C3%A7%C3%B6d%C3%A9.go",
		"repo://a/it's(1)!~*.go":       "repo://a/it's(1)!~*.go",
		"repo://a/50%25.txt":           "repo://a/50%25.txt",
		"repo://a/q%3Fhash%23.txt#L1":  "repo://a/q%3Fhash%23.txt#L1-L1",
		"repo://.github/workflows/x.y": "repo://.github/workflows/x.y",
	}
	for in, want := range good {
		got, err := Canonical(in)
		if err != nil || got != want {
			t.Errorf("Canonical(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := []string{
		"src/a.go", "file:///etc/passwd", "repo://", "repo://.", "repo://../x", "repo://a/../../x",
		"repo:///etc/passwd", "repo://C:/x", "repo://a#L1#L2", "repo://a#L0", "repo://a#L5-L3",
		"repo://a#10-20", "repo://a#L1-20", "repo://a%ZZ", "repo://a%FF", "repo://a%0Ab",
		"repo://.git/config", "repo://.GIT", "repo://openwiki/index.md", "repo://OpenWiki",
		"repo://a#L99999999999999999999",
	}
	for _, in := range bad {
		if _, err := Parse(in); !errors.Is(err, ErrInvalidResource) {
			t.Errorf("Parse(%q) err = %v, want ErrInvalidResource", in, err)
		}
	}
	if r, _ := Parse("repo://a.go#L2-L4"); r.WholeFile().String() != "repo://a.go" {
		t.Error("WholeFile should drop the range")
	}
}

func writeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for p, c := range files {
		abs := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func newResolver(t *testing.T, root string, rules string) *RepoResolver {
	t.Helper()
	r, err := NewRepoResolver(root, ignore.Parse(rules))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func mustResolve(t *testing.T, r Resolver, resource, prev string) *Resolved {
	t.Helper()
	res, err := r.Resolve(resource, prev)
	if err != nil {
		t.Fatalf("Resolve(%s): %v", resource, err)
	}
	return res
}

func TestWholeFile(t *testing.T) {
	root := writeRepo(t, map[string]string{"a.go": "hello\n"})
	r := newResolver(t, root, "")
	res := mustResolve(t, r, "repo://./a.go", "")
	// sha256("hello\n")
	want := FileVersionPrefix + "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03"
	if res.Evidence.Version != want || res.Evidence.Resource != "repo://a.go" || res.Content != "hello\n" {
		t.Fatalf("got %+v", res.Evidence)
	}
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("bye\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if mustResolve(t, r, "repo://a.go", res.Evidence.Version).Evidence.Version == want {
		t.Error("changed content must change the version")
	}
}

func TestMissingAndNonFiles(t *testing.T) {
	root := writeRepo(t, map[string]string{"dir/a.go": "x\n", "file.txt": "y\n"})
	r := newResolver(t, root, "")
	for _, res := range []string{"repo://missing.go", "repo://dir", "repo://file.txt/child", "repo://dir/a.go#L2-L3"} {
		if got := mustResolve(t, r, res, ""); got != nil {
			t.Errorf("%s should be unresolved, got %+v", res, got.Evidence)
		}
	}
}

func TestIgnoredEvidenceIsInvalid(t *testing.T) {
	root := writeRepo(t, map[string]string{"secret/key.txt": "k\n"})
	r := newResolver(t, root, "secret/")
	if _, err := r.Resolve("repo://secret/key.txt", ""); !errors.Is(err, ErrInvalidResource) {
		t.Fatalf("want ErrInvalidResource, got %v", err)
	}
}

func TestContainment(t *testing.T) {
	outside := writeRepo(t, map[string]string{"target.txt": "outside\n"})
	root := writeRepo(t, map[string]string{"real/inner.txt": "inner\n"})
	if err := os.Symlink(filepath.Join(outside, "target.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	r := newResolver(t, root, "")
	for _, res := range []string{"repo://link.txt", "repo://alias/inner.txt"} {
		if _, err := r.Resolve(res, ""); !errors.Is(err, ErrSecurity) {
			t.Errorf("%s: want ErrSecurity, got %v", res, err)
		}
	}
	if mustResolve(t, r, "repo://real/inner.txt", "") == nil {
		t.Error("real path should resolve")
	}

	// A repository root reached through a symlink still works.
	linkedRoot := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(root, linkedRoot); err != nil {
		t.Fatal(err)
	}
	if mustResolve(t, newResolver(t, linkedRoot, ""), "repo://real/inner.txt", "") == nil {
		t.Error("symlinked root should resolve")
	}
	if _, err := NewRepoResolver("relative", nil); !errors.Is(err, ErrInvalidResource) {
		t.Error("relative root must be refused")
	}
}

const source = `package x

import "fmt"

// Greet says hello.
func Greet(name string) {
	fmt.Println("hello", name)
}

func Other() {}
`

func TestLineRangeStableUnderMoves(t *testing.T) {
	root := writeRepo(t, map[string]string{"x.go": source})
	r := newResolver(t, root, "")
	first := mustResolve(t, r, "repo://x.go#L5-L8", "")
	if !strings.HasPrefix(first.Evidence.Version, LinesVersionPrefix) {
		t.Fatalf("version %s", first.Evidence.Version)
	}
	if first.Content != "// Greet says hello.\nfunc Greet(name string) {\n\tfmt.Println(\"hello\", name)\n}\n" {
		t.Fatalf("content %q", first.Content)
	}

	// Insert lines above: same text moves down by two lines.
	moved := strings.Replace(source, "import \"fmt\"\n", "import \"fmt\"\n\nvar _ = 1\n", 1)
	if err := os.WriteFile(filepath.Join(root, "x.go"), []byte(moved), 0o644); err != nil {
		t.Fatal(err)
	}
	got := mustResolve(t, r, first.Evidence.Resource, first.Evidence.Version)
	if got.Evidence.Version != first.Evidence.Version {
		t.Error("moved but unchanged text must keep its version")
	}
	if got.Evidence.Resource != "repo://x.go#L7-L10" {
		t.Errorf("relocated resource = %s", got.Evidence.Resource)
	}

	// A previous version from another scheme is ignored: plain hint lookup.
	plain := mustResolve(t, r, "repo://x.go#L7-L10", "repo-file-v1:sha256:abc")
	// Same text, so the same content hash; the anchors describe new context.
	contentHash := func(v string) string { return strings.SplitN(v, ":", 4)[2] }
	if plain.Content != first.Content || contentHash(plain.Evidence.Version) != contentHash(first.Evidence.Version) {
		t.Error("unparseable previous version should fall back to the hinted span")
	}
}

func TestLineRangeEditedInPlace(t *testing.T) {
	root := writeRepo(t, map[string]string{"x.go": source})
	r := newResolver(t, root, "")
	first := mustResolve(t, r, "repo://x.go#L5-L8", "")

	// Edit inside the range and add a line: the region between the surviving
	// preceding and following context is the new range.
	edited := strings.Replace(source, "\tfmt.Println(\"hello\", name)\n", "\tfmt.Println(\"hi\", name)\n\treturn\n", 1)
	if err := os.WriteFile(filepath.Join(root, "x.go"), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	changed := mustResolve(t, r, first.Evidence.Resource, first.Evidence.Version)
	if changed == nil || changed.Evidence.Version == first.Evidence.Version {
		t.Fatal("edited range should resolve with a new version")
	}
	if changed.Evidence.Resource != "repo://x.go#L5-L9" || !strings.Contains(changed.Content, "return") {
		t.Errorf("changed = %+v", changed.Evidence)
	}
}

func TestLineRangeUnresolvedWhenGone(t *testing.T) {
	root := writeRepo(t, map[string]string{"x.go": source})
	r := newResolver(t, root, "")
	first := mustResolve(t, r, "repo://x.go#L5-L8", "")
	if err := os.WriteFile(filepath.Join(root, "x.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := mustResolve(t, r, first.Evidence.Resource, first.Evidence.Version); got != nil {
		t.Errorf("deleted range should be unresolved, got %+v", got.Evidence)
	}
}

func TestLineRangeAmbiguousDuplicates(t *testing.T) {
	block := "a\nb\n"
	root := writeRepo(t, map[string]string{"x.txt": "1\n" + block + "2\n"})
	r := newResolver(t, root, "")
	first := mustResolve(t, r, "repo://x.txt#L2-L3", "")
	// Two copies of the block with identical context: cannot relocate.
	dup := "0\n1\n" + block + "2\n" + "1\n" + block + "2\n"
	if err := os.WriteFile(filepath.Join(root, "x.txt"), []byte(dup), 0o644); err != nil {
		t.Fatal(err)
	}
	got := mustResolve(t, r, "repo://x.txt#L9-L10", first.Evidence.Version)
	if got != nil {
		t.Errorf("ambiguous relocation should be unresolved, got %+v", got.Evidence)
	}
	// One copy whose context matches is preferred.
	one := "x\n" + block + "y\n" + "1\n" + block + "2\n"
	if err := os.WriteFile(filepath.Join(root, "x.txt"), []byte(one), 0o644); err != nil {
		t.Fatal(err)
	}
	got = mustResolve(t, r, "repo://x.txt#L9-L10", first.Evidence.Version)
	if got == nil || got.Evidence.Resource != "repo://x.txt#L6-L7" || got.Evidence.Version != first.Evidence.Version {
		t.Errorf("context should disambiguate, got %+v", got)
	}
}

func TestSplitLines(t *testing.T) {
	cases := map[string]int{"": 0, "a": 1, "a\n": 1, "a\nb": 2, "a\r\nb\n": 2, "\n\n": 2}
	for in, n := range cases {
		if got := len(splitLines(in)); got != n {
			t.Errorf("splitLines(%q) = %d lines, want %d", in, got, n)
		}
	}
}

type countingResolver struct{ calls int }

func (c *countingResolver) Resolve(string, string) (*Resolved, error) {
	c.calls++
	return nil, nil
}

func TestCached(t *testing.T) {
	inner := &countingResolver{}
	c := Cached(inner)
	for i := 0; i < 3; i++ {
		_, _ = c.Resolve("repo://a", "v1")
	}
	_, _ = c.Resolve("repo://a", "v2")
	if inner.calls != 2 {
		t.Errorf("calls = %d, want 2", inner.calls)
	}
	if _, _ = Cached(inner).Resolve("repo://a", "v1"); inner.calls != 3 {
		t.Error("a fresh cache must not share results")
	}
}

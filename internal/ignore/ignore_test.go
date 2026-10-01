package ignore

import (
	"os"
	"path/filepath"
	"testing"
)

type probe struct {
	path  string
	isDir bool
	want  bool
}

func TestIgnores(t *testing.T) {
	tests := []struct {
		name   string
		rules  string
		probes []probe
	}{
		{
			name:  "unanchored name matches at any depth and below",
			rules: "secrets",
			probes: []probe{
				{"secrets", false, true},
				{"a/b/secrets", true, true},
				{"a/secrets/key.pem", false, true},
				{"secretsfile", false, false},
				{"mysecrets", false, false},
			},
		},
		{
			name:  "leading slash anchors to root",
			rules: "/build",
			probes: []probe{
				{"build", true, true},
				{"build/out.o", false, true},
				{"src/build", true, false},
			},
		},
		{
			name:  "inner slash anchors to root",
			rules: "docs/internal",
			probes: []probe{
				{"docs/internal/a.md", false, true},
				{"x/docs/internal/a.md", false, false},
			},
		},
		{
			name:  "trailing slash is directory-only",
			rules: "cache/",
			probes: []probe{
				{"cache", true, true},
				{"cache", false, false},
				{"a/cache", true, true},
				{"a/cache/x.bin", false, true},
				{"a/cache", false, false},
			},
		},
		{
			name:  "single star stays within a segment",
			rules: "*.log\n/gen/*.go",
			probes: []probe{
				{"app.log", false, true},
				{"a/b/app.log", false, true},
				{"gen/x.go", false, true},
				{"gen/sub/x.go", false, false},
			},
		},
		{
			name:  "question mark is one non-slash character",
			rules: "file?.txt",
			probes: []probe{
				{"file1.txt", false, true},
				{"file12.txt", false, false},
				{"file/.txt", false, false},
			},
		},
		{
			name:  "double star prefix spans zero or more directories",
			rules: "**/fixtures/*.json",
			probes: []probe{
				{"fixtures/a.json", false, true},
				{"x/y/fixtures/a.json", false, true},
				{"x/fixtures/sub/a.json", false, false},
			},
		},
		{
			name:  "trailing double star matches everything below",
			rules: "vendor/**",
			probes: []probe{
				{"vendor/a/b/c.go", false, true},
				{"vendor", true, false},
			},
		},
		{
			name:  "last match wins and negation can re-include below an ignored dir",
			rules: "dist/\n!dist/keep.txt",
			probes: []probe{
				{"dist/drop.txt", false, true},
				{"dist/keep.txt", false, false},
			},
		},
		{
			name:  "order matters: later ignore overrides earlier negation",
			rules: "!important.md\n*.md",
			probes: []probe{
				{"important.md", false, true},
			},
		},
		{
			name:  "matching is case-insensitive",
			rules: "README.MD",
			probes: []probe{
				{"readme.md", false, true},
				{"Docs/ReadMe.md", false, true},
			},
		},
		{
			name:  "comments, blanks, dot-slash, and backslashes",
			rules: "# comment\n\n  ./tmp  \nwin\\path",
			probes: []probe{
				{"tmp/x", false, true},
				{"win/path", false, true},
				{"# comment", false, false},
			},
		},
		{
			name:  "regex metacharacters are literal",
			rules: "a+b(c).txt",
			probes: []probe{
				{"a+b(c).txt", false, true},
				{"aab(c).txt", false, false},
			},
		},
		{
			name:  ".git is always excluded and cannot be re-included",
			rules: "!.git",
			probes: []probe{
				{".git", true, true},
				{".git/config", false, true},
				{".github/workflows/ci.yml", false, false},
			},
		},
		{
			name:  "paths are normalized before matching",
			rules: "/build",
			probes: []probe{
				{"/build/", true, true},
				{"./build/x", false, true},
				{`build\x`, false, true},
				{"src/../build/x", false, true},
				{"", true, false},
				{"/", true, false},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Parse(tt.rules)
			for _, p := range tt.probes {
				if got := m.Ignores(p.path, p.isDir); got != p.want {
					t.Errorf("Ignores(%q, dir=%v) = %v, want %v", p.path, p.isDir, got, p.want)
				}
			}
		})
	}
}

func TestCompileRejectsEmptyPatterns(t *testing.T) {
	for _, p := range []string{"/", "!", "./", "//", "!/"} {
		if _, ok := Compile(p); ok {
			t.Errorf("Compile(%q) accepted an empty pattern", p)
		}
	}
	if m := Parse("/\n!\n"); m.Active() {
		t.Error("matcher with only empty patterns should be inactive")
	}
}

func TestExcludeIsFixedAndCopies(t *testing.T) {
	base := Parse("!openwiki/keep.md")
	m := base.Exclude("openwiki/")
	if !m.Ignores("openwiki/keep.md", false) {
		t.Error("fixed exclusion must win over negation")
	}
	if !m.Ignores("OpenWiki", true) {
		t.Error("fixed exclusion should be case-insensitive")
	}
	if m.Ignores("openwikis/x", false) {
		t.Error("fixed exclusion must match whole segments")
	}
	if base.Ignores("openwiki/x.md", false) {
		t.Error("Exclude must not modify the receiver")
	}
}

func TestSkipDir(t *testing.T) {
	if !Parse("node_modules/").SkipDir("node_modules") {
		t.Error("ignored dir without negations should be prunable")
	}
	m := Parse("dist/\n!dist/keep.txt")
	if m.SkipDir("dist") {
		t.Error("dir with possible re-includes must not be pruned")
	}
	if !m.SkipDir(".git") {
		t.Error("fixed exclusions are always prunable")
	}
	if m.SkipDir("src") {
		t.Error("non-ignored dir must not be pruned")
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	m, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Active() || m.Ignores("x", false) {
		t.Error("missing file should give an inactive matcher")
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("*.secret\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err = Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Ignores("k.secret", false) {
		t.Error("CRLF rule file not honored")
	}
}

package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runCLI(args ...string) (string, error) {
	cmd := NewRootCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestCommandsRegistered(t *testing.T) {
	want := []string{"bind", "bindings", "unbind", "init", "update", "status", "check", "search", "read", "workspace", "wikis", "serve"}
	cmd := NewRootCommand()
	for _, name := range want {
		if c, _, err := cmd.Find([]string{name}); err != nil || c.Name() != name {
			t.Errorf("command %q not registered", name)
		}
	}
}

func TestGenerateValidatesConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, err := runCLI("init", "--provider", "bogus")
	if err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("want provider error, got %v", err)
	}
}

func TestVersionFlag(t *testing.T) {
	out, err := runCLI("--version")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "owcli version") {
		t.Fatalf("unexpected version output %q", out)
	}
}

func TestBindExternalAndUnbind(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(base, "data"))
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}

	out, err := runCLI("bind", "--external", repo)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "(external)") || !strings.Contains(out, filepath.Join(base, "data")) {
		t.Fatalf("unexpected bind output %q", out)
	}
	if out, err = runCLI("unbind", "--purge", repo); err != nil || !strings.Contains(out, "deleted") {
		t.Fatalf("unbind: %q, %v", out, err)
	}
}

func TestBindingsCommand(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(base, "data"))
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if _, err := runCLI("bind", "--external", repo); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI("bindings")
	if err != nil || !strings.Contains(out, repo) || !strings.Contains(out, "last run: none") {
		t.Fatalf("bindings: %q, %v", out, err)
	}
	out, err = runCLI("bindings", "--json")
	if err != nil || !strings.Contains(out, `"repoRoot"`) || !strings.Contains(out, `"orphans"`) {
		t.Fatalf("bindings json: %q, %v", out, err)
	}
}

// TestOwcliHome: with OWCLI_HOME set, the binding registry and external
// wikis live there, and nothing is written under the XDG directories.
func TestOwcliHome(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(base, "data"))
	home := filepath.Join(base, "owcli-home")
	t.Setenv("OWCLI_HOME", home)
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}

	if _, err := runCLI("bind", "--external", repo); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "bindings.json")); err != nil {
		t.Errorf("bindings.json not in OWCLI_HOME: %v", err)
	}
	if wikis, _ := filepath.Glob(filepath.Join(home, "wikis", "*", "openwiki")); len(wikis) != 1 {
		t.Errorf("want one external wiki under OWCLI_HOME/wikis, got %v", wikis)
	}
	for _, dir := range []string{"config", "data"} {
		if _, err := os.Stat(filepath.Join(base, dir)); !os.IsNotExist(err) {
			t.Errorf("%s written under XDG despite OWCLI_HOME: %v", dir, err)
		}
	}
}

// TestRelocateCommand: relocate rewrites a moved repository's binding, and
// --json reports the change.
func TestRelocateCommand(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(base, "data"))
	src, _ := filepath.EvalSymlinks(t.TempDir())
	repo := filepath.Join(src, "old")
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if _, err := runCLI("bind", "--external", repo); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(src, "new")
	if err := os.Rename(repo, moved); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI("relocate", "--json", repo, moved)
	if err != nil || !strings.Contains(out, `"to": "`+moved+`"`) || !strings.Contains(out, `"exists": true`) {
		t.Fatalf("relocate: %q, %v", out, err)
	}
	if out, err = runCLI("bindings"); err != nil || !strings.Contains(out, moved) || strings.Contains(out, repo+"\n") {
		t.Fatalf("bindings after relocate: %q, %v", out, err)
	}
	if out, err = runCLI("relocate", repo, moved); err != nil || !strings.Contains(out, "nothing registered") {
		t.Fatalf("second relocate: %q, %v", out, err)
	}
}

func runCLIIn(input string, args ...string) (string, error) {
	cmd := NewRootCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(input))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// TestInitWithoutAPIKey: owcli init needs a key for its own model calls; without
// one it points to the agent-driven path and leaves nothing behind.
func TestInitWithoutAPIKey(t *testing.T) {
	repo := cliRepo(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	for _, mode := range []string{"init", "update"} {
		_, err := runCLI(mode)
		if err == nil {
			t.Fatalf("%s without a key must fail", mode)
		}
		for _, want := range []string{"needs an API key", "owcli agents-md", "owcli run begin " + mode, "owcli quickstart"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s error lacks %q: %v", mode, want, err)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "owcli", "bindings.json")); !os.IsNotExist(err) {
		t.Errorf("a failed init must not bind the repository: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "openwiki")); !os.IsNotExist(err) {
		t.Errorf("a failed init must not create a wiki: %v", err)
	}
}

// TestModelFlagsOnlyOnGenerate: commands that never call a model do not offer
// model settings.
func TestModelFlagsOnlyOnGenerate(t *testing.T) {
	root := NewRootCommand()
	for _, name := range []string{"init", "update"} {
		c, _, _ := root.Find([]string{name})
		for _, flag := range []string{"provider", "model", "base-url", "api-key-env", "effort", "config"} {
			if c.Flags().Lookup(flag) == nil {
				t.Errorf("%s lacks --%s", name, flag)
			}
		}
	}
	for _, name := range []string{"search", "serve", "status", "wikis", "run"} {
		c, _, _ := root.Find([]string{name})
		if c.Flags().Lookup("api-key-env") != nil || c.InheritedFlags().Lookup("api-key-env") != nil {
			t.Errorf("%s offers --api-key-env", name)
		}
	}
	if !strings.Contains(root.Long, "no API key") {
		t.Error("root help must describe the agent-driven way")
	}
}

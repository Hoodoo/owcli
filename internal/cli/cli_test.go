package cli

import (
	"bytes"
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
	want := []string{"bind", "unbind", "init", "update", "status", "check", "search", "read"}
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

package cli

import (
	"bytes"
	"strings"
	"testing"
)

func run(args ...string) (string, error) {
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

func TestStubsNameTheirIssue(t *testing.T) {
	_, err := run("search", "retry handling")
	if err == nil || !strings.Contains(err.Error(), "t1k2") {
		t.Fatalf("want not-implemented error naming t1k2, got %v", err)
	}
}

func TestGenerateValidatesConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, err := run("init", "--provider", "bogus")
	if err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("want provider error, got %v", err)
	}
}

func TestVersionFlag(t *testing.T) {
	out, err := run("--version")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "owcli version") {
		t.Fatalf("unexpected version output %q", out)
	}
}

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadPrecedence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	writeFile(t, path, "provider = \"openai\"\nmodel = \"from-file\"\nbase_url = \"http://file\"\n")
	t.Setenv("OWCLI_MODEL", "from-env")
	t.Setenv("OWCLI_BASE_URL", "")
	t.Setenv("OWCLI_PROVIDER", "")
	t.Setenv("OWCLI_API_KEY_ENV", "")

	got, err := Load(path, Config{BaseURL: "http://flag"})
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Provider:  ProviderOpenAI,
		Model:     "from-env",
		BaseURL:   "http://flag",
		APIKeyEnv: Defaults().APIKeyEnv,
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestLoadMissingFileUsesDefaults(t *testing.T) {
	for _, k := range []string{"OWCLI_PROVIDER", "OWCLI_MODEL", "OWCLI_BASE_URL", "OWCLI_API_KEY_ENV"} {
		t.Setenv(k, "")
	}
	got, err := Load(filepath.Join(t.TempDir(), "absent.toml"), Config{})
	if err != nil {
		t.Fatal(err)
	}
	if got != Defaults() {
		t.Fatalf("got %+v, want defaults", got)
	}
}

func TestLoadFileRejectsUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, path, "modle = \"typo\"\n")
	_, err := LoadFile(path)
	if err == nil || !strings.Contains(err.Error(), "modle") {
		t.Fatalf("want unknown-key error, got %v", err)
	}
}

func TestValidate(t *testing.T) {
	if err := (Config{Provider: "bogus", Model: "m"}).Validate(); err == nil {
		t.Fatal("want error for unknown provider")
	}
	if err := (Config{Provider: ProviderOpenAI}).Validate(); err == nil {
		t.Fatal("want error for empty model")
	}
}

func TestDirHonorsXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/x")
	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != "/x/owcli" {
		t.Fatalf("got %s", dir)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

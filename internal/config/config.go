// Package config resolves owcli settings from defaults, the config file,
// OWCLI_* environment variables, and command-line flags, in increasing
// precedence.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Provider names accepted by Config.Provider.
const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai" // any OpenAI-compatible Chat Completions endpoint
)

// Config holds model settings. Fields left empty fall through to the next
// lower-precedence source.
type Config struct {
	Provider  string `toml:"provider"`
	Model     string `toml:"model"`
	BaseURL   string `toml:"base_url"`
	APIKeyEnv string `toml:"api_key_env"` // name of the env var holding the API key
}

// Defaults returns the settings used when nothing else is configured.
func Defaults() Config {
	return Config{
		Provider:  ProviderAnthropic,
		Model:     "claude-sonnet-5-5",
		APIKeyEnv: "ANTHROPIC_API_KEY",
	}
}

// Merge returns c with every non-empty field of over applied on top.
func (c Config) Merge(over Config) Config {
	if over.Provider != "" {
		c.Provider = over.Provider
	}
	if over.Model != "" {
		c.Model = over.Model
	}
	if over.BaseURL != "" {
		c.BaseURL = over.BaseURL
	}
	if over.APIKeyEnv != "" {
		c.APIKeyEnv = over.APIKeyEnv
	}
	return c
}

// Validate reports settings that cannot work.
func (c Config) Validate() error {
	switch c.Provider {
	case ProviderAnthropic, ProviderOpenAI:
	default:
		return fmt.Errorf("unknown provider %q (want %q or %q)", c.Provider, ProviderAnthropic, ProviderOpenAI)
	}
	if c.Model == "" {
		return errors.New("model is required")
	}
	return nil
}

// Dir returns owcli's configuration directory ($XDG_CONFIG_HOME/owcli).
func Dir() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "owcli"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "owcli"), nil
}

// LoadFile reads a TOML config file. A missing file yields an empty Config.
func LoadFile(path string) (Config, error) {
	var c Config
	md, err := toml.DecodeFile(path, &c)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return Config{}, fmt.Errorf("read config %s: unknown key %q", path, undecoded[0].String())
	}
	return c, nil
}

// FromEnv reads OWCLI_PROVIDER, OWCLI_MODEL, OWCLI_BASE_URL and
// OWCLI_API_KEY_ENV through getenv.
func FromEnv(getenv func(string) string) Config {
	return Config{
		Provider:  getenv("OWCLI_PROVIDER"),
		Model:     getenv("OWCLI_MODEL"),
		BaseURL:   getenv("OWCLI_BASE_URL"),
		APIKeyEnv: getenv("OWCLI_API_KEY_ENV"),
	}
}

// Load resolves the effective config: defaults < file < env < flags.
// An empty path uses $XDG_CONFIG_HOME/owcli/config.toml.
func Load(path string, flags Config) (Config, error) {
	if path == "" {
		dir, err := Dir()
		if err != nil {
			return Config{}, err
		}
		path = filepath.Join(dir, "config.toml")
	}
	file, err := LoadFile(path)
	if err != nil {
		return Config{}, err
	}
	c := Defaults().Merge(file).Merge(FromEnv(os.Getenv)).Merge(flags)
	return c, c.Validate()
}

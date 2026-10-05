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

	"github.com/Hoodoo/owcli/internal/store"
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
	// Effort is the Anthropic output_config.effort level (low, medium, high,
	// xhigh, max). Ignored by other providers.
	Effort string `toml:"effort"`
	// NoFallbacks disables Anthropic server-side refusal fallbacks (needed
	// for proxies and platforms that reject the beta).
	NoFallbacks bool `toml:"no_fallbacks"`
}

// Provider-specific defaults, applied by Resolve when a field is empty.
const (
	DefaultAnthropicModel  = "claude-opus-5-5"
	DefaultAnthropicKeyEnv = "ANTHROPIC_API_KEY"
	DefaultAnthropicURL    = "https://api.anthropic.com"
	DefaultAnthropicEffort = "high"
	DefaultOpenAIKeyEnv    = "OPENAI_API_KEY"
	DefaultOpenAIURL       = "https://api.openai.com/v1"
)

// Defaults returns the settings used when nothing else is configured.
func Defaults() Config {
	return Config{Provider: ProviderAnthropic}
}

// Resolve fills provider-specific defaults into empty fields.
func (c Config) Resolve() Config {
	switch c.Provider {
	case ProviderAnthropic:
		c = Config{Model: DefaultAnthropicModel, APIKeyEnv: DefaultAnthropicKeyEnv, BaseURL: DefaultAnthropicURL, Effort: DefaultAnthropicEffort}.Merge(c)
	case ProviderOpenAI:
		c = Config{APIKeyEnv: DefaultOpenAIKeyEnv, BaseURL: DefaultOpenAIURL}.Merge(c)
	}
	return c
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
	if over.Effort != "" {
		c.Effort = over.Effort
	}
	c.NoFallbacks = c.NoFallbacks || over.NoFallbacks
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
		return errors.New("model is required (set --model, OWCLI_MODEL, or model in config.toml)")
	}
	switch c.Effort {
	case "", "low", "medium", "high", "xhigh", "max":
	default:
		return fmt.Errorf("unknown effort %q (want low, medium, high, xhigh, or max)", c.Effort)
	}
	return nil
}

// Dir returns owcli's configuration directory: $OWCLI_HOME when set, else
// $XDG_CONFIG_HOME/owcli. It is the same directory that holds bindings.json.
func Dir() (string, error) {
	dirs, err := store.DefaultDirs()
	if err != nil {
		return "", err
	}
	return dirs.Config, nil
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

// FromEnv reads OWCLI_PROVIDER, OWCLI_MODEL, OWCLI_BASE_URL,
// OWCLI_API_KEY_ENV, and OWCLI_EFFORT through getenv.
func FromEnv(getenv func(string) string) Config {
	return Config{
		Provider:  getenv("OWCLI_PROVIDER"),
		Model:     getenv("OWCLI_MODEL"),
		BaseURL:   getenv("OWCLI_BASE_URL"),
		APIKeyEnv: getenv("OWCLI_API_KEY_ENV"),
		Effort:    getenv("OWCLI_EFFORT"),
	}
}

// Load resolves the effective config: defaults < file < env < flags, then
// provider-specific defaults for anything still empty.
// An empty path uses config.toml in Dir.
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
	c := Defaults().Merge(file).Merge(FromEnv(os.Getenv)).Merge(flags).Resolve()
	return c, c.Validate()
}

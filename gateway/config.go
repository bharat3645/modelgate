package gateway

import (
	"encoding/json"
	"fmt"
	"os"
)

// Provider is one backend modelgate can route to, in the order providers
// are tried (config order = fallback order).
type Provider struct {
	Name      string  `json:"name"`
	BaseURL   string  `json:"base_url"`
	APIKeyEnv string  `json:"api_key_env"`
	Pricing   Pricing `json:"pricing"`
	// ModelOverride, if set, replaces the client's "model" field before
	// forwarding to this provider - lets a client send one generic model
	// name while each configured provider serves it under its own naming
	// scheme (e.g. Together's "openai/gpt-oss-120b" vs Fireworks's
	// "accounts/fireworks/models/gpt-oss-120b" for the same open model).
	ModelOverride string `json:"model_override,omitempty"`
}

type AuditConfig struct {
	Path string `json:"path"`
}

type Config struct {
	Listen    string       `json:"listen"`
	Audit     AuditConfig  `json:"audit"`
	Providers []Provider   `json:"providers"`
	Timeout   TimeoutField `json:"timeout_seconds"`
}

// TimeoutField lets the config omit timeout_seconds (defaulting sensibly)
// while still rejecting a literal 0 or negative value if written explicitly
// - distinguishing "not set" from "set to zero" needs a pointer under
// encoding/json's normal unmarshaling, so this wraps that rather than
// exposing *int in the public struct.
type TimeoutField struct {
	set   bool
	value int
}

func (t *TimeoutField) UnmarshalJSON(data []byte) error {
	var v int
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	t.set = true
	t.value = v
	return nil
}

func (t TimeoutField) MarshalJSON() ([]byte, error) {
	if !t.set {
		return json.Marshal(DefaultTimeoutSeconds)
	}
	return json.Marshal(t.value)
}

// Seconds returns the configured timeout, or DefaultTimeoutSeconds if unset.
func (t TimeoutField) Seconds() int {
	if !t.set {
		return DefaultTimeoutSeconds
	}
	return t.value
}

// DefaultTimeoutSeconds is the per-provider request timeout when
// timeout_seconds is omitted from the config.
const DefaultTimeoutSeconds = 30

// LoadConfig reads and validates a modelgate config file. Unknown fields
// are rejected (DisallowUnknownFields) so a typo in a config key fails
// loudly at startup instead of being silently ignored.
func LoadConfig(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening config: %w", err)
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate checks structural and semantic correctness: required fields
// present, at least one provider, provider names unique, and (fail fast,
// not on the first proxied request) that every provider's api_key_env is
// actually set in the environment.
func (c *Config) Validate() error {
	if c.Listen == "" {
		return fmt.Errorf("config: listen is required")
	}
	if c.Audit.Path == "" {
		return fmt.Errorf("config: audit.path is required")
	}
	if len(c.Providers) == 0 {
		return fmt.Errorf("config: at least one provider is required")
	}
	if t := c.Timeout.Seconds(); t <= 0 {
		return fmt.Errorf("config: timeout_seconds must be positive, got %d", t)
	}
	seen := make(map[string]bool, len(c.Providers))
	for i, p := range c.Providers {
		if p.Name == "" {
			return fmt.Errorf("config: providers[%d].name is required", i)
		}
		if seen[p.Name] {
			return fmt.Errorf("config: duplicate provider name %q", p.Name)
		}
		seen[p.Name] = true
		if p.BaseURL == "" {
			return fmt.Errorf("config: providers[%d] (%s): base_url is required", i, p.Name)
		}
		if p.APIKeyEnv == "" {
			return fmt.Errorf("config: providers[%d] (%s): api_key_env is required", i, p.Name)
		}
		if os.Getenv(p.APIKeyEnv) == "" {
			return fmt.Errorf("config: providers[%d] (%s): environment variable %s is not set", i, p.Name, p.APIKeyEnv)
		}
		if p.Pricing.PromptPer1M < 0 || p.Pricing.CompletionPer1M < 0 {
			return fmt.Errorf("config: providers[%d] (%s): pricing must not be negative", i, p.Name)
		}
	}
	return nil
}

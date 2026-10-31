// Package config loads and validates the gateway's policy-as-code file.
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/mengxie/aegis/internal/policy"
	"github.com/mengxie/aegis/internal/usage"
)

type Config struct {
	Listen         string              `json:"listen"`
	RequestTimeout Duration            `json:"requestTimeout"`
	Upstreams      map[string]Upstream `json:"upstreams"`
	Models         map[string]Model    `json:"models"`
	Teams          map[string]Team     `json:"teams"`

	keys map[string]string // key hash -> team
}

// Upstream is an LLM provider. Types: "openai" (OpenAI and any compatible API, such as vLLM,
// Ollama or Mistral) and "azure-openai".
type Upstream struct {
	Type       string `json:"type"`
	BaseURL    string `json:"baseURL"`
	APIKey     string `json:"apiKey"`
	APIVersion string `json:"apiVersion"`
}

// Model is an alias clients ask for, tried against its routes in order until one succeeds.
type Model struct {
	Routes []Route `json:"routes"`
	usage.Price
}

type Route struct {
	Upstream string `json:"upstream"`
	Model    string `json:"model"`
}

type Team struct {
	// Keys are SHA-256 hashes of API keys ("sha256:<hex>"), never the keys themselves.
	Keys   []string      `json:"keys"`
	Policy policy.Policy `json:"policy"`
}

type Duration struct{ time.Duration }

func (d *Duration) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(text)
	d.Duration = parsed
	return err
}

var envReference = regexp.MustCompile(`^\$\{([A-Z0-9_]+)\}$`)

// Load reads, expands and validates a config file, reporting every problem at once.
func Load(path string, lookupEnv func(string) (string, bool)) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data, lookupEnv)
}

func Parse(data []byte, lookupEnv func(string) (string, bool)) (*Config, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields() // a typo in a policy must not be silently ignored
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if cfg.Listen == "" {
		cfg.Listen = ":8080"
	}
	if cfg.RequestTimeout.Duration == 0 {
		cfg.RequestTimeout.Duration = 120 * time.Second
	}

	var errs []error
	for name, up := range cfg.Upstreams {
		// Secrets come from the environment, never from the file: "apiKey": "${OPENAI_API_KEY}".
		if match := envReference.FindStringSubmatch(up.APIKey); match != nil {
			value, ok := lookupEnv(match[1])
			if !ok {
				errs = append(errs, fmt.Errorf("upstream %q: environment variable %s is not set", name, match[1]))
			}
			up.APIKey = value
		} else if up.APIKey != "" {
			errs = append(errs, fmt.Errorf("upstream %q: put the API key in an environment variable, as \"${NAME}\"", name))
		}
		up.BaseURL = strings.TrimRight(up.BaseURL, "/")
		switch {
		case up.Type != "openai" && up.Type != "azure-openai":
			errs = append(errs, fmt.Errorf("upstream %q: type must be openai or azure-openai", name))
		case !strings.HasPrefix(up.BaseURL, "http://") && !strings.HasPrefix(up.BaseURL, "https://"):
			errs = append(errs, fmt.Errorf("upstream %q: baseURL must be an http(s) URL", name))
		case up.Type == "azure-openai" && up.APIVersion == "":
			errs = append(errs, fmt.Errorf("upstream %q: azure-openai needs apiVersion", name))
		}
		cfg.Upstreams[name] = up
	}

	models := map[string]bool{}
	for alias, model := range cfg.Models {
		models[alias] = true
		if len(model.Routes) == 0 {
			errs = append(errs, fmt.Errorf("model %q: needs at least one route", alias))
		}
		for _, route := range model.Routes {
			if _, ok := cfg.Upstreams[route.Upstream]; !ok {
				errs = append(errs, fmt.Errorf("model %q: unknown upstream %q", alias, route.Upstream))
			}
		}
		if model.InputPerMTok < 0 || model.OutputPerMTok < 0 {
			errs = append(errs, fmt.Errorf("model %q: prices cannot be negative", alias))
		}
	}

	cfg.keys = map[string]string{}
	for name, team := range cfg.Teams {
		team.Policy.ApplyDefaults()
		cfg.Teams[name] = team
		for _, err := range team.Policy.Validate(models) {
			errs = append(errs, fmt.Errorf("team %q: %w", name, err))
		}
		if len(team.Keys) == 0 {
			errs = append(errs, fmt.Errorf("team %q: needs at least one key", name))
		}
		for _, key := range team.Keys {
			hash, ok := strings.CutPrefix(key, "sha256:")
			if !ok || len(hash) != 64 {
				errs = append(errs, fmt.Errorf("team %q: keys must be \"sha256:<64 hex chars>\" (run aegis new-key)", name))
				continue
			}
			if other, taken := cfg.keys[hash]; taken {
				errs = append(errs, fmt.Errorf("team %q: key is also used by team %q", name, other))
			}
			cfg.keys[hash] = name
		}
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("config is invalid:\n%w", errors.Join(errs...))
	}
	return &cfg, nil
}

// HashKey is how API keys are stored and looked up.
func HashKey(apiKey string) string {
	sum := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(sum[:])
}

// TeamForKey resolves an API key to its team.
func (c *Config) TeamForKey(apiKey string) (string, Team, bool) {
	name, ok := c.keys[HashKey(apiKey)]
	if !ok {
		return "", Team{}, false
	}
	return name, c.Teams[name], true
}

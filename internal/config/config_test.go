package config

import (
	"strings"
	"testing"
)

func env(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) { v, ok := values[name]; return v, ok }
}

const valid = `{
  "upstreams": {"openai": {"type": "openai", "baseURL": "https://api.openai.com/v1/", "apiKey": "${OPENAI_API_KEY}"}},
  "models": {"gpt-4o": {"routes": [{"upstream": "openai", "model": "gpt-4o"}], "inputPerMTok": 2.5, "outputPerMTok": 10}},
  "teams": {"support": {"keys": ["sha256:` + "a3f1c2b4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f80" + `"], "policy": {"models": ["gpt-4o"]}}}
}`

func TestParsesAValidConfigWithDefaults(t *testing.T) {
	cfg, err := Parse([]byte(valid), env(map[string]string{"OPENAI_API_KEY": "sk-test"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":8080" || cfg.Upstreams["openai"].APIKey != "sk-test" || cfg.Upstreams["openai"].BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	policy := cfg.Teams["support"].Policy
	if policy.PII != "pseudonymize" || !*policy.BlockSecrets {
		t.Fatalf("defaults not applied: %+v", policy)
	}
}

func TestReportsEveryProblemAtOnce(t *testing.T) {
	bad := strings.NewReplacer(`"type": "openai"`, `"type": "anthropic"`, `"upstream": "openai"`, `"upstream": "missing"`, `"models": ["gpt-4o"]`, `"models": ["gpt-5"], "pii": "maybe"`).Replace(valid)
	_, err := Parse([]byte(bad), env(nil))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"OPENAI_API_KEY is not set", "type must be openai", `unknown upstream "missing"`, `unknown model "gpt-5"`, "pii must be"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q:\n%v", want, err)
		}
	}
}

func TestRejectsTyposAndInlineSecrets(t *testing.T) {
	if _, err := Parse([]byte(strings.Replace(valid, `"teams"`, `"tems"`, 1)), env(map[string]string{"OPENAI_API_KEY": "x"})); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Errorf("typo not caught: %v", err)
	}
	inline := strings.Replace(valid, "${OPENAI_API_KEY}", "sk-pasted-straight-in", 1)
	if _, err := Parse([]byte(inline), env(nil)); err == nil || !strings.Contains(err.Error(), "environment variable") {
		t.Errorf("inline secret not caught: %v", err)
	}
}

func TestResolvesKeysByHashOnly(t *testing.T) {
	key := "aeg_example"
	cfg, err := Parse([]byte(strings.Replace(valid, "a3f1c2b4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f80", HashKey(key), 1)), env(map[string]string{"OPENAI_API_KEY": "x"}))
	if err != nil {
		t.Fatal(err)
	}
	if team, _, ok := cfg.TeamForKey(key); !ok || team != "support" {
		t.Fatalf("key did not resolve: %q %v", team, ok)
	}
	if _, _, ok := cfg.TeamForKey("aeg_wrong"); ok {
		t.Fatal("wrong key resolved")
	}
}

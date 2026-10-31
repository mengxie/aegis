package gateway

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mengxie/aegis/internal/config"
	"github.com/mengxie/aegis/internal/mock"
	"github.com/mengxie/aegis/internal/usage"
)

const apiKey = "aeg_test-key"

type rig struct {
	url             string
	primary, backup *mock.Server
	store           *usage.MemoryStore
}

func newRig(t *testing.T, policy string) *rig {
	t.Helper()
	primary, backup := mock.New(), mock.New()
	primaryServer, backupServer := httptest.NewServer(primary), httptest.NewServer(backup)
	t.Cleanup(primaryServer.Close)
	t.Cleanup(backupServer.Close)
	raw := fmt.Sprintf(`{
	  "upstreams": {
	    "primary": {"type": "openai", "baseURL": %q, "apiKey": "${KEY}"},
	    "backup": {"type": "azure-openai", "baseURL": %q, "apiKey": "${KEY}", "apiVersion": "2024-10-21"}
	  },
	  "models": {
	    "smart": {"routes": [{"upstream": "primary", "model": "model-a"}, {"upstream": "backup", "model": "model-b"}], "inputPerMTok": 2.5, "outputPerMTok": 10},
	    "picky": {"routes": [{"upstream": "primary", "model": "reject-all"}, {"upstream": "backup", "model": "model-b"}]},
	    "secret-project": {"routes": [{"upstream": "primary", "model": "model-c"}]}
	  },
	  "teams": {"support": {"keys": ["sha256:%s"], "policy": %s}}
	}`, primaryServer.URL, backupServer.URL, config.HashKey(apiKey), policy)
	cfg, err := config.Parse([]byte(raw), func(string) (string, bool) { return "upstream-secret", true })
	if err != nil {
		t.Fatal(err)
	}
	store := usage.NewMemoryStore()
	frozen := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	gw := New(Options{Config: cfg, Store: store, HTTPClient: http.DefaultClient, Now: func() time.Time { return frozen }})
	server := httptest.NewServer(gw.Handler())
	t.Cleanup(server.Close)
	return &rig{url: server.URL, primary: primary, backup: backup, store: store}
}

func (r *rig) post(t *testing.T, key string, body map[string]any) (*http.Response, map[string]any) {
	t.Helper()
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, r.url+"/v1/chat/completions", strings.NewReader(string(data)))
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var parsed map[string]any
	_ = json.Unmarshal(raw, &parsed)
	return resp, parsed
}

func chat(text string, extra ...any) map[string]any {
	body := map[string]any{"model": "smart", "messages": []any{map[string]any{"role": "user", "content": text}}}
	for i := 0; i+1 < len(extra); i += 2 {
		body[extra[i].(string)] = extra[i+1]
	}
	return body
}

func reply(body map[string]any) string {
	choices, _ := body["choices"].([]any)
	if len(choices) == 0 {
		return ""
	}
	message, _ := choices[0].(map[string]any)["message"].(map[string]any)
	content, _ := message["content"].(string)
	return content
}

func errorCode(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

func sentToProvider(s *mock.Server) string {
	received := s.Received()
	if len(received) == 0 {
		return ""
	}
	data, _ := json.Marshal(received[len(received)-1]["messages"])
	return string(data)
}

func TestPersonalDataNeverReachesTheProviderButComesBack(t *testing.T) {
	r := newRig(t, `{}`)
	resp, body := r.post(t, apiKey, chat("Email aroha@acme.co.nz or call +64 21 555 0199 about it"))

	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %v", resp.StatusCode, body)
	}
	if got := sentToProvider(r.primary); strings.Contains(got, "aroha@") || !strings.Contains(got, "[EMAIL_1]") || !strings.Contains(got, "[PHONE_1]") {
		t.Fatalf("provider saw %s", got)
	}
	if got := reply(body); got != "Echo: Email aroha@acme.co.nz or call +64 21 555 0199 about it" {
		t.Fatalf("client got %q", got)
	}
	if resp.Header.Get("X-Aegis-Pii-Protected") != "2" || resp.Header.Get("X-Aegis-Request-Id") == "" {
		t.Fatalf("headers: %v", resp.Header)
	}
	record := r.store.Snapshot()[0]
	if record.PII["EMAIL"] != 1 || record.PII["PHONE"] != 1 || record.Cost <= 0 || record.Team != "support" {
		t.Fatalf("audit record %+v", record)
	}
}

func TestStreamingRestoresPlaceholdersSplitAcrossChunks(t *testing.T) {
	r := newRig(t, `{}`)
	data, _ := json.Marshal(chat("Please reply to aroha@acme.co.nz", "stream", true))
	req, _ := http.NewRequest(http.MethodPost, r.url+"/v1/chat/completions", strings.NewReader(string(data)))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var text strings.Builder
	sawUsage, sawDone := false, false
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data: ")
		if !ok {
			continue
		}
		if data == "[DONE]" {
			sawDone = true
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct{ Content string } `json:"delta"`
			} `json:"choices"`
			Usage any `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			t.Fatalf("bad chunk %q", data)
		}
		sawUsage = sawUsage || chunk.Usage != nil
		for _, c := range chunk.Choices {
			text.WriteString(c.Delta.Content)
		}
	}
	if text.String() != "Echo: Please reply to aroha@acme.co.nz" || !sawDone {
		t.Fatalf("streamed %q (done=%v)", text.String(), sawDone)
	}
	if sawUsage {
		t.Fatal("the client did not ask for usage, so the usage chunk must not be forwarded")
	}
	if record := r.store.Snapshot()[0]; record.CompletionTokens == 0 || record.Cost == 0 {
		t.Fatalf("streamed usage was not recorded: %+v", record)
	}
}

func TestSecretsAreBlockedBeforeLeavingTheCompany(t *testing.T) {
	r := newRig(t, `{}`)
	resp, body := r.post(t, apiKey, chat("Why does AKIAIOSFODNN7EXAMPLE fail?"))
	if resp.StatusCode != 403 || errorCode(body) != "secret_detected" || len(r.primary.Received()) != 0 {
		t.Fatalf("status %d, code %q, provider calls %d", resp.StatusCode, errorCode(body), len(r.primary.Received()))
	}
}

func TestPolicyModes(t *testing.T) {
	t.Run("block", func(t *testing.T) {
		r := newRig(t, `{"pii": "block"}`)
		resp, body := r.post(t, apiKey, chat("Email aroha@acme.co.nz"))
		if resp.StatusCode != 403 || errorCode(body) != "pii_detected" {
			t.Fatalf("status %d, code %q", resp.StatusCode, errorCode(body))
		}
	})
	t.Run("redact", func(t *testing.T) {
		r := newRig(t, `{"pii": "redact"}`)
		_, body := r.post(t, apiKey, chat("Email aroha@acme.co.nz"))
		if !strings.Contains(sentToProvider(r.primary), "[EMAIL]") || reply(body) != "Echo: Email [EMAIL]" {
			t.Fatalf("provider saw %s, client got %q", sentToProvider(r.primary), reply(body))
		}
	})
	t.Run("allowed models, token cap and blocked terms", func(t *testing.T) {
		r := newRig(t, `{"models": ["smart"], "maxTokens": 100, "blockedTerms": ["Project Orca"]}`)
		if resp, body := r.post(t, apiKey, map[string]any{"model": "secret-project", "messages": []any{map[string]any{"role": "user", "content": "hi"}}}); resp.StatusCode != 403 || errorCode(body) != "model_not_allowed" {
			t.Fatalf("model policy: %d %q", resp.StatusCode, errorCode(body))
		}
		if resp, body := r.post(t, apiKey, chat("Summarise project orca", "max_tokens", 5000)); resp.StatusCode != 403 || errorCode(body) != "blocked_term" {
			t.Fatalf("blocked term: %d %q", resp.StatusCode, errorCode(body))
		}
		r.post(t, apiKey, chat("Summarise this", "max_tokens", 5000))
		if got := r.primary.Received()[0]["max_tokens"]; got != float64(100) {
			t.Fatalf("max_tokens sent upstream = %v, want 100", got)
		}
	})
}

func TestFailoverAndCircuitBreaker(t *testing.T) {
	r := newRig(t, `{}`)
	r.primary.FailNext(1)
	resp, body := r.post(t, apiKey, chat("hello"))
	if resp.StatusCode != 200 || resp.Header.Get("X-Aegis-Upstream") != "backup" || reply(body) != "Echo: hello" {
		t.Fatalf("failover: %d via %q", resp.StatusCode, resp.Header.Get("X-Aegis-Upstream"))
	}
	if got := r.backup.Received()[0]["model"]; got != "model-b" {
		t.Fatalf("backup was asked for %v", got)
	}

	r.primary.FailNext(100)
	for range 6 {
		r.post(t, apiKey, chat("hello"))
	}
	// One earlier failure plus four more open the breaker at five; the sixth call skips the primary.
	if calls := len(r.primary.Received()); calls != 5 {
		t.Fatalf("primary called %d times, want 5 before the breaker opened", calls)
	}
}

func TestClientErrorsPassThroughWithoutFailover(t *testing.T) {
	r := newRig(t, `{}`)
	resp, _ := r.post(t, apiKey, map[string]any{"model": "picky", "messages": []any{map[string]any{"role": "user", "content": "hi"}}})
	if resp.StatusCode != 400 || len(r.backup.Received()) != 0 {
		t.Fatalf("status %d, backup calls %d", resp.StatusCode, len(r.backup.Received()))
	}
}

func TestRateLimitAndBudget(t *testing.T) {
	t.Run("rate limit", func(t *testing.T) {
		r := newRig(t, `{"requestsPerMinute": 2}`)
		r.post(t, apiKey, chat("one"))
		r.post(t, apiKey, chat("two"))
		resp, body := r.post(t, apiKey, chat("three"))
		if resp.StatusCode != 429 || errorCode(body) != "rate_limit_exceeded" || resp.Header.Get("Retry-After") != "30" {
			t.Fatalf("status %d, code %q, retry %q", resp.StatusCode, errorCode(body), resp.Header.Get("Retry-After"))
		}
	})
	t.Run("budget", func(t *testing.T) {
		r := newRig(t, `{"monthlyBudgetUSD": 0.00001}`)
		if resp, _ := r.post(t, apiKey, chat("this costs a little")); resp.StatusCode != 200 {
			t.Fatalf("first request: %d", resp.StatusCode)
		}
		resp, body := r.post(t, apiKey, chat("over budget now"))
		if resp.StatusCode != 429 || errorCode(body) != "budget_exceeded" || len(r.primary.Received()) != 1 {
			t.Fatalf("status %d, code %q", resp.StatusCode, errorCode(body))
		}
	})
}

func TestDeterministicRequestsAreCachedPerTeam(t *testing.T) {
	r := newRig(t, `{"cacheSeconds": 60}`)
	first, _ := r.post(t, apiKey, chat("Classify aroha@acme.co.nz's ticket", "temperature", 0))
	second, body := r.post(t, apiKey, chat("Classify aroha@acme.co.nz's ticket", "temperature", 0))
	if first.Header.Get("X-Aegis-Cache") != "miss" || second.Header.Get("X-Aegis-Cache") != "hit" || len(r.primary.Received()) != 1 {
		t.Fatalf("cache: %q then %q, provider calls %d", first.Header.Get("X-Aegis-Cache"), second.Header.Get("X-Aegis-Cache"), len(r.primary.Received()))
	}
	if reply(body) != "Echo: Classify aroha@acme.co.nz's ticket" {
		t.Fatalf("cached reply %q", reply(body))
	}
	r.post(t, apiKey, chat("Classify aroha@acme.co.nz's ticket", "temperature", 0.7))
	if len(r.primary.Received()) != 2 {
		t.Fatal("non-deterministic requests must not be cached")
	}
}

func TestAuthModelsAndOperationalEndpoints(t *testing.T) {
	r := newRig(t, `{"models": ["smart"]}`)
	resp, body := r.post(t, "aeg_wrong", chat("hi"))
	if resp.StatusCode != 401 || errorCode(body) != "invalid_api_key" {
		t.Fatalf("auth: %d %q", resp.StatusCode, errorCode(body))
	}
	r.post(t, apiKey, chat("hi"))

	req, _ := http.NewRequest(http.MethodGet, r.url+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	models, _ := http.DefaultClient.Do(req)
	var list struct{ Data []struct{ ID string } }
	_ = json.NewDecoder(models.Body).Decode(&list)
	models.Body.Close()
	if len(list.Data) != 1 || list.Data[0].ID != "smart" {
		t.Fatalf("models %+v", list)
	}
	for path, want := range map[string]string{"/healthz": "ok", "/readyz": "ready", "/metrics": `aegis_requests_total{team="support",model="smart",status="200"} 1`} {
		resp, _ := http.Get(r.url + path)
		text, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(text), want) {
			t.Errorf("%s missing %q:\n%s", path, want, text)
		}
	}
}

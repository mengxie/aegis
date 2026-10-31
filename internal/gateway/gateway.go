// Package gateway is the HTTP front door: an OpenAI-compatible API that enforces policy, protects
// personal data, routes to providers with failover, and accounts for every token.
package gateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mengxie/aegis/internal/cache"
	"github.com/mengxie/aegis/internal/config"
	"github.com/mengxie/aegis/internal/metrics"
	"github.com/mengxie/aegis/internal/pii"
	"github.com/mengxie/aegis/internal/policy"
	"github.com/mengxie/aegis/internal/provider"
	"github.com/mengxie/aegis/internal/ratelimit"
	"github.com/mengxie/aegis/internal/usage"
)

const maxRequestBytes = 2 << 20

type Options struct {
	Config     *config.Config
	Store      usage.Store
	HTTPClient *http.Client
	Logger     *slog.Logger
	Registry   *metrics.Registry
	Now        func() time.Time
}

type Gateway struct {
	cfg       *config.Config
	store     usage.Store
	providers *provider.Client
	limiter   *ratelimit.Limiter
	cache     *cache.LRU[[]byte]
	log       *slog.Logger
	registry  *metrics.Registry
	now       func() time.Time
	m         gatewayMetrics
}

type gatewayMetrics struct {
	requests, tokens, cost, pii, upstreamFailures, cacheHits *metrics.Counter
	latency                                                  *metrics.Histogram
}

func New(opts Options) *Gateway {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Registry == nil {
		opts.Registry = &metrics.Registry{}
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	r := opts.Registry
	return &Gateway{
		cfg:       opts.Config,
		store:     opts.Store,
		providers: provider.NewClient(opts.HTTPClient, opts.Now),
		limiter:   ratelimit.New(opts.Now),
		cache:     cache.New[[]byte](10_000, opts.Now),
		log:       opts.Logger,
		registry:  r,
		now:       opts.Now,
		m: gatewayMetrics{
			requests:         r.Counter("aegis_requests_total", "Chat completion requests by outcome.", "team", "model", "status"),
			tokens:           r.Counter("aegis_tokens_total", "Tokens used.", "team", "model", "kind"),
			cost:             r.Counter("aegis_cost_usd_total", "Spend in US dollars.", "team", "model"),
			pii:              r.Counter("aegis_pii_protected_total", "Sensitive values kept out of prompts.", "kind"),
			upstreamFailures: r.Counter("aegis_upstream_failures_total", "Failed provider calls, including circuit-breaker skips.", "upstream"),
			cacheHits:        r.Counter("aegis_cache_hits_total", "Responses served from cache.", "team"),
			latency:          r.Histogram("aegis_request_duration_seconds", "End-to-end request latency.", []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}, "model"),
		},
	}
}

func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", g.chat)
	mux.HandleFunc("GET /v1/models", g.models)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := g.store.Ping(r.Context()); err != nil {
			http.Error(w, "store unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, "ready")
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = g.registry.WriteTo(w)
	})
	return mux
}

// exchange is the per-request state that ends up in the audit log.
type exchange struct {
	id, team, keyID, model, upstream string
	decisions                        []string
	status                           int
	promptTokens, completionTokens   int
	cost                             usage.NanoUSD
	cached                           bool
	pii                              map[string]int
	started                          time.Time
}

func (g *Gateway) chat(w http.ResponseWriter, r *http.Request) {
	x := &exchange{id: requestID(r), started: g.now(), pii: map[string]int{}}
	w.Header().Set("X-Aegis-Request-Id", x.id)
	if err := g.handleChat(w, r, x); err != nil {
		x.status = err.status
		x.decisions = append(x.decisions, err.code)
		err.write(w)
	}
	g.finish(context.WithoutCancel(r.Context()), x)
}

func (g *Gateway) handleChat(w http.ResponseWriter, r *http.Request, x *exchange) *apiError {
	apiKey, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	team, settings, ok := g.cfg.TeamForKey(strings.TrimSpace(apiKey))
	if !ok {
		return errUnauthorized
	}
	pol := settings.Policy
	x.team, x.keyID = team, config.HashKey(apiKey)[:12]

	if allowed, wait := g.limiter.Allow(x.keyID, pol.RequestsPerMinute); !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		return tooManyRequests("rate_limit_exceeded", fmt.Sprintf("This key may make %d requests per minute.", pol.RequestsPerMinute))
	}

	req, apiErr := parseChat(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if apiErr != nil {
		return apiErr
	}
	x.model = req.model
	model, known := g.cfg.Models[req.model]
	if !known {
		return &apiError{http.StatusNotFound, "invalid_request_error", "model_not_found", fmt.Sprintf("The model %q does not exist.", req.model)}
	}
	if !pol.AllowsModel(req.model) {
		return forbidden("model_not_allowed", fmt.Sprintf("Your team is not allowed to use %q.", req.model))
	}

	if pol.MonthlyBudgetUSD > 0 {
		spent, err := g.store.Spent(r.Context(), team, usage.Month(g.now()))
		if err != nil {
			return &apiError{http.StatusServiceUnavailable, "api_error", "ledger_unavailable", "Spend cannot be checked right now, so the request was not sent."}
		}
		if spent >= usage.FromDollars(pol.MonthlyBudgetUSD) {
			return tooManyRequests("budget_exceeded", fmt.Sprintf("Your team has used its monthly budget of $%.2f.", pol.MonthlyBudgetUSD))
		}
	}

	vault := pii.NewVault()
	if err := req.eachText(func(text string) (string, *apiError) { return g.protect(text, pol, vault, x) }); err != nil {
		return err
	}

	if pol.MaxTokens > 0 && (req.maxTokens == nil || *req.maxTokens > pol.MaxTokens) {
		if req.maxTokens != nil { // only worth auditing when the caller asked for more than allowed
			x.decisions = append(x.decisions, "max_tokens_capped")
		}
		req.set("max_tokens", pol.MaxTokens)
	}

	clientWantsUsage := false
	if req.stream {
		var options map[string]any
		_ = json.Unmarshal(req.raw["stream_options"], &options)
		clientWantsUsage, _ = options["include_usage"].(bool)
		if options == nil {
			options = map[string]any{}
		}
		options["include_usage"] = true // always know what a streamed request cost
		req.set("stream_options", options)
	}

	cacheKey := ""
	if !req.stream && pol.CacheSeconds > 0 && req.temperature != nil && *req.temperature == 0 {
		sum := sha256.Sum256(mustJSON(req.raw))
		cacheKey = team + ":" + hex.EncodeToString(sum[:])
		if body, hit := g.cache.Get(cacheKey); hit {
			x.cached, x.status, x.upstream = true, http.StatusOK, "cache"
			g.m.cacheHits.Add(1, team)
			w.Header().Set("X-Aegis-Cache", "hit")
			return g.writeJSON(w, http.StatusOK, body, vault, x, usage.Price{})
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), g.cfg.RequestTimeout.Duration)
	defer cancel()
	for _, route := range model.Routes {
		breaker := g.providers.Breaker(route.Upstream)
		if !breaker.Allow() {
			g.m.upstreamFailures.Add(1, route.Upstream)
			continue
		}
		resp, err := g.providers.Send(ctx, g.cfg.Upstreams[route.Upstream], route.Model, req.raw, req.stream)
		if err != nil || resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
			breaker.Failure()
			g.m.upstreamFailures.Add(1, route.Upstream)
			if resp != nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
			continue // fail over to the next route
		}
		breaker.Success()
		x.upstream = route.Upstream
		w.Header().Set("X-Aegis-Upstream", route.Upstream)
		defer resp.Body.Close()
		if resp.StatusCode >= 400 { // the request itself is wrong: other providers would reject it too
			x.status = resp.StatusCode
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.StatusCode)
			_, _ = io.Copy(w, resp.Body)
			return nil
		}
		if req.stream {
			g.relayStream(w, resp, vault, x, clientWantsUsage, model.Price)
			return nil
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		if err != nil {
			return &apiError{http.StatusBadGateway, "api_error", "upstream_read_failed", "The provider's response could not be read."}
		}
		if cacheKey != "" {
			g.cache.Put(cacheKey, body, time.Duration(pol.CacheSeconds)*time.Second)
			w.Header().Set("X-Aegis-Cache", "miss")
		}
		return g.writeJSON(w, resp.StatusCode, body, vault, x, model.Price)
	}
	return &apiError{http.StatusBadGateway, "api_error", "upstream_unavailable", "Every provider for this model is unavailable right now. Try again shortly."}
}

// protect applies the team's data rules to one piece of prompt text.
func (g *Gateway) protect(text string, pol policy.Policy, vault *pii.Vault, x *exchange) (string, *apiError) {
	if term, found := pol.BlockedTerm(text); found {
		return "", forbidden("blocked_term", fmt.Sprintf("Requests mentioning %q are not allowed by your team's policy.", term))
	}
	findings := pii.Scan(text)
	for _, f := range findings {
		if f.Kind == pii.Secret && *pol.BlockSecrets {
			return "", forbidden("secret_detected", "The request contains what looks like a credential (an API key, token or private key). Remove it and try again.")
		}
	}
	if len(findings) == 0 || pol.PII == policy.Allow {
		return text, nil
	}
	for _, f := range findings {
		x.pii[string(f.Kind)]++
		g.m.pii.Add(1, string(f.Kind))
	}
	switch pol.PII {
	case policy.Block:
		return "", forbidden("pii_detected", "The request contains personal data, which your team's policy does not allow to leave the company.")
	case policy.Redact:
		redacted, _ := pii.Redact(text)
		return redacted, nil
	default:
		pseudonymized, _ := vault.Pseudonymize(text)
		return pseudonymized, nil
	}
}

// writeJSON restores personal data in a non-streamed response and records its usage.
func (g *Gateway) writeJSON(w http.ResponseWriter, status int, body []byte, vault *pii.Vault, x *exchange, price usage.Price) *apiError {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err == nil {
		if !x.cached {
			x.promptTokens, x.completionTokens = readUsage(payload["usage"])
			x.cost = price.Cost(x.promptTokens, x.completionTokens)
		}
		if vault.Size() > 0 {
			var choices []map[string]json.RawMessage
			if json.Unmarshal(payload["choices"], &choices) == nil {
				for _, choice := range choices {
					var message map[string]json.RawMessage
					var content string
					if json.Unmarshal(choice["message"], &message) == nil && json.Unmarshal(message["content"], &content) == nil {
						message["content"] = mustJSON(vault.Restore(content))
						choice["message"] = mustJSON(message)
					}
				}
				payload["choices"] = mustJSON(choices)
			}
			body = mustJSON(payload)
		}
	}
	x.status = status
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Aegis-Pii-Protected", strconv.Itoa(vault.Size()))
	w.WriteHeader(status)
	_, _ = w.Write(body)
	return nil
}

func readUsage(raw json.RawMessage) (prompt, completion int) {
	var u struct {
		Prompt     int `json:"prompt_tokens"`
		Completion int `json:"completion_tokens"`
	}
	_ = json.Unmarshal(raw, &u)
	return u.Prompt, u.Completion
}

func (g *Gateway) finish(ctx context.Context, x *exchange) {
	elapsed := g.now().Sub(x.started)
	decision := "allowed"
	if len(x.decisions) > 0 {
		decision = strings.Join(x.decisions, ",")
	}
	status := strconv.Itoa(x.status)
	g.m.requests.Add(1, x.team, x.model, status)
	g.m.latency.Observe(elapsed.Seconds(), x.model)
	if x.team != "" {
		g.m.tokens.Add(float64(x.promptTokens), x.team, x.model, "prompt")
		g.m.tokens.Add(float64(x.completionTokens), x.team, x.model, "completion")
		g.m.cost.Add(x.cost.Dollars(), x.team, x.model)
		record := usage.Record{
			RequestID: x.id, At: x.started, Team: x.team, KeyID: x.keyID, Model: x.model, Upstream: x.upstream,
			Status: x.status, PromptTokens: x.promptTokens, CompletionTokens: x.completionTokens, Cost: x.cost,
			Cached: x.cached, PII: x.pii, Decision: decision, Latency: elapsed,
		}
		if _, err := g.store.Record(ctx, record); err != nil {
			g.log.ErrorContext(ctx, "could not record usage", "request_id", x.id, "error", err)
		}
	}
	// One structured line per request: who, what, cost and decisions. Never the prompt or completion.
	g.log.LogAttrs(ctx, slog.LevelInfo, "request",
		slog.String("request_id", x.id), slog.String("team", x.team), slog.String("key_id", x.keyID),
		slog.String("model", x.model), slog.String("upstream", x.upstream), slog.Int("status", x.status),
		slog.Int("prompt_tokens", x.promptTokens), slog.Int("completion_tokens", x.completionTokens),
		slog.Float64("cost_usd", x.cost.Dollars()), slog.Bool("cached", x.cached), slog.Any("pii", x.pii),
		slog.String("decision", decision), slog.Int64("latency_ms", elapsed.Milliseconds()))
}

func (g *Gateway) models(w http.ResponseWriter, r *http.Request) {
	apiKey, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	_, team, ok := g.cfg.TeamForKey(strings.TrimSpace(apiKey))
	if !ok {
		errUnauthorized.write(w)
		return
	}
	var names []string
	for name := range g.cfg.Models {
		if team.Policy.AllowsModel(name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	data := make([]map[string]any, 0, len(names))
	for _, name := range names {
		data = append(data, map[string]any{"id": name, "object": "model", "owned_by": "aegis"})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

var safeRequestID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// requestID honours a caller's X-Request-Id for end-to-end tracing, if it is safe to log.
func requestID(r *http.Request) string {
	if id := r.Header.Get("X-Request-Id"); safeRequestID.MatchString(id) {
		return id
	}
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "req_" + hex.EncodeToString(b)
}

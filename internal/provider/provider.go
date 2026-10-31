// Package provider calls upstream LLM APIs and tracks their health with circuit breakers.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/mengxie/aegis/internal/config"
)

// Breaker stops sending traffic to an upstream after repeated failures, then lets a single probe
// request through after a cool-down to see whether it has recovered.
type Breaker struct {
	mu        sync.Mutex
	failures  int
	openUntil time.Time
	probing   bool
	threshold int
	cooldown  time.Duration
	now       func() time.Time
}

func NewBreaker(threshold int, cooldown time.Duration, now func() time.Time) *Breaker {
	return &Breaker{threshold: threshold, cooldown: cooldown, now: now}
}

func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failures < b.threshold {
		return true
	}
	if b.now().Before(b.openUntil) || b.probing {
		return false
	}
	b.probing = true // half-open: one request decides
	return true
}

func (b *Breaker) Success() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures, b.probing = 0, false
}

func (b *Breaker) Failure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures++
	b.probing = false
	if b.failures >= b.threshold {
		b.openUntil = b.now().Add(b.cooldown)
	}
}

func (b *Breaker) Open() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.failures >= b.threshold && b.now().Before(b.openUntil)
}

type Client struct {
	http     *http.Client
	mu       sync.Mutex
	breakers map[string]*Breaker
	now      func() time.Time
}

func NewClient(httpClient *http.Client, now func() time.Time) *Client {
	return &Client{http: httpClient, breakers: map[string]*Breaker{}, now: now}
}

func (c *Client) Breaker(upstream string) *Breaker {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.breakers[upstream]
	if !ok {
		b = NewBreaker(5, 30*time.Second, c.now)
		c.breakers[upstream] = b
	}
	return b
}

// Send forwards a chat completion request, with the client's model alias swapped for the upstream model.
func (c *Client) Send(ctx context.Context, up config.Upstream, model string, body map[string]json.RawMessage, stream bool) (*http.Response, error) {
	request := make(map[string]json.RawMessage, len(body)+1)
	for key, value := range body {
		request[key] = value
	}
	encodedModel, _ := json.Marshal(model)
	request["model"] = encodedModel
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}

	endpoint := up.BaseURL + "/chat/completions"
	if up.Type == "azure-openai" {
		endpoint = fmt.Sprintf("%s/%s/chat/completions?api-version=%s", up.BaseURL, url.PathEscape(model), url.QueryEscape(up.APIVersion))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	if up.APIKey != "" {
		if up.Type == "azure-openai" {
			req.Header.Set("api-key", up.APIKey)
		} else {
			req.Header.Set("Authorization", "Bearer "+up.APIKey)
		}
	}
	return c.http.Do(req)
}

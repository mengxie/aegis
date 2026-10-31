// Package mock is an OpenAI-compatible stand-in provider for demos and tests. It echoes the last
// user message, so the round trip of placeholders through a "model" is visible.
package mock

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Server struct {
	// Log, when set, prints each prompt the provider receives, to show what left the company.
	Log      *slog.Logger
	mu       sync.Mutex
	received []map[string]any
	failNext int
}

func New() *Server { return &Server{} }

// FailNext makes the next n requests fail with 503, to exercise failover.
func (s *Server) FailNext(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failNext = n
}

// Received returns every request body the provider has seen.
func (s *Server) Received() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.received...)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		http.NotFound(w, r)
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.received = append(s.received, body)
	fail := s.failNext > 0
	if fail {
		s.failNext--
	}
	s.mu.Unlock()

	model, _ := body["model"].(string)
	if fail || strings.Contains(model, "unavailable") {
		writeError(w, http.StatusServiceUnavailable, "The mock provider is unavailable.")
		return
	}
	if strings.Contains(model, "reject") {
		writeError(w, http.StatusBadRequest, "The mock provider rejected this request.")
		return
	}

	if s.Log != nil {
		s.Log.Info("provider received", "model", model, "prompt", lastUserText(body))
	}
	reply := "Echo: " + lastUserText(body)
	promptTokens, completionTokens := words(fmt.Sprint(body["messages"])), words(reply)
	if stream, _ := body["stream"].(bool); stream {
		streamReply(w, model, reply, promptTokens, completionTokens, includeUsage(body))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": "chatcmpl-mock", "object": "chat.completion", "created": time.Now().Unix(), "model": model,
		"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": reply}, "finish_reason": "stop"}},
		"usage":   map[string]any{"prompt_tokens": promptTokens, "completion_tokens": completionTokens, "total_tokens": promptTokens + completionTokens},
	})
}

// streamReply sends the reply four characters at a time, deliberately splitting placeholders.
func streamReply(w http.ResponseWriter, model, reply string, promptTokens, completionTokens int, usage bool) {
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)
	chunk := func(choices []any, extra map[string]any) {
		payload := map[string]any{"id": "chatcmpl-mock", "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": model, "choices": choices}
		for key, value := range extra {
			payload[key] = value
		}
		data, _ := json.Marshal(payload)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		if flusher != nil {
			flusher.Flush()
		}
	}
	chunk([]any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": ""}, "finish_reason": nil}}, nil)
	runes := []rune(reply)
	for i := 0; i < len(runes); i += 4 {
		piece := string(runes[i:min(i+4, len(runes))])
		chunk([]any{map[string]any{"index": 0, "delta": map[string]any{"content": piece}, "finish_reason": nil}}, nil)
	}
	chunk([]any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}, nil)
	if usage {
		chunk([]any{}, map[string]any{"usage": map[string]any{"prompt_tokens": promptTokens, "completion_tokens": completionTokens, "total_tokens": promptTokens + completionTokens}})
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

func lastUserText(body map[string]any) string {
	messages, _ := body["messages"].([]any)
	for i := len(messages) - 1; i >= 0; i-- {
		message, _ := messages[i].(map[string]any)
		if message["role"] != "user" {
			continue
		}
		if text, ok := message["content"].(string); ok {
			return text
		}
		parts, _ := message["content"].([]any)
		var texts []string
		for _, part := range parts {
			if p, _ := part.(map[string]any); p["type"] == "text" {
				text, _ := p["text"].(string)
				texts = append(texts, text)
			}
		}
		return strings.Join(texts, " ")
	}
	return ""
}

func includeUsage(body map[string]any) bool {
	options, _ := body["stream_options"].(map[string]any)
	value, _ := options["include_usage"].(bool)
	return value
}

func words(text string) int { return len(strings.Fields(text)) }

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": message, "type": "api_error"}})
}

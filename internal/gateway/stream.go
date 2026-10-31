package gateway

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/mengxie/aegis/internal/pii"
	"github.com/mengxie/aegis/internal/usage"
)

// relayStream forwards Server-Sent Events from the provider, restoring personal data as it streams.
// A placeholder split across chunks ("[EMA" + "IL_1]") is held back until it is complete.
func (g *Gateway) relayStream(w http.ResponseWriter, resp *http.Response, vault *pii.Vault, x *exchange, clientWantsUsage bool, price usage.Price) {
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Aegis-Pii-Protected", strconv.Itoa(vault.Size()))
	w.WriteHeader(http.StatusOK)
	x.status = http.StatusOK

	send := func(data []byte) {
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		if flusher != nil {
			flusher.Flush()
		}
	}
	restorers := map[int]*pii.StreamRestorer{}
	restorer := func(index int) *pii.StreamRestorer {
		if restorers[index] == nil {
			restorers[index] = vault.NewStreamRestorer()
		}
		return restorers[index]
	}
	var last map[string]json.RawMessage

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			for index, r := range restorers { // anything still held back goes out before the end
				if rest := r.Flush(); rest != "" && last != nil {
					last["choices"] = mustJSON([]map[string]any{{"index": index, "delta": map[string]any{"content": rest}, "finish_reason": nil}})
					send(mustJSON(last))
				}
			}
			send([]byte("[DONE]"))
			break
		}

		var chunk map[string]json.RawMessage
		if json.Unmarshal([]byte(data), &chunk) != nil {
			send([]byte(data))
			continue
		}
		last = chunk
		hasUsage := len(chunk["usage"]) > 0 && string(chunk["usage"]) != "null"
		if hasUsage {
			x.promptTokens, x.completionTokens = readUsage(chunk["usage"])
			x.cost = price.Cost(x.promptTokens, x.completionTokens)
		}
		var choices []map[string]json.RawMessage
		_ = json.Unmarshal(chunk["choices"], &choices)
		if len(choices) == 0 && hasUsage && !clientWantsUsage {
			continue // Aegis asked for usage; the client did not, so keep the stream as it expects
		}
		if vault.Size() > 0 {
			for _, choice := range choices {
				var index int
				_ = json.Unmarshal(choice["index"], &index)
				var delta map[string]json.RawMessage
				if json.Unmarshal(choice["delta"], &delta) != nil {
					continue
				}
				var content string
				hasContent := json.Unmarshal(delta["content"], &content) == nil
				out := ""
				if hasContent {
					out = restorer(index).Push(content)
				}
				if finish := string(choice["finish_reason"]); finish != "" && finish != "null" {
					out += restorer(index).Flush()
				}
				if hasContent || out != "" {
					delta["content"] = mustJSON(out)
					choice["delta"] = mustJSON(delta)
				}
			}
			chunk["choices"] = mustJSON(choices)
		}
		send(mustJSON(chunk))
	}
}

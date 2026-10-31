package gateway

import (
	"encoding/json"
	"io"
)

// chatRequest keeps the client's JSON as raw fields, so parameters Aegis does not know about pass
// through untouched, and decodes only what policy needs.
type chatRequest struct {
	raw         map[string]json.RawMessage
	messages    []map[string]json.RawMessage
	model       string
	stream      bool
	temperature *float64
	maxTokens   *int
}

func parseChat(body io.Reader) (*chatRequest, *apiError) {
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(body).Decode(&raw); err != nil {
		return nil, invalidRequest("invalid_json", "The request body must be a JSON object.")
	}
	req := &chatRequest{raw: raw}
	if err := json.Unmarshal(raw["model"], &req.model); err != nil || req.model == "" {
		return nil, invalidRequest("missing_model", "Specify a model.")
	}
	if err := json.Unmarshal(raw["messages"], &req.messages); err != nil || len(req.messages) == 0 {
		return nil, invalidRequest("missing_messages", "Send at least one message.")
	}
	if value, ok := raw["stream"]; ok {
		_ = json.Unmarshal(value, &req.stream)
	}
	if value, ok := raw["temperature"]; ok {
		_ = json.Unmarshal(value, &req.temperature)
	}
	if value, ok := raw["max_tokens"]; ok {
		_ = json.Unmarshal(value, &req.maxTokens)
	}
	return req, nil
}

// eachText applies fn to every piece of text a message carries (plain string content, or the text
// parts of multi-part content), writing the results back into the request.
func (r *chatRequest) eachText(fn func(string) (string, *apiError)) *apiError {
	for _, message := range r.messages {
		var text string
		if json.Unmarshal(message["content"], &text) == nil {
			updated, err := fn(text)
			if err != nil {
				return err
			}
			message["content"] = mustJSON(updated)
			continue
		}
		var parts []map[string]json.RawMessage
		if json.Unmarshal(message["content"], &parts) != nil {
			continue
		}
		for _, part := range parts {
			var kind string
			if json.Unmarshal(part["type"], &kind) != nil || kind != "text" || json.Unmarshal(part["text"], &text) != nil {
				continue
			}
			updated, err := fn(text)
			if err != nil {
				return err
			}
			part["text"] = mustJSON(updated)
		}
		message["content"] = mustJSON(parts)
	}
	r.raw["messages"] = mustJSON(r.messages)
	return nil
}

func (r *chatRequest) set(field string, value any) { r.raw[field] = mustJSON(value) }

func mustJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err) // only ever called with values that came from JSON
	}
	return data
}

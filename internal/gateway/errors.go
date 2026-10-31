package gateway

import (
	"encoding/json"
	"net/http"
)

// apiError uses OpenAI's error shape, so existing SDKs surface Aegis decisions as ordinary API errors.
type apiError struct {
	status  int
	kind    string
	code    string
	message string
}

func (e *apiError) write(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"message": e.message, "type": e.kind, "code": e.code},
	})
}

func invalidRequest(code, message string) *apiError {
	return &apiError{http.StatusBadRequest, "invalid_request_error", code, message}
}

func forbidden(code, message string) *apiError {
	return &apiError{http.StatusForbidden, "permission_error", code, message}
}

func tooManyRequests(code, message string) *apiError {
	return &apiError{http.StatusTooManyRequests, "rate_limit_error", code, message}
}

var errUnauthorized = &apiError{http.StatusUnauthorized, "authentication_error", "invalid_api_key", "Send a valid Aegis API key as \"Authorization: Bearer aeg_...\"."}

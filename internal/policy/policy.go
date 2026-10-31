// Package policy decides what each team may do. Policies live in version-controlled config.
package policy

import (
	"fmt"
	"slices"
	"strings"
)

// PIIMode decides what happens to personal data before a prompt leaves the company.
type PIIMode string

const (
	// Pseudonymize swaps values for placeholders and restores them in the response (default).
	Pseudonymize PIIMode = "pseudonymize"
	// Redact swaps values for their category, irreversibly.
	Redact PIIMode = "redact"
	// Block rejects any request containing personal data.
	Block PIIMode = "block"
	// Allow sends personal data unchanged (for approved, contracted providers).
	Allow PIIMode = "allow"
)

type Policy struct {
	// Models the team may use; empty means all configured models.
	Models []string `json:"models"`
	// MaxTokens caps max_tokens on every request; 0 means no cap.
	MaxTokens int     `json:"maxTokens"`
	PII       PIIMode `json:"pii"`
	// BlockSecrets rejects prompts containing credentials. Defaults to true.
	BlockSecrets *bool `json:"blockSecrets"`
	// BlockedTerms rejects prompts mentioning, for example, confidential project code names.
	BlockedTerms      []string `json:"blockedTerms"`
	RequestsPerMinute int      `json:"requestsPerMinute"`
	MonthlyBudgetUSD  float64  `json:"monthlyBudgetUSD"`
	// CacheSeconds caches identical deterministic (temperature 0) requests; 0 disables caching.
	CacheSeconds int `json:"cacheSeconds"`
}

func (p *Policy) ApplyDefaults() {
	if p.PII == "" {
		p.PII = Pseudonymize
	}
	if p.BlockSecrets == nil {
		yes := true
		p.BlockSecrets = &yes
	}
}

func (p Policy) Validate(models map[string]bool) []error {
	var errs []error
	if !slices.Contains([]PIIMode{Pseudonymize, Redact, Block, Allow}, p.PII) {
		errs = append(errs, fmt.Errorf("pii must be pseudonymize, redact, block or allow, not %q", p.PII))
	}
	for _, model := range p.Models {
		if !models[model] {
			errs = append(errs, fmt.Errorf("allows unknown model %q", model))
		}
	}
	if p.MaxTokens < 0 || p.RequestsPerMinute < 0 || p.MonthlyBudgetUSD < 0 || p.CacheSeconds < 0 {
		errs = append(errs, fmt.Errorf("limits cannot be negative"))
	}
	return errs
}

func (p Policy) AllowsModel(model string) bool {
	return len(p.Models) == 0 || slices.Contains(p.Models, model)
}

// BlockedTerm returns the first blocked term found in text, case-insensitively.
func (p Policy) BlockedTerm(text string) (string, bool) {
	lower := strings.ToLower(text)
	for _, term := range p.BlockedTerms {
		if term != "" && strings.Contains(lower, strings.ToLower(term)) {
			return term, true
		}
	}
	return "", false
}

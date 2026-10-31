package pii

import (
	"fmt"
	"regexp"
	"strings"
)

var placeholder = regexp.MustCompile(`\[(EMAIL|PHONE|CARD|IP|IBAN|SECRET)_(\d+)\]`)

// maxPlaceholder is the longest placeholder the streaming restorer ever needs to hold back.
const maxPlaceholder = len("[SECRET_999999]")

// Vault holds the mapping between sensitive values and their placeholders for one request.
// The same value always gets the same placeholder, so the model can still tell "the same email"
// from "a different email" without seeing either.
type Vault struct {
	byValue map[string]string
	byToken map[string]string
	counts  map[Kind]int
}

func NewVault() *Vault {
	return &Vault{byValue: map[string]string{}, byToken: map[string]string{}, counts: map[Kind]int{}}
}

// Pseudonymize replaces every finding with a reversible placeholder.
func (v *Vault) Pseudonymize(text string) (string, []Finding) {
	findings := Scan(text)
	return replace(text, findings, func(f Finding) string {
		if token, ok := v.byValue[f.Value]; ok {
			return token
		}
		v.counts[f.Kind]++
		token := fmt.Sprintf("[%s_%d]", f.Kind, v.counts[f.Kind])
		v.byValue[f.Value] = token
		v.byToken[token] = f.Value
		return token
	}), findings
}

// Redact replaces every finding with its category, irreversibly.
func Redact(text string) (string, []Finding) {
	findings := Scan(text)
	return replace(text, findings, func(f Finding) string { return "[" + string(f.Kind) + "]" }), findings
}

// Restore puts the original values back. Placeholders the vault did not issue are left untouched.
func (v *Vault) Restore(text string) string {
	if len(v.byToken) == 0 {
		return text
	}
	return placeholder.ReplaceAllStringFunc(text, func(token string) string {
		if value, ok := v.byToken[token]; ok {
			return value
		}
		return token
	})
}

// Size is the number of distinct values in the vault.
func (v *Vault) Size() int { return len(v.byToken) }

func replace(text string, findings []Finding, substitute func(Finding) string) string {
	if len(findings) == 0 {
		return text
	}
	var b strings.Builder
	last := 0
	for _, f := range findings {
		b.WriteString(text[last:f.Start])
		b.WriteString(substitute(f))
		last = f.End
	}
	b.WriteString(text[last:])
	return b.String()
}

// StreamRestorer restores placeholders in streamed text, where a placeholder can be split across
// chunks ("[EMA" then "IL_1]"). It holds back only a trailing fragment that could still become a
// placeholder, so the stream keeps flowing with at most a few characters of delay.
type StreamRestorer struct {
	vault   *Vault
	pending string
}

func (v *Vault) NewStreamRestorer() *StreamRestorer { return &StreamRestorer{vault: v} }

// Push returns the text that is now safe to emit.
func (r *StreamRestorer) Push(chunk string) string {
	text := r.pending + chunk
	hold := heldFrom(text)
	r.pending = text[hold:]
	return r.vault.Restore(text[:hold])
}

// Flush returns whatever was held back. Call it when the stream ends.
func (r *StreamRestorer) Flush() string {
	out := r.vault.Restore(r.pending)
	r.pending = ""
	return out
}

// heldFrom returns the index of a trailing fragment that could be the start of a placeholder.
// Placeholders contain no '[' after the first character, so only the last '[' can start one.
func heldFrom(text string) int {
	i := strings.LastIndexByte(text, '[')
	if i < 0 {
		return len(text)
	}
	suffix := text[i:]
	if len(suffix) > maxPlaceholder || strings.ContainsRune(suffix, ']') || !couldBecomePlaceholder(suffix[1:]) {
		return len(text)
	}
	return i
}

func couldBecomePlaceholder(rest string) bool {
	name, number, hasUnderscore := strings.Cut(rest, "_")
	for _, kind := range Kinds {
		k := string(kind)
		if !hasUnderscore && strings.HasPrefix(k, name) {
			return true
		}
		if hasUnderscore && k == name {
			return strings.Trim(number, "0123456789") == ""
		}
	}
	return false
}

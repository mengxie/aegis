// Package pii finds personal data and secrets in text, and replaces them with placeholders that can
// be restored later, including in a token-by-token stream.
package pii

import (
	"net"
	"regexp"
	"sort"
	"strings"
)

// Kind is a category of sensitive data. It appears in placeholders such as [EMAIL_1].
type Kind string

const (
	Email  Kind = "EMAIL"
	Phone  Kind = "PHONE"
	Card   Kind = "CARD"
	IP     Kind = "IP"
	IBAN   Kind = "IBAN"
	Secret Kind = "SECRET"
)

// Kinds lists every category, in priority order when matches overlap.
var Kinds = []Kind{Secret, Email, IBAN, Card, IP, Phone}

// Finding is one match: Value is text[Start:End].
type Finding struct {
	Kind       Kind
	Start, End int
	Value      string
}

type detector struct {
	kind    Kind
	pattern *regexp.Regexp
	// valid rejects pattern matches that fail a checksum or sanity check, cutting false positives.
	valid func(string) bool
}

var detectors = []detector{
	{Secret, regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`), nil},
	{Secret, regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), nil},
	{Secret, regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}\b`), nil},
	{Secret, regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}\b`), nil},
	{Secret, regexp.MustCompile(`\bsk-(?:proj-|ant-)?[A-Za-z0-9_-]{20,}`), nil},
	{Secret, regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), nil},
	{Email, regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`), nil},
	{IBAN, regexp.MustCompile(`\b[A-Z]{2}\d{2}(?: ?[A-Z0-9]{4}){2,7}(?: ?[A-Z0-9]{1,4})?\b`), validIBAN},
	{Card, regexp.MustCompile(`\b\d(?:[ -]?\d){12,18}\b`), validCard},
	{IP, regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`), func(s string) bool { return net.ParseIP(s) != nil }},
	{Phone, regexp.MustCompile(`(?:\+\d{1,3}[ .-]?)?(?:\(\d{1,4}\)[ .-]?)?\d{2,4}(?:[ .-]?\d{3,4}){1,2}\b`), validPhone},
}

// Scan returns non-overlapping findings in order of appearance. When matches overlap, the one that
// starts first wins, then the longer one, then the higher-priority kind.
func Scan(text string) []Finding {
	var all []Finding
	for _, d := range detectors {
		for _, loc := range d.pattern.FindAllStringIndex(text, -1) {
			value := text[loc[0]:loc[1]]
			if d.valid != nil && !d.valid(value) {
				continue
			}
			if d.kind == Phone && partOfLongerNumber(text, loc[0], loc[1]) {
				continue // e.g. the first groups of a card number that failed its checksum
			}
			all = append(all, Finding{Kind: d.kind, Start: loc[0], End: loc[1], Value: value})
		}
	}
	rank := func(k Kind) int {
		for i, kind := range Kinds {
			if kind == k {
				return i
			}
		}
		return len(Kinds)
	}
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.Start != b.Start {
			return a.Start < b.Start
		}
		if a.End-a.Start != b.End-b.Start {
			return a.End-a.Start > b.End-b.Start
		}
		return rank(a.Kind) < rank(b.Kind)
	})
	var kept []Finding
	end := -1
	for _, f := range all {
		if f.Start >= end {
			kept = append(kept, f)
			end = f.End
		}
	}
	return kept
}

func digits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// validCard applies the Luhn checksum that every payment card number satisfies.
func validCard(s string) bool {
	d := digits(s)
	if len(d) < 13 || len(d) > 19 {
		return false
	}
	sum := 0
	for i := len(d) - 1; i >= 0; i-- {
		n := int(d[i] - '0')
		if (len(d)-1-i)%2 == 1 {
			n *= 2
			if n > 9 {
				n -= 9
			}
		}
		sum += n
	}
	return sum%10 == 0
}

// validIBAN applies the ISO 13616 mod-97 check.
func validIBAN(s string) bool {
	compact := strings.ReplaceAll(s, " ", "")
	if len(compact) < 15 || len(compact) > 34 {
		return false
	}
	rearranged := compact[4:] + compact[:4]
	remainder := 0
	for _, r := range rearranged {
		switch {
		case r >= '0' && r <= '9':
			remainder = (remainder*10 + int(r-'0')) % 97
		case r >= 'A' && r <= 'Z':
			remainder = (remainder*100 + int(r-'A'+10)) % 97
		default:
			return false
		}
	}
	return remainder == 1
}

// validPhone requires 8-15 digits and some phone-like punctuation, so plain numbers in prose or
// code (order ids, timestamps) are not mistaken for phone numbers.
func validPhone(s string) bool {
	d := digits(s)
	return len(d) >= 8 && len(d) <= 15 && strings.ContainsAny(s, " .-()+")
}

// partOfLongerNumber reports whether text[start:end] continues a digit sequence on either side,
// allowing one separator: "1234 5678 9012" inside "1234 5678 9012 3456" is not a phone number.
func partOfLongerNumber(text string, start, end int) bool {
	isDigit := func(i int) bool { return i >= 0 && i < len(text) && text[i] >= '0' && text[i] <= '9' }
	isSeparator := func(i int) bool { return i >= 0 && i < len(text) && strings.IndexByte(" .-", text[i]) >= 0 }
	before := isDigit(start-1) || (isSeparator(start-1) && isDigit(start-2))
	after := isDigit(end) || (isSeparator(end) && isDigit(end+1))
	return before || after
}

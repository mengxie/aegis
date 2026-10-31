// Package usage prices requests and keeps the spend ledger and request log.
package usage

import (
	"context"
	"math"
	"sync"
	"time"
)

// NanoUSD is billionths of a US dollar. Integer money cannot drift over millions of requests.
type NanoUSD int64

func FromDollars(dollars float64) NanoUSD { return NanoUSD(math.Round(dollars * 1e9)) }

func (n NanoUSD) Dollars() float64 { return float64(n) / 1e9 }

// Price is in US dollars per million tokens, as providers publish it.
type Price struct {
	InputPerMTok  float64 `json:"inputPerMTok"`
	OutputPerMTok float64 `json:"outputPerMTok"`
}

// Cost: $1 per million tokens is exactly 1,000 nano-dollars per token.
func (p Price) Cost(promptTokens, completionTokens int) NanoUSD {
	return NanoUSD(math.Round(float64(promptTokens)*p.InputPerMTok*1000 + float64(completionTokens)*p.OutputPerMTok*1000))
}

// Record is one request in the audit log. It never contains prompt or completion text.
type Record struct {
	RequestID        string
	At               time.Time
	Team             string
	KeyID            string
	Model            string
	Upstream         string
	Status           int
	PromptTokens     int
	CompletionTokens int
	Cost             NanoUSD
	Cached           bool
	PII              map[string]int
	Decision         string
	Latency          time.Duration
}

// Month is the budget period for a moment, in UTC.
func Month(t time.Time) string { return t.UTC().Format("2006-01") }

// Store keeps team spend and the request log. Implementations must make Record atomic.
type Store interface {
	Spent(ctx context.Context, team, month string) (NanoUSD, error)
	// Record logs a request and adds its cost to the team's month, returning the new total.
	Record(ctx context.Context, record Record) (NanoUSD, error)
	Ping(ctx context.Context) error
}

// MemoryStore is for development, tests and single-instance deployments.
type MemoryStore struct {
	mu       sync.Mutex
	spend    map[string]NanoUSD
	Requests []Record
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{spend: map[string]NanoUSD{}} }

func (s *MemoryStore) Spent(_ context.Context, team, month string) (NanoUSD, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spend[team+"/"+month], nil
}

func (s *MemoryStore) Record(_ context.Context, record Record) (NanoUSD, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := record.Team + "/" + Month(record.At)
	s.spend[key] += record.Cost
	s.Requests = append(s.Requests, record)
	return s.spend[key], nil
}

func (s *MemoryStore) Ping(context.Context) error { return nil }

// Snapshot returns a copy of the request log, safe to read while requests continue.
func (s *MemoryStore) Snapshot() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Record(nil), s.Requests...)
}

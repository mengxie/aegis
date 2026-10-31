package usage

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"
)

func TestCostIsExactIntegerMoney(t *testing.T) {
	price := Price{InputPerMTok: 2.5, OutputPerMTok: 10}
	if got := price.Cost(1_000_000, 0); got != FromDollars(2.5) {
		t.Fatalf("1M prompt tokens cost %d, want $2.50", got)
	}
	if got := price.Cost(1, 1); got != 12_500 {
		t.Fatalf("one token each cost %d nano-dollars, want 12500", got)
	}
	var total NanoUSD
	for range 1_000_000 {
		total += Price{InputPerMTok: 0.15}.Cost(1, 0)
	}
	if total != FromDollars(0.15) {
		t.Fatalf("a million tiny charges drifted to %d", total)
	}
}

func TestMemoryStoreAccumulatesPerTeamAndMonth(t *testing.T) {
	store := NewMemoryStore()
	at := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	ctx := context.Background()
	_, _ = store.Record(ctx, Record{Team: "support", At: at, Cost: 100})
	total, _ := store.Record(ctx, Record{Team: "support", At: at, Cost: 50})
	_, _ = store.Record(ctx, Record{Team: "support", At: at.AddDate(0, 1, 0), Cost: 7})
	if spent, _ := store.Spent(ctx, "support", "2026-10"); total != 150 || spent != 150 {
		t.Fatalf("total %d, spent %d", total, spent)
	}
}

func TestPostgresStoreNeverLosesConcurrentSpend(t *testing.T) {
	dsn := os.Getenv("AEGIS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set AEGIS_TEST_DATABASE_URL to run")
	}
	ctx := context.Background()
	store, err := OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := OpenPostgres(ctx, dsn); err != nil { // migrations are idempotent
		t.Fatal(err)
	}
	team := "team-" + time.Now().Format("150405.000000")
	var wg sync.WaitGroup
	for i := range 25 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.Record(ctx, Record{RequestID: team + "-" + string(rune('a'+i)), Team: team, At: time.Now(), Model: "m", Cost: 40, PII: map[string]int{"EMAIL": 1}})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if spent, err := store.Spent(ctx, team, Month(time.Now())); err != nil || spent != 1000 {
		t.Fatalf("spent %d (%v), want 1000", spent, err)
	}
}

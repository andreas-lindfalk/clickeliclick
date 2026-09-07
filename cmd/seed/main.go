// Command seed fills the events table with synthetic data so the queries in
// later steps have something to chew on.
//
// It generates rows column by column and sends them in large batches, which is
// the shape ClickHouse likes: each INSERT becomes one part on disk, and the
// server merges parts in the background. Try a small -batch and watch
// system.parts grow (and eventually hit "too many parts").
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"time"

	"clickeliclick/internal/app"
	"clickeliclick/internal/pkg/clickhouse"
)

var (
	eventTypes = []string{"view", "view", "view", "view", "view", "view", "click", "click", "click", "purchase"}
	countries  = []string{"SE", "SE", "SE", "NO", "DK", "FI", "DE", "DE", "US", "US"}
	plans      = []string{"free", "free", "free", "pro", "team"}
)

func main() {
	rows := flag.Int("rows", 5_000_000, "total rows to insert")
	batch := flag.Int("batch", 500_000, "rows per INSERT (one part each)")
	users := flag.Int("users", 100_000, "number of distinct user ids")
	days := flag.Int("days", 30, "spread timestamps over the last N days")
	seed := flag.Uint64("seed", 42, "random seed, for reproducible data")
	flag.Parse()

	ctx := context.Background()
	cfg := clickhouse.ConfigFromEnv()
	if err := clickhouse.Migrate(ctx, cfg); err != nil {
		log.Fatal(err)
	}
	ch, err := clickhouse.New(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer ch.Close()
	repo := app.NewRepository(ch)

	rng := rand.New(rand.NewPCG(*seed, 0))
	end := time.Now()
	start := end.Add(-time.Duration(*days) * 24 * time.Hour)
	span := end.Sub(start)

	t0 := time.Now()

	// One users row per id, so events have something to look up.
	us := make([]app.User, 0, *users)
	for id := 1; id <= *users; id++ {
		us = append(us, app.User{UserID: uint64(id), Country: countries[rng.IntN(len(countries))], Plan: plans[rng.IntN(len(plans))]})
	}
	if err := repo.UpsertUsers(ctx, us); err != nil {
		log.Fatal(err)
	}
	if err := repo.ReloadUserLookup(ctx); err != nil {
		log.Fatal(err)
	}
	log.Printf("upserted %d users", len(us))

	for done := 0; done < *rows; {
		n := min(*batch, *rows-done)
		cols := app.EventColumns{
			TS:        make([]time.Time, n),
			UserID:    make([]uint64, n),
			EventType: make([]string, n),
			Payload:   make([]string, n),
		}
		for i := range n {
			cols.TS[i] = start.Add(time.Duration(rng.Int64N(int64(span))))
			cols.UserID[i] = uint64(rng.IntN(*users)) + 1
			cols.EventType[i] = eventTypes[rng.IntN(len(eventTypes))]
			cols.Payload[i] = fmt.Sprintf(`{"page":"/product/%d","ref":"%s"}`, rng.IntN(1000), ref(rng))
		}
		if err := repo.InsertEventColumns(ctx, cols); err != nil {
			log.Fatal(err)
		}
		done += n
		log.Printf("inserted %d/%d rows", done, *rows)
	}

	elapsed := time.Since(t0)
	log.Printf("done: %d rows in %s (%.0f rows/s)", *rows, elapsed.Round(time.Millisecond), float64(*rows)/elapsed.Seconds())
}

func ref(rng *rand.Rand) string {
	switch rng.IntN(4) {
	case 0:
		return "google"
	case 1:
		return "newsletter"
	default:
		return "direct"
	}
}

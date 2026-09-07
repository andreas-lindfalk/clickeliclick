package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"clickeliclick/internal/app"
	"clickeliclick/internal/pkg/clickhouse"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

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

	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	// POST /events with a JSON array of events, e.g.
	// [{"user_id":1,"event_type":"click","payload":"{}"}]
	mux.HandleFunc("POST /events", func(w http.ResponseWriter, r *http.Request) {
		var events []app.Event
		if err := json.NewDecoder(r.Body).Decode(&events); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := repo.InsertEvents(r.Context(), events); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})

	// POST /event with a single JSON event. Each request is its own INSERT;
	// ClickHouse buffers them server-side (async_insert) and flushes one part.
	mux.HandleFunc("POST /event", func(w http.ResponseWriter, r *http.Request) {
		var e app.Event
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := repo.InsertEventAsync(r.Context(), e); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})

	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		events, err := repo.RecentEvents(r.Context(), 50)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, events)
	})

	mux.HandleFunc("GET /users/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid user id", http.StatusBadRequest)
			return
		}
		events, err := repo.RecentEventsByUser(r.Context(), id, 50)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, events)
	})

	// GET /pages?ref=google — top pages for one referrer, grouped on a JSON path.
	mux.HandleFunc("GET /pages", func(w http.ResponseWriter, r *http.Request) {
		ref := r.URL.Query().Get("ref")
		if ref == "" {
			http.Error(w, "ref is required", http.StatusBadRequest)
			return
		}
		pages, err := repo.TopPagesByRef(r.Context(), ref, 10)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, pages)
	})

	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, r *http.Request) {
		counts, err := repo.CountByType(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, counts)
	})

	httpAddr := os.Getenv("HTTP_ADDR")
	if httpAddr == "" {
		httpAddr = ":8080"
	}
	// GET /stats/minutes?from=RFC3339&to=RFC3339, defaulting to the last hour.
	// Served from the events_per_minute rollup, not the raw table.
	mux.HandleFunc("GET /stats/minutes", func(w http.ResponseWriter, r *http.Request) {
		to, from := time.Now(), time.Now().Add(-time.Hour)
		var err error
		if v := r.URL.Query().Get("from"); v != "" {
			if from, err = time.Parse(time.RFC3339, v); err != nil {
				http.Error(w, "from: "+err.Error(), http.StatusBadRequest)
				return
			}
		}
		if v := r.URL.Query().Get("to"); v != "" {
			if to, err = time.Parse(time.RFC3339, v); err != nil {
				http.Error(w, "to: "+err.Error(), http.StatusBadRequest)
				return
			}
		}
		stats, err := repo.StatsPerMinute(r.Context(), from, to)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, stats)
	})

	srv := &http.Server{Addr: httpAddr, Handler: mux}

	go func() {
		log.Printf("listening on %s", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

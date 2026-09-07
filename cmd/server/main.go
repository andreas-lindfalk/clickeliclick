package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"clickeliclick/internal/app"
	"clickeliclick/internal/pkg/clickhouse"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ch, err := clickhouse.New(ctx,
		env("CLICKHOUSE_ADDR", "localhost:9000"),
		env("CLICKHOUSE_DB", "poc"),
		env("CLICKHOUSE_USER", "default"),
		env("CLICKHOUSE_PASSWORD", ""),
	)
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

	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		events, err := repo.RecentEvents(r.Context(), 50)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, events)
	})

	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, r *http.Request) {
		counts, err := repo.CountByType(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, counts)
	})

	srv := &http.Server{Addr: env("HTTP_ADDR", ":8080"), Handler: mux}

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

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"docker-watchdog/internal/storage"
)

// New returns a read-only API with bounded request lifetimes. Bind to loopback
// unless an authenticated reverse proxy protects access to container metadata.
func New(address string, store *storage.Store) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := store.Ping(r.Context()); err != nil {
			http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/containers", func(w http.ResponseWriter, r *http.Request) {
		events, err := store.Current(r.Context())
		if err != nil {
			http.Error(w, "could not read container observations", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, events)
	})
	mux.HandleFunc("GET /api/events", func(w http.ResponseWriter, r *http.Request) {
		limit := 100
		if value := r.URL.Query().Get("limit"); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 1 || parsed > 500 {
				http.Error(w, "limit must be between 1 and 500", http.StatusBadRequest)
				return
			}
			limit = parsed
		}
		var before int64
		if value := r.URL.Query().Get("before"); value != "" {
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil || parsed < 1 {
				http.Error(w, "before must be a positive event sequence", http.StatusBadRequest)
				return
			}
			before = parsed
		}
		events, err := store.History(r.Context(), r.URL.Query().Get("container"), before, limit)
		if err != nil {
			http.Error(w, "could not read incident history", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, events)
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		events, err := store.Current(r.Context())
		if err != nil {
			http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		fmt.Fprintln(w, "# TYPE watchdog_containers gauge")
		fmt.Fprintf(w, "watchdog_containers %d\n", len(events))
		fmt.Fprintln(w, "# TYPE watchdog_recovery_attempts gauge")
		fmt.Fprintln(w, "# TYPE watchdog_cpu_percent gauge")
		fmt.Fprintln(w, "# TYPE watchdog_memory_bytes gauge")
		fmt.Fprintln(w, "# TYPE watchdog_observation_timestamp_seconds gauge")
		for _, event := range events {
			fmt.Fprintf(w, "watchdog_recovery_attempts{container_id=%q} %d\n", event.ID, event.Attempts)
			fmt.Fprintf(w, "watchdog_observation_timestamp_seconds{container_id=%q} %d\n", event.ID, event.Time.Unix())
			if event.StatsOK {
				fmt.Fprintf(w, "watchdog_cpu_percent{container_id=%q} %g\n", event.ID, event.CPUPercent)
				fmt.Fprintf(w, "watchdog_memory_bytes{container_id=%q} %d\n", event.ID, event.MemoryBytes)
			}
		}
	})
	return &http.Server{
		Addr:              address,
		Handler:           http.TimeoutHandler(mux, 5*time.Second, "request timed out"),
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(value)
}

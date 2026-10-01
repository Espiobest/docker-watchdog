package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"docker-watchdog/internal/storage"
	"docker-watchdog/internal/watchdog"
)

func TestReadOnlyAPI(t *testing.T) {
	store, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	event := watchdog.Event{
		Time:     time.Now(),
		Sample:   watchdog.Sample{ID: "abc", Name: "demo", CPUPercent: 12.5, StatsOK: true},
		Decision: watchdog.Decision{Status: watchdog.StatusHealthy},
	}
	if err := store.Record(context.Background(), event, nil); err != nil {
		t.Fatal(err)
	}
	server := New("", store)

	for _, test := range []struct {
		method string
		path   string
		status int
	}{
		{"GET", "/healthz", 200},
		{"GET", "/api/containers", 200},
		{"GET", "/api/events?container=abc&limit=1", 200},
		{"GET", "/metrics", 200},
		{"GET", "/api/events?limit=0", 400},
		{"GET", "/api/events?limit=501", 400},
		{"GET", "/api/events?limit=nope", 400},
		{"GET", "/api/events?before=-1", 400},
		{"POST", "/api/containers", 405},
		{"POST", "/api/restart", 404},
	} {
		t.Run(test.method+test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			server.Handler.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
			if response.Code != test.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if test.path == "/api/containers" && test.method == http.MethodGet {
				var events []watchdog.Event
				if err := json.Unmarshal(response.Body.Bytes(), &events); err != nil || len(events) != 1 || events[0].ID != "abc" {
					t.Fatalf("invalid container response: %s", response.Body.String())
				}
			}
			if test.path == "/metrics" && !strings.Contains(response.Body.String(), `watchdog_cpu_percent{container_id="abc"} 12.5`) {
				t.Fatal("CPU metric missing")
			}
		})
	}
}

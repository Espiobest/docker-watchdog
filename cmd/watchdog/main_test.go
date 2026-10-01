package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDockerDiscoveryTimeoutIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer server.Close()

	previous := os.Args
	t.Cleanup(func() { os.Args = previous })
	os.Args = []string{
		"watchdog", "--output=logs", "--listen=", "--api-timeout=100ms",
		"--host=" + server.URL, "--db=" + filepath.Join(t.TempDir(), "startup.db"),
	}
	if err := run(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("startup API timeout must fail, got %v", err)
	}
}

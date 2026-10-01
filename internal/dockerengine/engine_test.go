package dockerengine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"docker-watchdog/internal/watchdog"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// Exercise the actual SDK against a fake HTTP daemon, not only the Engine fake.
func TestSDKRoundTrip(t *testing.T) {
	restarted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			if r.URL.Query().Get("all") != "1" || !strings.Contains(r.URL.Query().Get("filters"), "watchdog.demo=true") {
				t.Errorf("missing list options: %s", r.URL.RawQuery)
			}
			w.Write([]byte(`[{"Id":"abc","Names":["/demo"],"State":"running"}]`))
		case strings.HasSuffix(r.URL.Path, "/containers/abc/json"):
			w.Write([]byte(`{"Id":"abc","Name":"/demo","RestartCount":2,"State":{"Status":"running","ExitCode":0,"StartedAt":"2026-01-01T00:00:00Z","Health":{"Status":"unhealthy"}},"HostConfig":{"RestartPolicy":{"Name":"no"}}}`))
		case strings.HasSuffix(r.URL.Path, "/containers/abc/stats"):
			if r.URL.Query().Get("stream") != "false" {
				t.Error("stats request should not stream indefinitely")
			}
			json.NewEncoder(w).Encode(map[string]any{
				"cpu_stats":    map[string]any{"cpu_usage": map[string]any{"total_usage": 300}, "system_cpu_usage": 1000, "online_cpus": 2},
				"precpu_stats": map[string]any{"cpu_usage": map[string]any{"total_usage": 100}, "system_cpu_usage": 500},
				"memory_stats": map[string]any{"usage": 4096, "limit": 8192, "stats": map[string]any{"inactive_file": 1024}},
				"networks":     map[string]any{"eth0": map[string]any{"rx_bytes": 100, "tx_bytes": 200}},
			})
		case strings.HasSuffix(r.URL.Path, "/containers/abc/restart"):
			if r.Method != http.MethodPost || r.URL.Query().Get("t") != "10" {
				t.Error("incorrect restart request")
			}
			restarted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cli, err := client.New(client.WithHost(server.URL), client.WithAPIVersion("1.52"))
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	engine := &Engine{Client: cli, Label: "watchdog.demo=true"}
	ctx := context.Background()
	list, err := engine.List(ctx)
	if err != nil || len(list) != 1 || list[0].Name != "demo" {
		t.Fatalf("list=%+v err=%v", list, err)
	}
	sample, err := engine.Inspect(ctx, "abc")
	if err != nil || sample.Health != container.Unhealthy || sample.RestartCount != 2 {
		t.Fatalf("sample=%+v err=%v", sample, err)
	}
	if err := engine.Stats(ctx, &sample); err != nil {
		t.Fatal(err)
	}
	if sample.CPUPercent != 80 || sample.MemoryBytes != 3072 || sample.NetworkRX != 100 || sample.NetworkTX != 200 {
		t.Fatalf("incorrect metrics: %+v", sample)
	}
	if err := engine.Restart(ctx, "abc"); err != nil || !restarted {
		t.Fatalf("restart=%t err=%v", restarted, err)
	}
}

func TestCounterResetDoesNotUnderflow(t *testing.T) {
	var stats container.StatsResponse
	stats.CPUStats.CPUUsage.TotalUsage = 10
	stats.PreCPUStats.CPUUsage.TotalUsage = 100
	stats.CPUStats.SystemUsage = 1000
	stats.PreCPUStats.SystemUsage = 500
	stats.CPUStats.OnlineCPUs = 2
	stats.MemoryStats.Usage = 10
	stats.MemoryStats.Stats = map[string]uint64{"inactive_file": 100}
	var sample watchdog.Sample
	applyStats(&sample, stats)
	if sample.CPUPercent != 0 || sample.MemoryBytes != 10 {
		t.Fatalf("counter underflow: %+v", sample)
	}
}

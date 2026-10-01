package dockerengine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/moby/moby/client"
)

func TestStartAndStopSDKRequests(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if strings.HasSuffix(r.URL.Path, "/stop") && r.URL.Query().Get("t") != "10" {
			t.Error("stop must request a ten-second grace period")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	cli, err := client.New(client.WithHost(server.URL), client.WithAPIVersion("1.52"))
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	engine := &Engine{Client: cli}
	if err := engine.Start(context.Background(), "full-id"); err != nil {
		t.Fatal(err)
	}
	if err := engine.Stop(context.Background(), "full-id"); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || !strings.HasSuffix(paths[0], "/containers/full-id/start") || !strings.HasSuffix(paths[1], "/containers/full-id/stop") {
		t.Fatalf("requests = %v", paths)
	}
}

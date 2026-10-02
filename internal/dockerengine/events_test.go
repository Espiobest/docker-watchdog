package dockerengine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

func TestEventsFilterLifecycleAndCancel(t *testing.T) {
	closed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/events") {
			t.Errorf("path %s", r.URL.Path)
		}
		filters := r.URL.Query().Get("filters")
		if !strings.Contains(filters, "container") || !strings.Contains(filters, "watchdog.test=true") {
			t.Errorf("filters %s", filters)
		}
		w.Header().Set("Content-Type", "application/json")
		for _, action := range []string{"exec_create", "start", "health_status: unhealthy", "destroy"} {
			json.NewEncoder(w).Encode(map[string]any{"Type": "container", "Action": action, "Actor": map[string]string{"ID": "abc"}})
		}
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(closed)
	}))
	defer server.Close()
	cli, err := client.New(client.WithHost(server.URL), client.WithAPIVersion("1.52"))
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var ids []string
	err = (&Engine{Client: cli, Label: "watchdog.test=true"}).WatchEvents(ctx, func(id string) {
		ids = append(ids, id)
		if len(ids) == 4 {
			cancel()
		}
	})
	if err == nil || len(ids) != 4 || ids[0] != "" {
		t.Fatalf("ids=%v error=%v", ids, err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("event connection leaked")
	}
}

package display

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"docker-watchdog/internal/watchdog"
)

func TestLogsDeduplicateButKeepActions(t *testing.T) {
	var output bytes.Buffer
	logger := New("logs", &output)
	event := watchdog.Event{Time: time.Now(), Sample: watchdog.Sample{ID: "a", Name: "demo\nspoof"}, Decision: watchdog.Decision{Status: watchdog.StatusHealthy}}
	for range 2 {
		if err := logger.Accept(event); err != nil {
			t.Fatal(err)
		}
	}
	event.Action = watchdog.ActionRestarted
	if err := logger.Accept(event); err != nil {
		t.Fatal(err)
	}
	if strings.Count(output.String(), "\n") != 2 || !strings.Contains(output.String(), "action=restarted") {
		t.Fatalf("unexpected logs: %s", output.String())
	}
}

func TestJSONIncludesTypedStatuses(t *testing.T) {
	var output bytes.Buffer
	event := watchdog.Event{Decision: watchdog.Decision{Status: watchdog.StatusBackoff}}
	if err := New("json", &output).Accept(event); err != nil {
		t.Fatal(err)
	}
	var decoded watchdog.Event
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil || decoded.Status != event.Status {
		t.Fatalf("JSON=%s err=%v", output.String(), err)
	}
}

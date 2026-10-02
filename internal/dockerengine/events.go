package dockerengine

import (
	"context"
	"io"
	"strings"
	"time"

	"github.com/moby/moby/client"
)

func (e *Engine) WatchEvents(ctx context.Context, notify func(string)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Bound connection setup, but allow an established stream to stay idle.
	timer := time.AfterFunc(5*time.Second, cancel)
	defer timer.Stop()
	filters := make(client.Filters).Add("type", "container")
	if e.Label != "" {
		filters = filters.Add("label", e.Label)
	}
	result := e.Client.Events(ctx, client.EventsListOptions{Filters: filters})
	timer.Stop()
	select {
	case err := <-result.Err:
		if err == nil {
			return io.EOF
		}
		return err
	default:
	}
	notify("")
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-result.Err:
			if err == nil {
				return io.EOF
			}
			return err
		case event, ok := <-result.Messages:
			if !ok {
				return io.EOF
			}
			action := string(event.Action)
			switch action {
			case "create", "start", "stop", "die", "destroy", "restart", "pause", "unpause", "rename", "update", "oom":
				notify(event.Actor.ID)
			default:
				if strings.HasPrefix(action, "health_status") {
					notify(event.Actor.ID)
				}
			}
		}
	}
}

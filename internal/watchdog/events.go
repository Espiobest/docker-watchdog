package watchdog

import (
	"context"
	"io"
	"time"
)

// EventSource supplies refresh hints, not authoritative lifecycle state.
// An empty ID signals a new connection and requests full reconciliation.
type EventSource interface {
	WatchEvents(context.Context, func(string)) error
}

type engineNotice struct {
	id  string
	err error
}

// watchEvents reconnects without holding a polling API slot. Hints may be
// coalesced under load; periodic discovery and polling remain authoritative.
func watchEvents(ctx context.Context, source EventSource, notices chan<- engineNotice) {
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := source.WatchEvents(ctx, func(id string) {
			if id == "" {
				select {
				case notices <- engineNotice{}:
				case <-ctx.Done():
				}
				return
			}
			select {
			case notices <- engineNotice{id: id}:
			default:
			}
		})
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			err = io.EOF
		}
		select {
		case notices <- engineNotice{err: err}:
		case <-ctx.Done():
			return
		}
		if time.Since(started) >= time.Minute {
			backoff = time.Second
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff = min(30*time.Second, backoff*2)
	}
}

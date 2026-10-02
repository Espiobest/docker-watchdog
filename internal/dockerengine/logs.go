package dockerengine

import (
	"context"
	"io"
	"time"
	"unicode/utf8"

	"docker-watchdog/internal/watchdog"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

// Logs follows the most recent 200 lines. Cancellation closes the SDK body.
func (e *Engine) Logs(ctx context.Context, id string, emit func(watchdog.LogLine) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	timer := time.AfterFunc(5*time.Second, cancel)
	defer timer.Stop()
	item, err := e.Client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return err
	}
	body, err := e.Client.ContainerLogs(ctx, id, client.ContainerLogsOptions{
		ShowStdout: true, ShowStderr: true, Follow: true, Tail: "200", Timestamps: true,
	})
	if err != nil {
		return err
	}
	timer.Stop()
	defer body.Close()
	if err := emit(watchdog.LogLine{Ready: true}); err != nil {
		return err
	}
	stdout := &logWriter{stream: "out", emit: emit}
	stderr := &logWriter{stream: "err", emit: emit}
	if item.Container.Config != nil && item.Container.Config.Tty {
		stdout.stream = "tty"
		_, err = io.Copy(stdout, body)
	} else {
		_, err = stdcopy.StdCopy(stdout, stderr, body)
	}
	if flushErr := stdout.flush(); err == nil {
		err = flushErr
	}
	if flushErr := stderr.flush(); err == nil {
		err = flushErr
	}
	return err
}

// Docker frames need not end at newlines. Keep partial lines across writes,
// splitting very long lines into chunks so neither storage nor UI grows unbounded.
type logWriter struct {
	stream string
	buffer []byte
	emit   func(watchdog.LogLine) error
}

func (w *logWriter) Write(p []byte) (int, error) {
	for i, b := range p {
		if b == '\n' {
			if err := w.send(); err != nil {
				return i + 1, err
			}
			continue
		}
		// Finish the UTF-8 rune before splitting; still bound malformed input.
		if len(w.buffer) >= 4096 && (utf8.RuneStart(b) || len(w.buffer) >= 4099) {
			if err := w.send(); err != nil {
				return i, err
			}
		}
		w.buffer = append(w.buffer, b)
	}
	return len(p), nil
}

func (w *logWriter) send() error {
	err := w.emit(watchdog.LogLine{Stream: w.stream, Text: string(w.buffer)})
	w.buffer = w.buffer[:0]
	return err
}

func (w *logWriter) flush() error {
	if len(w.buffer) == 0 {
		return nil
	}
	return w.send()
}

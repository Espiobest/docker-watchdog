package dockerengine

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"docker-watchdog/internal/watchdog"
	"github.com/moby/moby/client"
)

func TestLogsDecodeTTYAndMultiplexedStreams(t *testing.T) {
	for _, tty := range []bool{false, true} {
		t.Run(map[bool]string{false: "multiplexed", true: "tty"}[tty], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/json") {
					json.NewEncoder(w).Encode(map[string]any{"Config": map[string]bool{"Tty": tty}})
					return
				}
				q := r.URL.Query()
				if q.Get("tail") != "200" || q.Get("follow") != "1" || q.Get("stdout") != "1" || q.Get("stderr") != "1" || q.Get("timestamps") != "1" {
					t.Errorf("options: %v", q)
				}
				if tty {
					io.WriteString(w, "hello\nlast")
					return
				}
				frame := func(stream byte, text string) {
					header := make([]byte, 8)
					header[0] = stream
					binary.BigEndian.PutUint32(header[4:], uint32(len(text)))
					w.Write(header)
					io.WriteString(w, text)
				}
				frame(1, "hel")
				frame(1, "lo\n")
				frame(2, "problem\n")
				frame(1, "last")
			}))
			defer server.Close()
			cli, err := client.New(client.WithHost(server.URL), client.WithAPIVersion("1.52"))
			if err != nil {
				t.Fatal(err)
			}
			defer cli.Close()
			var lines []watchdog.LogLine
			err = (&Engine{Client: cli}).Logs(context.Background(), "abc", func(line watchdog.LogLine) error {
				if !line.Ready {
					lines = append(lines, line)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(lines) < 2 || lines[0].Text != "hello" || lines[len(lines)-1].Text != "last" {
				t.Fatalf("lines: %+v", lines)
			}
			if tty && lines[0].Stream != "tty" {
				t.Fatal("TTY not identified")
			}
			if !tty && (len(lines) != 3 || lines[1].Stream != "err") {
				t.Fatalf("stderr lost: %+v", lines)
			}
		})
	}
}

func TestLogWriterBoundsLongLinesAndPropagatesConsumerError(t *testing.T) {
	var sizes []int
	w := &logWriter{emit: func(line watchdog.LogLine) error { sizes = append(sizes, len(line.Text)); return nil }}
	if _, err := io.WriteString(w, strings.Repeat("x", 10000)); err != nil {
		t.Fatal(err)
	}
	if err := w.flush(); err != nil {
		t.Fatal(err)
	}
	if len(sizes) != 3 || sizes[0] != 4096 || sizes[2] != 1808 {
		t.Fatalf("chunk sizes: %v", sizes)
	}
	w.emit = func(watchdog.LogLine) error { return io.ErrClosedPipe }
	if _, err := io.WriteString(w, "stop\n"); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("error=%v", err)
	}
}

func TestIdleLogsCancelClosesConnection(t *testing.T) {
	connected := make(chan struct{})
	closed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/json") {
			io.WriteString(w, `{"Config":{"Tty":true}}`)
			return
		}
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		close(connected)
		<-r.Context().Done()
		close(closed)
	}))
	defer server.Close()
	cli, err := client.New(client.WithHost(server.URL), client.WithAPIVersion("1.52"))
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- (&Engine{Client: cli}).Logs(ctx, "abc", func(watchdog.LogLine) error { return nil }) }()
	select {
	case <-connected:
	case <-time.After(3 * time.Second):
		t.Fatal("not connected")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("logs did not cancel")
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("connection leaked")
	}
}

func TestLogChunksPreserveUnicode(t *testing.T) {
	var chunks []string
	w := &logWriter{emit: func(line watchdog.LogLine) error { chunks = append(chunks, line.Text); return nil }}
	text := strings.Repeat("界", 2000)
	for _, b := range []byte(text) {
		if _, err := w.Write([]byte{b}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.flush(); err != nil {
		t.Fatal(err)
	}
	for _, chunk := range chunks {
		if !utf8.ValidString(chunk) || len(chunk) > 4099 {
			t.Fatal("invalid or oversized chunk")
		}
	}
	if strings.Join(chunks, "") != text {
		t.Fatal("log content lost")
	}
}

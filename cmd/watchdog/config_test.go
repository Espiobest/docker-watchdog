package main

import (
	"io"
	"testing"
)

func TestRejectInvalidOptions(t *testing.T) {
	for _, args := range [][]string{
		{"--interval=0"}, {"--concurrency=0"}, {"--max-retries=0"},
		{"--restart-timeout=10s"}, {"--output=invalid"}, {"--run-for=-1s"},
		{"--backoff=2m", "--max-backoff=1m"}, {"--db="}, {"unexpected"},
	} {
		if _, err := parseOptions(args, io.Discard); err == nil {
			t.Errorf("accepted invalid args: %v", args)
		}
	}
}

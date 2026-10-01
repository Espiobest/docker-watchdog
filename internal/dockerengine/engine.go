package dockerengine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"docker-watchdog/internal/watchdog"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

type Engine struct {
	Client *client.Client
	Label  string
}

func (e *Engine) List(ctx context.Context) ([]watchdog.Container, error) {
	opts := client.ContainerListOptions{All: true}
	if e.Label != "" {
		opts.Filters = make(client.Filters).Add("label", e.Label)
	}

	result, err := e.Client.ContainerList(ctx, opts)
	if err != nil {
		return nil, err
	}

	out := make([]watchdog.Container, 0, len(result.Items))
	for _, item := range result.Items {
		name := item.ID
		if len(item.Names) > 0 {
			name = strings.TrimPrefix(item.Names[0], "/")
		}
		out = append(out, watchdog.Container{ID: item.ID, Name: name, State: item.State})
	}

	return out, nil
}

func (e *Engine) Inspect(ctx context.Context, id string) (watchdog.Sample, error) {
	result, err := e.Client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return watchdog.Sample{}, err
	}

	item := result.Container
	if item.State == nil {
		return watchdog.Sample{}, fmt.Errorf("container %s has no state", id)
	}

	sample := watchdog.Sample{
		ID:           id,
		Name:         strings.TrimPrefix(item.Name, "/"),
		State:        item.State.Status,
		ExitCode:     item.State.ExitCode,
		RestartCount: item.RestartCount,
	}
	sample.StartedAt, _ = time.Parse(time.RFC3339Nano, item.State.StartedAt)
	if item.State.Health != nil {
		sample.Health = item.State.Health.Status
	}
	if item.HostConfig != nil {
		sample.RestartPolicy = item.HostConfig.RestartPolicy.Name
	}

	return sample, nil
}

func (e *Engine) Stats(ctx context.Context, sample *watchdog.Sample) error {
	result, err := e.Client.ContainerStats(ctx, sample.ID, client.ContainerStatsOptions{IncludePreviousSample: true})
	if err != nil {
		return err
	}
	defer result.Body.Close()

	var stats container.StatsResponse
	if err := json.NewDecoder(result.Body).Decode(&stats); err != nil {
		return err
	}

	applyStats(sample, stats)

	return nil
}

func (e *Engine) Restart(ctx context.Context, id string) error {
	timeout := 10
	_, err := e.Client.ContainerRestart(ctx, id, client.ContainerRestartOptions{Timeout: &timeout})

	return err
}

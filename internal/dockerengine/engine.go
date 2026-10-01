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
	for _, c := range result.Items {
		name := c.ID
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		out = append(out, watchdog.Container{ID: c.ID, Name: name, State: c.State})
	}
	return out, nil
}

func (e *Engine) Inspect(ctx context.Context, id string) (watchdog.Sample, error) {
	result, err := e.Client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return watchdog.Sample{}, err
	}
	c := result.Container
	if c.State == nil {
		return watchdog.Sample{}, fmt.Errorf("container %s has no state", id)
	}
	s := watchdog.Sample{ID: id, Name: strings.TrimPrefix(c.Name, "/"), State: c.State.Status, ExitCode: c.State.ExitCode, RestartCount: c.RestartCount}
	s.StartedAt, _ = time.Parse(time.RFC3339Nano, c.State.StartedAt)
	if c.State.Health != nil {
		s.Health = c.State.Health.Status
	}
	if c.HostConfig != nil {
		s.RestartPolicy = c.HostConfig.RestartPolicy.Name
	}
	return s, nil
}

func (e *Engine) Stats(ctx context.Context, s *watchdog.Sample) error {
	result, err := e.Client.ContainerStats(ctx, s.ID, client.ContainerStatsOptions{IncludePreviousSample: true})
	if err != nil {
		return err
	}
	defer result.Body.Close()

	var stats container.StatsResponse
	if err := json.NewDecoder(result.Body).Decode(&stats); err != nil {
		return err
	}

	applyStats(s, stats)

	return nil
}

func applyStats(s *watchdog.Sample, stats container.StatsResponse) {
	cpu, prev := stats.CPUStats.CPUUsage.TotalUsage, stats.PreCPUStats.CPUUsage.TotalUsage
	system, priorSystem := stats.CPUStats.SystemUsage, stats.PreCPUStats.SystemUsage
	cores := stats.CPUStats.OnlineCPUs
	if cores == 0 {
		cores = uint32(len(stats.CPUStats.CPUUsage.PercpuUsage))
	}
	if cpu >= prev && system > priorSystem {
		s.CPUPercent = float64(cpu-prev) / float64(system-priorSystem) * float64(cores) * 100
	}
	s.MemoryBytes = stats.MemoryStats.Usage
	cache := stats.MemoryStats.Stats["inactive_file"]
	if v, ok := stats.MemoryStats.Stats["total_inactive_file"]; ok {
		cache = v
	}
	if cache < s.MemoryBytes {
		s.MemoryBytes -= cache
	}
	s.MemoryLimit = stats.MemoryStats.Limit
	for _, network := range stats.Networks {
		s.NetworkRX += network.RxBytes
		s.NetworkTX += network.TxBytes
	}
}

func (e *Engine) Restart(ctx context.Context, id string) error {
	timeout := 10
	_, err := e.Client.ContainerRestart(ctx, id, client.ContainerRestartOptions{Timeout: &timeout})
	return err
}

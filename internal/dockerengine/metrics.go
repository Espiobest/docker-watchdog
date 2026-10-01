package dockerengine

import (
	"docker-watchdog/internal/watchdog"
	"github.com/moby/moby/api/types/container"
)

// applyStats converts Linux Docker stats into display values. CPU can exceed
// 100% when a container uses more than one core. Network values are totals.
func applyStats(sample *watchdog.Sample, stats container.StatsResponse) {
	cpu := stats.CPUStats.CPUUsage.TotalUsage
	previousCPU := stats.PreCPUStats.CPUUsage.TotalUsage
	system := stats.CPUStats.SystemUsage
	previousSystem := stats.PreCPUStats.SystemUsage
	cores := stats.CPUStats.OnlineCPUs
	if cores == 0 {
		cores = uint32(len(stats.CPUStats.CPUUsage.PercpuUsage))
	}
	if cpu >= previousCPU && system > previousSystem {
		sample.CPUPercent = float64(cpu-previousCPU) / float64(system-previousSystem) * float64(cores) * 100
	}

	sample.MemoryBytes = stats.MemoryStats.Usage
	cache := stats.MemoryStats.Stats["inactive_file"]
	if value, exists := stats.MemoryStats.Stats["total_inactive_file"]; exists {
		cache = value
	}
	if cache < sample.MemoryBytes {
		sample.MemoryBytes -= cache
	}
	sample.MemoryLimit = stats.MemoryStats.Limit

	for _, network := range stats.Networks {
		sample.NetworkRX += network.RxBytes
		sample.NetworkTX += network.TxBytes
	}
}

package docker

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"
)

// maxStatsBody bounds one stats answer. A reading is a few KiB; a cgroup v1
// engine's per-CPU list on a large host makes it longer, not megabytes.
const maxStatsBody = 4 << 20

// windowsCPUUnit is the length of one CPU-time unit a Windows container's
// stats count in: 100 ns. A Linux container's count nanoseconds.
const windowsCPUUnit = 100

// containerStats is one resource reading of a container, its counters
// cumulative since the container started (backlog 3.12a). The plugin turns
// two readings into rates (resources.go).
type containerStats struct {
	// Read is when the engine took the reading; the zero time when the
	// container is not running (the engine then answers an empty reading).
	Read time.Time
	// CPUNanos is the CPU time the container has used, in nanoseconds.
	CPUNanos uint64
	// Memory is the memory in use without the inactive file cache, as
	// docker stats shows it (Windows: the private working set);
	// MemoryKnown is false when the engine reported no usage.
	Memory      uint64
	MemoryKnown bool
	// MemoryLimit is the container's memory limit, the host's memory when
	// it has none; 0 when unknown (Windows).
	MemoryLimit uint64
	// Net says whether the reading carries networks; NetRx and NetTx are
	// summed over them.
	Net          bool
	NetRx, NetTx uint64
	// BlockRead and BlockWrite are the bytes read from and written to block
	// devices, summed over the devices.
	BlockRead, BlockWrite uint64
}

// rawStats is the part of the engine's stats answer the check reads. The
// one-shot answer leaves precpu_stats empty; the plugin keeps its own
// previous reading instead.
type rawStats struct {
	Read     time.Time `json:"read"`
	NumProcs uint32    `json:"num_procs"` // Windows only; always 0 on Linux
	CPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
	} `json:"cpu_stats"`
	MemoryStats struct {
		Usage             *uint64           `json:"usage"`
		Limit             uint64            `json:"limit"`
		Stats             map[string]uint64 `json:"stats"`
		PrivateWorkingSet *uint64           `json:"privateworkingset"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
	} `json:"networks"`
	BlkioStats struct {
		IOServiceBytesRecursive []struct {
			Op    string `json:"op"`
			Value uint64 `json:"value"`
		} `json:"io_service_bytes_recursive"`
	} `json:"blkio_stats"`
	StorageStats struct {
		ReadSizeBytes  uint64 `json:"read_size_bytes"`
		WriteSizeBytes uint64 `json:"write_size_bytes"`
	} `json:"storage_stats"`
}

// Stats reads one container's resources once, without the engine's wait
// for a second CPU sample: stream=false answers a single reading, and
// one-shot=true (API 1.41 and later; an older engine ignores it and takes
// a second or two) skips filling precpu_stats.
func (c *dockerClient) Stats(ctx context.Context, id string) (containerStats, error) {
	q := url.Values{"stream": {"false"}, "one-shot": {"true"}}
	body, err := c.get(ctx, "/containers/"+url.PathEscape(id)+"/stats?"+q.Encode(), maxStatsBody, opStats)
	if err != nil {
		return containerStats{}, err
	}
	var raw rawStats
	if err := json.Unmarshal(body, &raw); err != nil {
		return containerStats{}, opError(opStats, errors.New("malformed response"))
	}
	return raw.stats(), nil
}

func (r rawStats) stats() containerStats {
	out := containerStats{Read: r.Read, CPUNanos: r.CPUStats.CPUUsage.TotalUsage}
	if r.Read.Year() <= 1 {
		out.Read = time.Time{} // "0001-01-01T00:00:00Z": not running
	}
	if r.Networks != nil {
		out.Net = true
		for _, n := range r.Networks {
			out.NetRx += n.RxBytes
			out.NetTx += n.TxBytes
		}
	}
	if r.NumProcs > 0 {
		// A Windows container: its own units and fields. CPU time becomes
		// nanoseconds, so its percentage reads 100 for one core as on
		// every other platform; docker stats on Windows divides by the
		// processor count instead (100 there is every core).
		out.CPUNanos *= windowsCPUUnit
		if ws := r.MemoryStats.PrivateWorkingSet; ws != nil {
			out.Memory, out.MemoryKnown = *ws, true
		}
		out.BlockRead, out.BlockWrite = r.StorageStats.ReadSizeBytes, r.StorageStats.WriteSizeBytes
		return out
	}
	if u := r.MemoryStats.Usage; u != nil {
		out.Memory, out.MemoryKnown = memoryWithoutCache(*u, r.MemoryStats.Stats), true
		out.MemoryLimit = r.MemoryStats.Limit
	}
	for _, e := range r.BlkioStats.IOServiceBytesRecursive {
		switch strings.ToLower(e.Op) {
		case "read":
			out.BlockRead += e.Value
		case "write":
			out.BlockWrite += e.Value
		}
	}
	return out
}

// memoryWithoutCache is docker stats' own figure, its fallback order
// included (the CLI's calculateMemUsageUnixNoCache): the usage less
// cgroup v1's total_inactive_file when that is below it, else less cgroup
// v2's inactive_file (0 when absent) when that is below it, else the usage.
func memoryWithoutCache(usage uint64, stats map[string]uint64) uint64 {
	if v, ok := stats["total_inactive_file"]; ok && v < usage {
		return usage - v
	}
	if v := stats["inactive_file"]; v < usage {
		return usage - v
	}
	return usage
}

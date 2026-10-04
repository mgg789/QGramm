package config

import (
	"os"
	"runtime"
	"strconv"
	"strings"
)

// Resources is the effective local budget, not a measured service capacity.
type Resources struct {
	CPUs        int
	MemoryBytes int64
}

func DetectResources() Resources {
	r := Resources{CPUs: runtime.GOMAXPROCS(0), MemoryBytes: 512 << 20}
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] == "MemAvailable:" {
				n, e := strconv.ParseInt(fields[1], 10, 64)
				if e == nil && n > 0 {
					r.MemoryBytes = n * 1024
				}
			}
		}
	}
	for _, path := range []string{"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes"} {
		data, e := os.ReadFile(path)
		if e != nil {
			continue
		}
		n, e := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
		if e == nil && n > 0 && n < r.MemoryBytes {
			r.MemoryBytes = n
		}
	}
	if data, e := os.ReadFile("/sys/fs/cgroup/cpu.max"); e == nil {
		f := strings.Fields(string(data))
		if len(f) == 2 {
			q, e1 := strconv.ParseInt(f[0], 10, 64)
			p, e2 := strconv.ParseInt(f[1], 10, 64)
			if e1 == nil && e2 == nil && q > 0 && p > 0 {
				n := int((q + p - 1) / p)
				if n < r.CPUs {
					r.CPUs = n
				}
			}
		}
	}
	return r
}

// DeriveCapacity bounds automatic queues using the local memory budget. Explicit
// operator limits are preserved. This heuristic requires workload validation.
func DeriveCapacity(c Capacity, r Resources) Capacity {
	if r.CPUs < 1 {
		r.CPUs = 1
	}
	if r.MemoryBytes < 1 {
		r.MemoryBytes = 512 << 20
	}
	if c.Workers == 0 {
		c.Workers = r.CPUs * 2
		if c.Workers > 64 {
			c.Workers = 64
		}
	}
	if c.MaxConnections == 0 {
		c.MaxConnections = c.ExpectedConcurrentUsers + c.ExpectedConcurrentUsers/5
		if c.MaxConnections < c.ExpectedConcurrentUsers+1 {
			c.MaxConnections = c.ExpectedConcurrentUsers + 1
		}
	}
	if c.QueueDepth == 0 {
		// Each connection may retain queue_depth maximum-size payloads. Reserve
		// at most 1/4 of available memory for a 64 KiB message queue estimate.
		c.QueueDepth = int((r.MemoryBytes / 4) / int64(c.MaxConnections) / (64 << 10))
		if c.QueueDepth < 1 {
			c.QueueDepth = 1
		}
		if c.QueueDepth > 64 {
			c.QueueDepth = 64
		}
	}
	return c
}

type Estimate struct {
	CPUs        int      `json:"cpus"`
	MemoryBytes int64    `json:"memory_bytes"`
	Assumptions []string `json:"assumptions"`
}

func EstimateResources(c Config) Estimate {
	users := int64(c.Capacity.ExpectedConcurrentUsers)
	cpus := int((users + 1999) / 2000)
	if cpus < 1 {
		cpus = 1
	}
	memory := (128 << 20) + int64(c.Capacity.MaxConnections)*((128<<10)+int64(c.Capacity.QueueDepth)*int64(c.Policy.MaxMessageBytes)) + int64(c.Capacity.Workers)*int64(c.Policy.MaxMessageBytes)*int64(c.Policy.MaxBatch)
	estimate := Estimate{CPUs: cpus, MemoryBytes: memory, Assumptions: []string{"Uncalibrated planning heuristic; load testing is required before production sizing.", "One connection per expected user; 20% connection headroom when unset.", "128 MiB process baseline, 128 KiB connection state, queues at maximum message size.", "CPU estimate assumes 2000 mostly idle connections per core; active fanout, AI and uploads need additional budget."}}
	if c.Features.Redis {
		estimate.MemoryBytes += int64(c.Redis.MaxMemoryMB+32+16) << 20
		estimate.Assumptions = append(estimate.Assumptions, "Embedded Redis heuristic: configured maxmemory + 32 MiB Pub/Sub/buffer budget + 16 MiB process baseline; load-test actual combined RSS and CPU.")
	}
	return estimate
}

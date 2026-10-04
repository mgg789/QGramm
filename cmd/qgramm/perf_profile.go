//go:build qg_bench_profile

package main

// Development-only instrumentation. No network listener or debug route is
// registered. The production build never includes this file.
import (
	"database/sql"
	"encoding/json"
	"github.com/mgg789/QGramm/internal/core"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

func init() {
	for _, arg := range os.Args[1:] {
		if arg == "-healthcheck" || arg == "--healthcheck" || strings.HasPrefix(arg, "-healthcheck=") || strings.HasPrefix(arg, "--healthcheck=") {
			return
		}
	}
	dir := os.Getenv("QGRAMM_BENCH_PROFILE_DIR")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		panic(err)
	}
	// A separate diagnostic run can collect timelines without injecting GC or
	// enabling sampling profilers. It is still instrumented, not a primary run.
	if os.Getenv("QGRAMM_BENCH_TIMELINE_ONLY") == "1" {
		startTimeline(dir)
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "cpu.pprof"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}
	if err = pprof.StartCPUProfile(f); err != nil {
		panic(err)
	}
	runtime.SetBlockProfileRate(10000)
	runtime.SetMutexProfileFraction(10)
	var attached atomic.Pointer[core.Core]
	profileCoreHook = func(c *core.Core) { attached.Store(c) }
	changes := make(chan os.Signal, 2)
	signal.Notify(changes, syscall.SIGUSR1, syscall.SIGUSR2)
	go func() {
		for sig := range changes {
			if sig == syscall.SIGUSR2 {
				pprof.StopCPUProfile()
				_ = f.Close()
				continue
			}
			stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
			// GC isolates retained heap from garbage; profile runs are separate
			// from latency comparisons because instrumentation changes timings.
			runtime.GC()
			for _, name := range []string{"heap", "goroutine", "block", "mutex"} {
				out, err := os.OpenFile(filepath.Join(dir, stamp+"-"+name+".pprof"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				if err == nil {
					_ = pprof.Lookup(name).WriteTo(out, 0)
					_ = out.Close()
				}
			}
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			row := map[string]any{"at": stamp, "goroutines": runtime.NumGoroutine(), "heap_alloc_bytes": m.HeapAlloc, "heap_inuse_bytes": m.HeapInuse, "heap_sys_bytes": m.HeapSys, "stack_inuse_bytes": m.StackInuse, "sys_bytes": m.Sys, "mallocs": m.Mallocs, "frees": m.Frees, "gc_cycles": m.NumGC, "total_alloc_bytes": m.TotalAlloc, "gc_pause_total_ns": m.PauseTotalNs, "gc_cpu_fraction": m.GCCPUFraction}
			if c := attached.Load(); c != nil {
				row["writer_db"] = c.DB.Stats()
				row["message_writer"] = c.WriterStats()
				row["http_admission"] = c.AdmissionStats()
				row["diagnostics"] = c.DiagnosticStats()
				if reader, ok := any(c).(interface{ ReadStats() sql.DBStats }); ok {
					row["reader_db"] = reader.ReadStats()
				}
			}
			data, _ := json.Marshal(row)
			_ = os.WriteFile(filepath.Join(dir, stamp+"-runtime.json"), append(data, '\n'), 0600)
		}
	}()
}

func startTimeline(dir string) {
	f, err := os.OpenFile(filepath.Join(dir, "timeline.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}
	var attached atomic.Pointer[core.Core]
	profileCoreHook = func(c *core.Core) { attached.Store(c) }
	changes := make(chan os.Signal, 2)
	signal.Notify(changes, syscall.SIGUSR1, syscall.SIGUSR2)
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		defer f.Close()
		defer signal.Stop(changes)
		encoder := json.NewEncoder(f)
		var signalSequence uint64
		snapshot := func(reason string) {
			c := attached.Load()
			if c == nil {
				return
			}
			diagnostics := c.DiagnosticStats()
			_ = encoder.Encode(map[string]any{
				"at_unix_ns": time.Now().UnixNano(), "forced_gc": false,
				"snapshot_reason": reason, "signal_sequence": signalSequence,
				"heap_alloc_bytes": diagnostics["heap_alloc_bytes"], "total_alloc_bytes": diagnostics["total_alloc_bytes"],
				"mallocs": diagnostics["mallocs"], "gc_cycles": diagnostics["gc_cycles"], "gc_pause_total_ns": diagnostics["gc_pause_total_ns"],
				"diagnostics": diagnostics, "message_writer": c.WriterStats(),
			})
		}
		for {
			select {
			case <-ticker.C:
				snapshot("tick")
			case sig := <-changes:
				signalSequence++
				snapshot("signal")
				if sig == syscall.SIGUSR2 {
					return
				}
			}
		}
	}()
}

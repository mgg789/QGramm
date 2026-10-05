package config

import "testing"

func TestCheckpointConfigurationBounds(t *testing.T) {
	for _, tc := range []struct {
		interval int
		bytes    int64
		valid    bool
	}{{0, 0, true}, {100, 1 << 20, true}, {60000, 1 << 30, true}, {99, 0, false}, {60001, 0, false}, {-1, 0, false}, {100, 65535, false}, {100, 1<<30 + 1, false}, {0, 1 << 20, false}} {
		cfg := Defaults()
		cfg.Server.AllowInsecureLoopback = true
		cfg.Storage.CheckpointIntervalMS = tc.interval
		cfg.Storage.CheckpointWALBytes = tc.bytes
		err := cfg.Validate()
		if (err == nil) != tc.valid {
			t.Fatalf("interval=%d bytes=%d valid=%v err=%v", tc.interval, tc.bytes, tc.valid, err)
		}
	}
}

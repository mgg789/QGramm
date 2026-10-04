package config

import "testing"

func TestRedisConfigurationBounds(t *testing.T) {
	c := Defaults()
	c.Server.AllowInsecureLoopback = true
	c.Features.Redis = true
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []Redis{{"", 64, 256}, {"redis-server", 0, 256}, {"redis-server", 1025, 256}, {"redis-server", 64, 0}, {"redis-server", 64, 4097}} {
		c.Redis = cfg
		if err := c.Validate(); err == nil {
			t.Fatalf("accepted invalid Redis config: %+v", cfg)
		}
	}
	c.Features.Redis = false
	if err := c.Validate(); err != nil {
		t.Fatalf("disabled feature requires Redis config: %v", err)
	}
}

func TestRedisResourceEstimateIncludesBothProcesses(t *testing.T) {
	cfg := Defaults()
	baseline := EstimateResources(cfg)
	cfg.Features.Redis = true
	withRedis := EstimateResources(cfg)
	if withRedis.MemoryBytes-baseline.MemoryBytes != int64(cfg.Redis.MaxMemoryMB+32+16)<<20 {
		t.Fatal("Redis budget omitted")
	}
	if len(withRedis.Assumptions) != len(baseline.Assumptions)+1 {
		t.Fatal("missing heuristic disclosure")
	}
}

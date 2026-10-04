package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func localConfig() Config { c := Defaults(); c.Server.AllowInsecureLoopback = true; return c }

func TestLoadProfiles(t *testing.T) {
	for _, path := range []string{"../../configs/minimal.toml", "../../configs/full.toml", "../../qgramm.toml"} {
		t.Run(path, func(t *testing.T) {
			c, e := Load(path)
			if e != nil {
				t.Fatal(e)
			}
			if c.Capacity.QueueDepth < 1 || c.Capacity.Workers < 1 || c.Capacity.MaxConnections < c.Capacity.ExpectedConcurrentUsers {
				t.Fatalf("invalid derived capacity: %+v", c.Capacity)
			}
		})
	}
}

func TestLoadRejectsUnknownFieldsAndLiteralSecrets(t *testing.T) {
	for _, text := range []string{
		"[server]\nallow_insecure_loopback=true\nlisten='127.0.0.1:8080'\ntypo=true\n",
		"[server]\nallow_insecure_loopback=true\n[security]\nmaster_key_env='literal-secret-with-dashes'\n",
		"[server]\nallow_insecure_loopback=true\n[security]\nmaster_key='literal'\n",
	} {
		path := filepath.Join(t.TempDir(), "config.toml")
		if e := os.WriteFile(path, []byte(text), 0600); e != nil {
			t.Fatal(e)
		}
		_, e := Load(path)
		if e == nil {
			t.Fatal("accepted invalid configuration")
		}
		if strings.Contains(e.Error(), "literal-secret-with-dashes") {
			t.Fatal("error disclosed value")
		}
	}
}

func TestSecurityValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Config)
	}{
		{"plaintext public", func(c *Config) { c.Server.Listen = "0.0.0.0:8080" }},
		{"unpaired TLS", func(c *Config) { c.Server.TLSCert = "cert.pem" }},
		{"unbounded AI", func(c *Config) {
			c.Features.OpenAI = true
			c.AI.OpenAIKeyEnv = "OPENAI_KEY"
			c.AI.Model = "model"
			c.AI.MaxSteps = 65
		}},
		{"tool without provider", func(c *Config) { c.Features.HTTPTools = true }},
		{"ambiguous HTTP tool method", func(c *Config) {
			c.Features.OpenAI = true
			c.Features.HTTPTools = true
			c.AI.OpenAIKeyEnv = "OPENAI_KEY"
			c.AI.Model = "model"
			c.AI.Tools = []Tool{{Name: "query", Kind: "http", URL: "https://example.com", Methods: []string{"GET", "POST"}}}
		}},
		{"unknown tool kind", func(c *Config) { c.AI.Tools = []Tool{{Name: "bad", Kind: "shell"}} }},
		{"credential URL", func(c *Config) {
			c.Features.OpenAI = true
			c.AI.Model = "model"
			c.AI.OpenAIKeyEnv = "OPENAI_KEY"
			c.AI.OpenAIURL = "https://user:pass@example.com"
		}},
		{"invalid retention", func(c *Config) { c.Policy.DedupRetentionHours = 0 }},
		{"invalid chunk", func(c *Config) { c.Policy.MaxChunkBytes = c.Policy.MaxFileBytes + 1 }},
		{"missing TURN", func(c *Config) { c.Features.Calls = true; c.Calls.TURNSecretEnv = "TURN_SECRET" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := localConfig()
			tc.mutate(&c)
			if c.Validate() == nil {
				t.Fatal("accepted invalid config")
			}
		})
	}
	if err := localConfig().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestEnabledSorted(t *testing.T) {
	f := Features{HTTPTools: true, Files: true, Anthropic: true}
	if !reflect.DeepEqual(f.Enabled(), []string{"anthropic", "files", "http_tools"}) {
		t.Fatal(f.Enabled())
	}
}

func TestDeriveCapacityBoundsAndPreservesOverrides(t *testing.T) {
	c := DeriveCapacity(Capacity{ExpectedConcurrentUsers: 100}, Resources{CPUs: 2, MemoryBytes: 32 << 20})
	if c.Workers != 4 || c.MaxConnections != 120 || c.QueueDepth != 1 {
		t.Fatalf("unexpected bounds: %+v", c)
	}
	want := Capacity{ExpectedConcurrentUsers: 100, Workers: 7, QueueDepth: 11, MaxConnections: 200}
	if got := DeriveCapacity(want, Resources{}); got != want {
		t.Fatalf("overrides lost: %+v", got)
	}
}

func TestEstimateIncludesQueueMemory(t *testing.T) {
	c := localConfig()
	c.Capacity = DeriveCapacity(c.Capacity, Resources{CPUs: 1, MemoryBytes: 512 << 20})
	a := EstimateResources(c)
	c.Capacity.QueueDepth++
	b := EstimateResources(c)
	if b.MemoryBytes-a.MemoryBytes != int64(c.Capacity.MaxConnections*c.Policy.MaxMessageBytes) {
		t.Fatal("queue sizing omitted")
	}
	if len(a.Assumptions) == 0 {
		t.Fatal("missing assumptions")
	}
}

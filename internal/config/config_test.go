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
	for _, path := range []string{"../../configs/minimal.toml", "../../configs/full.toml", "../../configs/support.toml", "../../configs/community.toml", "../../configs/ai-openai.toml", "../../configs/ai-anthropic.toml", "../../configs/ai-network.toml", "../../configs/ai-policy.toml", "../../qgramm.toml"} {
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

func TestNamedAIProfilesResolveInheritanceAndExplicitZero(t *testing.T) {
	text := []byte(`
[server]
allow_insecure_loopback = true
[features]
openai = true
http_tools = true
ai_streaming = true
[ai]
model = "legacy-model"
max_output_tokens = 4096
[[ai.tools]]
name = "lookup"
kind = "http"
url = "https://tools.example.test/lookup"
methods = ["POST"]
[ai.endpoints.primary]
provider = "openai"
url = "https://provider.example.test/v1"
model = "endpoint-model"
key_env = "ENDPOINT_KEY"
allow_private = true
streaming = true
max_steps = 12
max_output_tokens = 1024
[ai.bots.safe]
endpoint = "primary"
allow_private = false
streaming = false
max_steps = 0
tools = []
`)
	if _, err := ParseDetailed(text); err == nil {
		t.Fatal("accepted zero limit override")
	}
	text = []byte(strings.Replace(string(text), "max_steps = 0", "max_steps = 4", 1))
	d, err := ParseDetailed(text)
	if err != nil {
		t.Fatal(err)
	}
	effective, provider, err := d.Config.ResolveBot("safe")
	if err != nil {
		t.Fatal(err)
	}
	if provider != "openai" || effective.OpenAIURL != "https://provider.example.test/v1" || effective.OpenAIKeyEnv != "ENDPOINT_KEY" || effective.Model != "endpoint-model" {
		t.Fatalf("unexpected resolved endpoint: provider=%q config=%+v", provider, effective)
	}
	if effective.AllowPrivate || effective.Streaming || effective.MaxSteps != 4 || effective.MaxOutputTokens != 1024 || len(effective.Tools) != 0 {
		t.Fatalf("bot overrides did not apply: %+v", effective)
	}
}

func TestStreamingRequiresProvider(t *testing.T) {
	c := localConfig()
	c.Features.AIStreaming = true
	if err := c.Validate(); err == nil {
		t.Fatal("streaming accepted without a provider")
	}
}

func TestNamedAIProfilesRejectInvalidReferencesAndURLs(t *testing.T) {
	for _, text := range []string{
		`[server]
allow_insecure_loopback = true
[features]
openai = true
[ai.endpoints.bad]
provider = "openai"
url = "https://user:pass@example.test/v1"
model = "model"
key_env = "KEY"
`,
		`[server]
allow_insecure_loopback = true
[features]
openai = true
[ai.endpoints.bad]
provider = "openai"
url = "https://example.test/v1"
model = "model"
key_env = "not-an-env"
`,
		`[server]
allow_insecure_loopback = true
[features]
openai = true
[ai.endpoints.bad]
provider = "openai"
url = "https://example.test/v1"
model = "model"
key_env = "KEY"
[ai.bots.bot]
endpoint = "missing"
`,
		`[server]
allow_insecure_loopback = true
[features]
openai = true
[ai.endpoints.bad]
provider = "openai"
url = "https://example.test/v1"
model = "model"
key_env = "KEY"
capabilities = ["future"]
`,
		`[server]
allow_insecure_loopback = true
[features]
openai = true
[ai.endpoints.bad]
provider = "openai"
url = "https://example.test/v1"
model = "model"
key_env = "KEY"
capabilities = ["structured"]
`,
		`[server]
allow_insecure_loopback = true
[features]
openai = true
[ai.endpoints.bad]
provider = "openai"
url = "https://example.test/v1"
model = "model"
key_env = "KEY"
unknown = true
`,
	} {
		if _, err := ParseDetailed([]byte(text)); err == nil {
			t.Fatal("accepted invalid named AI profile")
		}
	}
}

func TestNamedAIProfilesRequireDefaultAndSupportPrivateNoAuth(t *testing.T) {
	withoutDefault := []byte(`[server]
allow_insecure_loopback = true
[features]
openai = true
[ai]
[ai.endpoints.local]
provider = "openai"
url = "http://127.0.0.1:9000/v1"
model = "local"
auth = "none"
allow_private = true
[ai.bots.local]
endpoint = "local"
`)
	if _, err := ParseDetailed(withoutDefault); err == nil {
		t.Fatal("accepted named-only configuration without default_bot")
	}
	withDefault := []byte(strings.Replace(string(withoutDefault), "[ai]\n", "[ai]\ndefault_bot = \"local\"\n", 1))
	d, err := ParseDetailed(withDefault)
	if err != nil {
		t.Fatal(err)
	}
	effective, provider, err := d.Config.ResolveBot("")
	if err != nil {
		t.Fatal(err)
	}
	if provider != "openai" || effective.Auth != "none" || !effective.AllowPrivate {
		t.Fatalf("unexpected local endpoint resolution: provider=%q config=%+v", provider, effective)
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

func TestPresetsAreCuratedAndExplicitValuesWin(t *testing.T) {
	for _, tc := range []struct {
		name     string
		features []string
		env      string
	}{
		{"minimal", nil, ""},
		{"support", []string{"delete", "edit", "files", "groups", "reactions", "reply"}, ""},
		{"community", []string{"delete", "edit", "files", "forward", "groups", "reactions", "reply"}, ""},
		{"ai-openai", []string{"openai"}, "QGRAMM_OPENAI_KEY"},
		{"ai-anthropic", []string{"anthropic"}, "QGRAMM_ANTHROPIC_KEY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := "preset = \"" + tc.name + "\"\n[server]\nallow_insecure_loopback = true\n"
			if strings.HasPrefix(tc.name, "ai-") {
				text += "[ai]\nmodel = \"operator-model\"\n"
			}
			d, err := ParseDetailedWithResources([]byte(text), Resources{CPUs: 2, MemoryBytes: 64 << 20})
			if err != nil {
				t.Fatal(err)
			}
			if got := d.Config.Features.Enabled(); !reflect.DeepEqual(got, append([]string{}, tc.features...)) {
				t.Fatalf("features=%v want=%v", got, tc.features)
			}
			if tc.env != "" && d.Config.AI.OpenAIKeyEnv != tc.env && d.Config.AI.AnthropicKeyEnv != tc.env {
				t.Fatalf("preset secret reference=%q", tc.env)
			}
			if tc.name == "support" {
				text += "\n[features]\nfiles = false\n[capacity]\nqueue_depth = 0\n"
				d, err = ParseDetailedWithResources([]byte(text), Resources{CPUs: 2, MemoryBytes: 64 << 20})
				if err != nil {
					t.Fatal(err)
				}
				if d.Config.Features.Files || d.Sources["features.files"] != "explicit" {
					t.Fatalf("explicit false did not override preset: files=%v source=%q", d.Config.Features.Files, d.Sources["features.files"])
				}
			}
		})
	}
}

func TestAIPresetRequiresExplicitModelAndAllowsDisable(t *testing.T) {
	withoutModel := []byte("preset = \"ai-openai\"\n[server]\nallow_insecure_loopback = true\n")
	if _, err := ParseDetailed(withoutModel); err == nil {
		t.Fatal("accepted AI preset without explicit model")
	}
	disabled := append(withoutModel, []byte("\n[features]\nopenai = false\n")...)
	if _, err := ParseDetailed(disabled); err != nil {
		t.Fatalf("explicit provider disable should avoid model requirement: %v", err)
	}
	override := append([]byte("preset = \"ai-openai\"\n[server]\nallow_insecure_loopback = true\n[ai]\nmodel = \"operator-model\"\nopenai_key_env = \"CUSTOM_OPENAI_REF\"\n"), '\n')
	d, err := ParseDetailed(override)
	if err != nil {
		t.Fatal(err)
	}
	if d.Config.AI.OpenAIKeyEnv != "CUSTOM_OPENAI_REF" || d.Sources["ai.openai_key_env"] != "explicit" {
		t.Fatalf("explicit AI secret reference did not override preset: %+v", d)
	}
}

func TestDetailedRejectsUnknownPresetAndTracksNestedSources(t *testing.T) {
	if _, err := ParseDetailed([]byte("preset = \"unknown\"\n")); err == nil || strings.Contains(err.Error(), "unknown\"") {
		t.Fatalf("unexpected unknown-preset diagnostic: %v", err)
	}
	text := []byte("[server]\nallow_insecure_loopback = true\n[security]\nissuer = \"operator\"\n[capacity]\nexpected_concurrent_users = 12\nworkers = 0\n")
	d, err := ParseDetailedWithResources(text, Resources{CPUs: 1, MemoryBytes: 32 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if d.Sources["server.allow_insecure_loopback"] != "explicit" || d.Sources["security.issuer"] != "explicit" {
		t.Fatalf("nested source classification missing: %#v", d.Sources)
	}
	if d.Sources["capacity.workers"] != "derived" || d.Config.Capacity.Workers == 0 {
		t.Fatalf("capacity derivation source missing: %+v", d)
	}
	if d.DeclaredSources["capacity.workers"] != "explicit" {
		t.Fatalf("explicit zero input source missing: %#v", d.DeclaredSources)
	}
	omitted, err := ParseDetailedWithResources([]byte("[server]\nallow_insecure_loopback = true\n"), Resources{CPUs: 1, MemoryBytes: 32 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if omitted.Sources["capacity.workers"] != "derived" || omitted.DeclaredSources["capacity.workers"] != "default" {
		t.Fatalf("omitted zero input source missing: sources=%#v declared=%#v", omitted.Sources, omitted.DeclaredSources)
	}
}

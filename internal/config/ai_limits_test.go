package config

import "testing"

func TestAILimitValidation(t *testing.T) {
	base := Defaults()
	base.Server.AllowInsecureLoopback = true
	base.Features.OpenAI = true
	base.AI.Model = "fixture"
	base.AI.OpenAIKeyEnv = "FIXTURE_OPENAI_KEY"
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Config){
		func(c *Config) { c.AI.MaxContextTurns = 21 },
		func(c *Config) { c.AI.MaxContextBytes = 262145 },
		func(c *Config) { c.AI.TimeoutSeconds = 46 },
		func(c *Config) { c.AI.MaxResponseBytes = 1048577 },
		func(c *Config) { c.AI.TimeoutSeconds = 0 },
		func(c *Config) {
			c.Features.HTTPTools = true
			c.AI.Tools = []Tool{{Name: "query", Kind: "http", URL: "https://tools.example.com", Methods: []string{"POST"}, TimeoutSeconds: -1}}
		},
		func(c *Config) {
			c.Features.HTTPTools = true
			c.AI.Tools = []Tool{{Name: "query", Kind: "http", URL: "https://tools.example.com", Methods: []string{"POST"}, MaxResponseBytes: 1048577}}
		},
	} {
		candidate := base
		mutate(&candidate)
		if err := candidate.Validate(); err == nil {
			t.Fatal("invalid AI limits accepted")
		}
	}
}

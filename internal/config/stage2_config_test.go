package config

import (
	"reflect"
	"testing"
)

func TestAIPolicyValidationAndSecretReferences(t *testing.T) {
	base := Defaults()
	base.Server.AllowInsecureLoopback = true
	base.Features.OpenAI = true
	base.Features.AIPolicy = true
	base.AI.Model = "operator-model"
	base.AI.OpenAIKeyEnv = "OPENAI_KEY"
	base.AIPolicy.GlobalDailyBudgetMicrounits = 100
	base.AIPolicy.ProviderReserveMicrounits = 1
	if err := base.Validate(); err != nil {
		t.Fatalf("valid policy rejected: %v", err)
	}
	refs := SecretReferences(base)
	if !reflect.DeepEqual(refs, []string{
		"OPENAI_KEY",
		"QGRAMM_AI_GRANT_PUBLIC_KEY",
		"QGRAMM_HPKE_KEY",
		"QGRAMM_MANAGEMENT_SECRET",
		"QGRAMM_MASTER_KEY",
		"QGRAMM_TOKEN_PUBLIC_KEY",
	}) {
		t.Fatalf("policy key reference missing or unstable: %v", refs)
	}
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"without provider", func(c *Config) { c.Features.OpenAI = false }},
		{"device audience collision", func(c *Config) { c.AIPolicy.Audience = c.Security.Audience }},
		{"budget without reserve", func(c *Config) { c.AIPolicy.ProviderReserveMicrounits = 0 }},
		{"invalid currency", func(c *Config) { c.AIPolicy.Currency = "usd" }},
		{"approval ttl out of range", func(c *Config) { c.AIPolicy.ApprovalTTLSeconds = 901 }},
		{"negative tool cost", func(c *Config) {
			c.Features.HTTPTools = true
			c.AI.Tools = []Tool{{Name: "lookup", Kind: "http", URL: "https://tools.example.test", Methods: []string{"POST"}, CostMicrounits: -1}}
		}},
		{"policy setting while disabled", func(c *Config) { c.Features.AIPolicy = false; c.AIPolicy.Currency = "EUR" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := base
			tc.mutate(&c)
			if err := c.Validate(); err == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}
}

func TestAIPolicyEndpointPricingInheritsIntoBot(t *testing.T) {
	data := []byte(`
[server]
allow_insecure_loopback = true
[features]
openai = true
ai_policy = true
[ai]
default_bot = "assistant"
[ai.endpoints.primary]
provider = "openai"
url = "https://provider.example.test/v1"
model = "endpoint-model"
key_env = "ENDPOINT_KEY"
input_price_microunits_per_million_tokens = 1200
output_price_microunits_per_million_tokens = 3400
[ai.bots.assistant]
endpoint = "primary"
`)
	d, err := ParseDetailed(data)
	if err != nil {
		t.Fatal(err)
	}
	effective, provider, err := d.Config.ResolveBot("assistant")
	if err != nil {
		t.Fatal(err)
	}
	if provider != "openai" || effective.InputPriceMicrounitsPerMillionTokens != 1200 || effective.OutputPriceMicrounitsPerMillionTokens != 3400 {
		t.Fatalf("endpoint prices did not inherit: provider=%q config=%+v", provider, effective)
	}
}

func TestAIPolicyExplicitFalseOverridesPreset(t *testing.T) {
	data := []byte(`preset = "ai-openai"
[server]
allow_insecure_loopback = true
[features]
openai = false
ai_policy = false
[ai]
model = "operator-model"
`)
	d, err := ParseDetailed(data)
	if err != nil {
		t.Fatal(err)
	}
	if d.Config.Features.OpenAI || d.Config.Features.AIPolicy || d.Sources["features.ai_policy"] != "explicit" {
		t.Fatalf("explicit false was not retained: features=%+v sources=%#v", d.Config.Features, d.Sources)
	}
}

func TestAIPolicyDisabledRejectsPricingAndApproval(t *testing.T) {
	c := Defaults()
	c.Server.AllowInsecureLoopback = true
	c.Features.OpenAI = true
	c.AI.Model = "operator-model"
	c.AI.InputPriceMicrounitsPerMillionTokens = 1
	if err := c.Validate(); err == nil {
		t.Fatal("pricing was silently ignored while policy feature was disabled")
	}
	c.AI.InputPriceMicrounitsPerMillionTokens = 0
	c.Features.HTTPTools = true
	c.AI.Tools = []Tool{{Name: "lookup", Kind: "http", URL: "https://tools.example.test", Methods: []string{"POST"}, RequireApproval: true}}
	if err := c.Validate(); err == nil {
		t.Fatal("tool approval was silently ignored while policy feature was disabled")
	}
}

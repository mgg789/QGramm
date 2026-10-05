package config

import "testing"

func TestAIStorageProfilesAndInvalidCombinations(t *testing.T) {
	valid := `[server]
allow_insecure_loopback=true
[features]
openai=true
ai_policy=true
ai_storage=true
[ai]
model="local"
openai_key_env="QGRAMM_OPENAI_KEY"
[[ai.tools]]
name="knowledge"
kind="storage"
resource="docs"
storage_action="search"
require_approval=true
[ai.tools.schema]
type="object"
[ai.tools.schema.properties.query]
type="string"
`
	c, err := Parse([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if c.AIStorage.MaxDocuments != 4096 || !c.Features.AIStorage {
		t.Fatal("defaults missing")
	}
	properties, ok := c.AI.Tools[0].Schema["properties"].(map[string]any)
	if !ok || properties["query"] == nil {
		t.Fatal("nested JSON Schema lost during TOML decoding")
	}
	if _, err := Parse([]byte(valid + "\n[ai.tools.unavailable]\nvalue=1\n")); err == nil {
		t.Fatal("unknown tool configuration accepted")
	}
	for _, alter := range []func(*Config){
		func(c *Config) { c.Features.AIPolicy = false }, func(c *Config) { c.Features.AIStorage = false }, func(c *Config) { c.AI.Tools[0].RequireApproval = false }, func(c *Config) { c.AI.Tools[0].URL = "https://example.com" }, func(c *Config) { c.AIStorage.Audience = c.Security.Audience }, func(c *Config) { c.AIStorage.MaxVectorDimensions = 4097 },
	} {
		broken := c
		broken.AI.Tools = append([]Tool(nil), c.AI.Tools...)
		alter(&broken)
		if broken.Validate() == nil {
			t.Fatal("unsafe storage profile accepted")
		}
	}
	if _, err = Parse([]byte("[server]\nallow_insecure_loopback=true\n[features]\nai_endpoint=true\n")); err == nil {
		t.Fatal("endpoint without MLS accepted")
	}
	if _, err = Parse([]byte("[server]\nallow_insecure_loopback=true\n[features]\nai_endpoint=true\ne2ee=true\n")); err != nil {
		t.Fatal(err)
	}
}

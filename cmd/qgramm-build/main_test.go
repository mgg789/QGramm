package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanIsExplicitAndContainsAssumptions(t *testing.T) {
	out := filepath.Join(t.TempDir(), "plan.json")
	if e := run([]string{"plan", "-config", "../../configs/full.toml", "-out", out}); e != nil {
		t.Fatal(e)
	}
	data, e := os.ReadFile(out)
	if e != nil {
		t.Fatal(e)
	}
	var plan struct {
		Tags     []string `json:"build_tags"`
		Estimate struct {
			Assumptions []string `json:"assumptions"`
		} `json:"estimate"`
	}
	if e = json.Unmarshal(data, &plan); e != nil {
		t.Fatal(e)
	}
	if len(plan.Tags) != 13 || len(plan.Estimate.Assumptions) == 0 {
		t.Fatalf("incomplete plan: %s", data)
	}
}
func TestComposeReferencesRuntimeSecrets(t *testing.T) {
	t.Chdir("../..")
	t.Setenv("QGRAMM_MASTER_KEY", "never-copy-this-test-value")
	out := filepath.Join(t.TempDir(), "compose.yaml")
	if e := run([]string{"compose", "-config", "configs/container.toml", "-out", out}); e != nil {
		t.Fatal(e)
	}
	data, e := os.ReadFile(out)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(data), "never-copy-this-test-value") || !strings.Contains(string(data), "${QGRAMM_MASTER_KEY:?required runtime secret}") {
		t.Fatal("secret handling failed")
	}
	if !strings.Contains(string(data), "127.0.0.1:8080:8080") {
		t.Fatal("public plaintext host binding")
	}
}
func TestRejectUnknownCommandBeforeWriting(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	if run([]string{"invalid", "-out", out}) == nil {
		t.Fatal("accepted unknown command")
	}
	if _, e := os.Stat(out); !os.IsNotExist(e) {
		t.Fatal("unexpected output")
	}
}

func TestRedisComposeSelectsEmbeddedDockerfile(t *testing.T) {
	t.Chdir("../..")
	out := filepath.Join(t.TempDir(), "compose.yaml")
	if err := run([]string{"compose", "-config", "configs/redis.toml", "-out", out}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `dockerfile: "Dockerfile.redis"`) {
		t.Fatalf("missing embedded Redis image selection: %s", data)
	}
}

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mgg789/QGramm/internal/config"
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
	if len(plan.Tags) != 14 || len(plan.Estimate.Assumptions) == 0 {
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

func TestInitGeneratesValidTargetsAndAIModels(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		listen string
		model  string
	}{
		{"local minimal", []string{"init", "-preset", "minimal", "-target", "local"}, "127.0.0.1:8080", ""},
		{"container community", []string{"init", "-preset", "community", "-target", "container"}, "0.0.0.0:8080", ""},
		{"local openai", []string{"init", "-preset", "ai-openai", "-model", "operator-model", "-target", "local"}, "127.0.0.1:8080", "operator-model"},
		{"container anthropic", []string{"init", "-preset", "ai-anthropic", "-model", "operator-model", "-target", "container"}, "0.0.0.0:8080", "operator-model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "qgramm.toml")
			args := append(tc.args, "-users", "23", "-out", out)
			if err := run(args); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "listen = \""+tc.listen+"\"") {
				t.Fatalf("target listener missing: %s", data)
			}
			if tc.model != "" && !strings.Contains(string(data), "model = \""+tc.model+"\"") {
				t.Fatalf("model missing: %s", data)
			}
			if _, err = config.Load(out); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestInitRefusesOverwriteAndInvalidAIHasNoOutput(t *testing.T) {
	out := filepath.Join(t.TempDir(), "qgramm.toml")
	if err := os.WriteFile(out, []byte("sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"init", "-preset", "minimal", "-out", out}); err == nil {
		t.Fatal("init overwrote existing configuration")
	}
	data, err := os.ReadFile(out)
	if err != nil || string(data) != "sentinel" {
		t.Fatalf("existing output changed: %q %v", data, err)
	}
	missing := filepath.Join(t.TempDir(), "missing.toml")
	if err := run([]string{"init", "-preset", "ai-openai", "-out", missing}); err == nil {
		t.Fatal("accepted AI preset without model")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("invalid input created output: %v", err)
	}
}

func TestExplainDoesNotReadSecretValues(t *testing.T) {
	t.Setenv("QGRAMM_MASTER_KEY", "sentinel-secret-value")
	out := filepath.Join(t.TempDir(), "explain.json")
	if err := run([]string{"explain", "-config", "../../configs/minimal.toml", "-out", out}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "sentinel-secret-value") || !strings.Contains(string(data), "QGRAMM_MASTER_KEY") || !strings.Contains(string(data), "not_checked") {
		t.Fatalf("secret handling failed: %s", data)
	}
}

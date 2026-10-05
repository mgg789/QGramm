package main

import (
	"bytes"
	"encoding/base64"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/mgg789/QGramm/internal/config"
	"github.com/mgg789/QGramm/internal/core"
	_ "github.com/mgg789/QGramm/internal/modules"
)

// Opt-in: each subprocess is compiled with its selected feature tags. Real
// Core.Open then verifies actual module installation, routes and schema.
func TestMatrixRuntimeSelection(t *testing.T) {
	path := os.Getenv("QGRAMM_MATRIX_CONFIG")
	if path == "" {
		t.Skip("run scripts/build-matrix.sh for tagged profile acceptance")
	}
	cfg, e := config.Load(path)
	if e != nil {
		t.Fatal(e)
	}
	expected := cfg.Features.Enabled()
	registered := core.Registered()
	sort.Strings(registered)
	if !reflect.DeepEqual(expected, registered) {
		t.Fatalf("runtime registry=%v config=%v", registered, expected)
	}
	cfg.Storage.Path = filepath.Join(t.TempDir(), "qgramm.db")
	cfg.Storage.Files = t.TempDir()
	for _, name := range []string{cfg.Security.TokenPublicKeyEnv, cfg.Security.MasterKeyEnv, cfg.Security.HPKEKeyEnv} {
		t.Setenv(name, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	}
	t.Setenv(cfg.Security.ManagementSecretEnv, strings.Repeat("m", 32))
	c, e := core.Open(cfg, expected)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	enabled := map[string]bool{}
	for _, name := range expected {
		enabled[name] = true
	}
	routes := map[string]string{"groups": "POST /management/v1/chats/groups", "files": "POST /v1/chats/example/uploads", "e2ee": "POST /v1/mls/keypackages", "calls": "GET /v1/calls/turn", "delete": "DELETE /v1/chats/example/messages/example", "edit": "PATCH /v1/chats/example/messages/example", "reactions": "GET /v1/chats/example/messages/example/reactions", "ai": "POST /management/v1/ai/participants", "ai_streaming": "GET /v1/chats/example/ai/jobs/example/progress"}
	for feature, route := range routes {
		want := enabled[feature]
		if feature == "ai" {
			want = enabled["openai"] || enabled["anthropic"]
		}
		method, path, _ := strings.Cut(route, " ")
		_, pattern := c.Mux.Handler(httptest.NewRequest(method, path, nil))
		if (pattern != "") != want {
			t.Errorf("route %s present=%v want=%v", route, pattern != "", want)
		}
	}
	tables := map[string][]string{"files": {"uploads", "upload_chunks", "upload_messages"}, "calls": {"calls", "call_operations"}, "e2ee": {"mls_groups", "mls_roster", "mls_keypackages", "mls_controls", "mls_transitions"}, "delete": {"hidden_messages"}, "reactions": {"reactions"}, "ai": {"ai_chats", "ai_jobs", "ai_audit", "ai_agents", "ai_sessions", "ai_tasks"}, "ai_streaming": {"ai_progress", "ai_progress_heads", "ai_progress_versions"}}
	for feature, names := range tables {
		want := enabled[feature]
		if feature == "ai" {
			want = enabled["openai"] || enabled["anthropic"]
		}
		for _, name := range names {
			var present bool
			if e = c.DB.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)`, name).Scan(&present); e != nil {
				t.Fatal(e)
			}
			if present != want {
				t.Errorf("table %s present=%v want=%v", name, present, want)
			}
		}
	}
}

func TestMatrixInvalidConfigurations(t *testing.T) {
	dir := os.Getenv("QGRAMM_MATRIX_INVALID_DIR")
	if dir == "" {
		t.Skip("run scripts/build-matrix.sh for invalid profile acceptance")
	}
	paths, e := filepath.Glob(filepath.Join(dir, "*.toml"))
	if e != nil || len(paths) == 0 {
		t.Fatal("invalid profile fixtures missing")
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "must-not-write.json")
			if e := run([]string{"plan", "-config", path, "-out", out}); e == nil {
				t.Fatal("invalid profile accepted")
			}
			if _, e := os.Stat(out); !os.IsNotExist(e) {
				t.Fatal("invalid profile wrote output")
			}
		})
	}
}

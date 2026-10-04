// Package config defines the versioned runtime configuration. Secrets are
// environment variable references and are deliberately never loaded here.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Server   Server   `toml:"server"`
	Storage  Storage  `toml:"storage"`
	Security Security `toml:"security"`
	Features Features `toml:"features"`
	Capacity Capacity `toml:"capacity"`
	Policy   Policy   `toml:"policy"`
	AI       AI       `toml:"ai"`
	Calls    Calls    `toml:"calls"`
}
type Server struct {
	Listen                string   `toml:"listen"`
	TLSCert               string   `toml:"tls_cert"`
	TLSKey                string   `toml:"tls_key"`
	AllowInsecureLoopback bool     `toml:"allow_insecure_loopback"`
	TrustedProxy          bool     `toml:"trusted_proxy"`
	Origins               []string `toml:"origins"`
}
type Storage struct {
	Path  string `toml:"path"`
	Files string `toml:"files"`
}
type Security struct {
	Issuer              string `toml:"issuer"`
	Audience            string `toml:"audience"`
	TokenPublicKeyEnv   string `toml:"token_public_key_env"`
	ManagementSecretEnv string `toml:"management_secret_env"`
	MasterKeyEnv        string `toml:"master_key_env"`
	HPKEKeyEnv          string `toml:"hpke_key_env"`
}
type Features struct {
	Groups    bool `toml:"groups"`
	Files     bool `toml:"files"`
	E2EE      bool `toml:"e2ee"`
	Calls     bool `toml:"calls"`
	Delete    bool `toml:"delete"`
	Edit      bool `toml:"edit"`
	Reply     bool `toml:"reply"`
	Forward   bool `toml:"forward"`
	Reactions bool `toml:"reactions"`
	OpenAI    bool `toml:"openai"`
	Anthropic bool `toml:"anthropic"`
	MCP       bool `toml:"mcp"`
	HTTPTools bool `toml:"http_tools"`
}

func (f Features) Enabled() []string {
	names := map[string]bool{"groups": f.Groups, "files": f.Files, "e2ee": f.E2EE, "calls": f.Calls, "delete": f.Delete, "edit": f.Edit, "reply": f.Reply, "forward": f.Forward, "reactions": f.Reactions, "openai": f.OpenAI, "anthropic": f.Anthropic, "mcp": f.MCP, "http_tools": f.HTTPTools}
	out := []string{}
	for name, enabled := range names {
		if enabled {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

type Capacity struct {
	ExpectedConcurrentUsers int `toml:"expected_concurrent_users"`
	MaxConnections          int `toml:"max_connections"`
	QueueDepth              int `toml:"queue_depth"`
	Workers                 int `toml:"workers"`
}
type Policy struct {
	History             string   `toml:"history"`
	DeleteMode          string   `toml:"delete_mode"`
	ReactionTypes       []string `toml:"reaction_types"`
	EventRetentionHours int      `toml:"event_retention_hours"`
	DedupRetentionHours int      `toml:"dedup_retention_hours"`
	MaxMessageBytes     int      `toml:"max_message_bytes"`
	MaxBatch            int      `toml:"max_batch"`
	MaxFileBytes        int64    `toml:"max_file_bytes"`
	MaxChunkBytes       int64    `toml:"max_chunk_bytes"`
	MaxStorageBytes     int64    `toml:"max_storage_bytes"`
	UploadTTLHours      int      `toml:"upload_ttl_hours"`
}
type AI struct {
	OpenAIURL       string `toml:"openai_url"`
	AnthropicURL    string `toml:"anthropic_url"`
	OpenAIKeyEnv    string `toml:"openai_key_env"`
	AnthropicKeyEnv string `toml:"anthropic_key_env"`
	Model           string `toml:"model"`
	MaxSteps        int    `toml:"max_steps"`
	Tools           []Tool `toml:"tools"`
}
type Tool struct {
	Name         string         `toml:"name"`
	Kind         string         `toml:"kind"`
	URL          string         `toml:"url"`
	SecretEnv    string         `toml:"secret_env"`
	Methods      []string       `toml:"methods"`
	AllowPrivate bool           `toml:"allow_private"`
	Schema       map[string]any `toml:"schema"`
}
type Calls struct {
	TURNURLs             []string `toml:"turn_urls"`
	TURNSecretEnv        string   `toml:"turn_secret_env"`
	CredentialTTLSeconds int      `toml:"credential_ttl_seconds"`
}

func Defaults() Config {
	return Config{
		Server: Server{Listen: "127.0.0.1:8080"}, Storage: Storage{Path: "data/qgramm.db", Files: "data/files"},
		Security: Security{Issuer: "qgramm", Audience: "qgramm", TokenPublicKeyEnv: "QGRAMM_TOKEN_PUBLIC_KEY", ManagementSecretEnv: "QGRAMM_MANAGEMENT_SECRET", MasterKeyEnv: "QGRAMM_MASTER_KEY", HPKEKeyEnv: "QGRAMM_HPKE_KEY"},
		Capacity: Capacity{ExpectedConcurrentUsers: 100},
		Policy:   Policy{History: "since_join", DeleteMode: "global", ReactionTypes: []string{"👍", "❤️", "👎"}, EventRetentionHours: 720, DedupRetentionHours: 24, MaxMessageBytes: 65536, MaxBatch: 100, MaxFileBytes: 67108864, MaxChunkBytes: 1048576, MaxStorageBytes: 10737418240, UploadTTLHours: 24},
		AI:       AI{OpenAIURL: "https://api.openai.com/v1", AnthropicURL: "https://api.anthropic.com/v1", MaxSteps: 8},
		Calls:    Calls{CredentialTTLSeconds: 600},
	}
}

func Load(path string) (Config, error) {
	c := Defaults()
	data, err := os.ReadFile(path)
	if err != nil {
		return c, fmt.Errorf("read configuration: %w", err)
	}
	md, err := toml.Decode(string(data), &c)
	if err != nil {
		return c, fmt.Errorf("decode configuration: %w", err)
	}
	if unknown := md.Undecoded(); len(unknown) > 0 {
		names := []string{}
		for _, k := range unknown {
			names = append(names, k.String())
		}
		return c, fmt.Errorf("unknown configuration fields: %s", strings.Join(names, ", "))
	}
	if err = c.Validate(); err != nil {
		return c, err
	}
	c.Capacity = DeriveCapacity(c.Capacity, DetectResources())
	return c, nil
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func secretRef(name, value string, required bool) error {
	if value == "" && !required {
		return nil
	}
	if !envName.MatchString(value) {
		return fmt.Errorf("%s must reference an environment variable name", name)
	}
	return nil
}
func endpoint(name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("%s must be an HTTP(S) URL without credentials or fragment", name)
	}
	return nil
}
func (c Config) Validate() error {
	host, port, err := net.SplitHostPort(c.Server.Listen)
	if err != nil || port == "" {
		return fmt.Errorf("server.listen must be host:port")
	}
	portNumber, portErr := strconv.Atoi(port)
	if portErr != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("server.listen has invalid port")
	}
	if (c.Server.TLSCert == "") != (c.Server.TLSKey == "") {
		return fmt.Errorf("server TLS certificate and key must both be set")
	}
	if c.Server.TLSCert == "" && !c.Server.TrustedProxy {
		ip := net.ParseIP(host)
		if !c.Server.AllowInsecureLoopback || ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("TLS required unless trusted_proxy or explicit insecure loopback is configured")
		}
	}
	for _, origin := range c.Server.Origins {
		if err := endpoint("server.origins", origin); err != nil {
			return err
		}
		u, _ := url.Parse(origin)
		if u.Path != "" && u.Path != "/" || u.RawQuery != "" {
			return fmt.Errorf("server.origins must contain origins only")
		}
	}
	if c.Storage.Path == "" || c.Storage.Files == "" {
		return fmt.Errorf("storage paths must not be empty")
	}
	if c.Security.Issuer == "" || c.Security.Audience == "" {
		return fmt.Errorf("security issuer and audience are required")
	}
	for _, r := range []struct {
		name, value string
		required    bool
	}{{"security.token_public_key_env", c.Security.TokenPublicKeyEnv, true}, {"security.management_secret_env", c.Security.ManagementSecretEnv, true}, {"security.master_key_env", c.Security.MasterKeyEnv, true}, {"security.hpke_key_env", c.Security.HPKEKeyEnv, true}, {"ai.openai_key_env", c.AI.OpenAIKeyEnv, c.Features.OpenAI}, {"ai.anthropic_key_env", c.AI.AnthropicKeyEnv, c.Features.Anthropic}, {"calls.turn_secret_env", c.Calls.TURNSecretEnv, c.Features.Calls}} {
		if err := secretRef(r.name, r.value, r.required); err != nil {
			return err
		}
	}
	if c.Capacity.ExpectedConcurrentUsers <= 0 || c.Capacity.ExpectedConcurrentUsers > 1000000 || c.Capacity.MaxConnections < 0 || c.Capacity.QueueDepth < 0 || c.Capacity.Workers < 0 {
		return fmt.Errorf("capacity values invalid: expected users must be 1..1000000, explicit limits nonnegative")
	}
	if c.Capacity.MaxConnections > 0 && c.Capacity.MaxConnections < c.Capacity.ExpectedConcurrentUsers {
		return fmt.Errorf("capacity.max_connections is below expected_concurrent_users")
	}
	if c.Capacity.MaxConnections > 1000000 || c.Capacity.QueueDepth > 4096 || c.Capacity.Workers > 4096 {
		return fmt.Errorf("explicit capacity exceeds safety ceilings")
	}
	if c.Policy.History != "since_join" && c.Policy.History != "all" {
		return fmt.Errorf("policy.history must be since_join or all")
	}
	if c.Policy.DeleteMode != "global" && c.Policy.DeleteMode != "author_only" {
		return fmt.Errorf("policy.delete_mode must be global or author_only")
	}
	if c.Policy.EventRetentionHours <= 0 || c.Policy.DedupRetentionHours <= 0 || c.Policy.MaxMessageBytes <= 0 || c.Policy.MaxBatch <= 0 || c.Policy.MaxFileBytes <= 0 || c.Policy.MaxChunkBytes <= 0 || c.Policy.MaxChunkBytes > c.Policy.MaxFileBytes || c.Policy.MaxStorageBytes < c.Policy.MaxFileBytes || c.Policy.UploadTTLHours <= 0 {
		return fmt.Errorf("policy retention and size limits must be positive and consistent")
	}
	if c.Policy.MaxMessageBytes > 16*1024*1024 || c.Policy.MaxBatch > 1000 || c.Policy.MaxChunkBytes > 16*1024*1024 || c.Policy.MaxFileBytes > 1<<40 || c.Policy.MaxStorageBytes > 1<<49 || c.Policy.EventRetentionHours > 87600 || c.Policy.DedupRetentionHours > 87600 || c.Policy.UploadTTLHours > 87600 {
		return fmt.Errorf("policy exceeds safety ceilings")
	}
	if c.Features.Reactions && len(c.Policy.ReactionTypes) == 0 {
		return fmt.Errorf("reactions require policy.reaction_types")
	}
	seen := map[string]bool{}
	for _, r := range c.Policy.ReactionTypes {
		if r == "" || seen[r] {
			return fmt.Errorf("reaction_types must be unique and nonempty")
		}
		seen[r] = true
	}
	if (c.Features.MCP || c.Features.HTTPTools) && !(c.Features.OpenAI || c.Features.Anthropic) {
		return fmt.Errorf("AI tools require an enabled AI provider")
	}
	if c.Features.OpenAI {
		if err := endpoint("ai.openai_url", c.AI.OpenAIURL); err != nil {
			return err
		}
	}
	if c.Features.Anthropic {
		if err := endpoint("ai.anthropic_url", c.AI.AnthropicURL); err != nil {
			return err
		}
	}
	if c.Features.OpenAI || c.Features.Anthropic {
		if c.AI.Model == "" || c.AI.MaxSteps < 1 || c.AI.MaxSteps > 64 {
			return fmt.Errorf("AI requires a model and max_steps 1..64")
		}
	}
	seen = map[string]bool{}
	for _, tool := range c.AI.Tools {
		if tool.Name == "" || seen[tool.Name] {
			return fmt.Errorf("AI tool names must be unique and nonempty")
		}
		seen[tool.Name] = true
		if tool.Kind == "mcp" {
			if !c.Features.MCP {
				return fmt.Errorf("MCP tool requires mcp feature")
			}
		} else if tool.Kind == "http" {
			if !c.Features.HTTPTools {
				return fmt.Errorf("HTTP tool requires http_tools feature")
			}
		} else {
			return fmt.Errorf("tool.kind must be mcp or http")
		}
		if err := endpoint("tool.url", tool.URL); err != nil {
			return err
		}
		if err := secretRef("tool.secret_env", tool.SecretEnv, false); err != nil {
			return err
		}
		if tool.Kind == "http" && len(tool.Methods) == 0 {
			return fmt.Errorf("HTTP tools require an explicit method allowlist")
		}
		for _, m := range tool.Methods {
			switch m {
			case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
			default:
				return fmt.Errorf("invalid tool HTTP method")
			}
		}
	}
	if c.Features.Calls {
		if len(c.Calls.TURNURLs) == 0 || c.Calls.CredentialTTLSeconds < 60 || c.Calls.CredentialTTLSeconds > 86400 {
			return fmt.Errorf("calls require TURN URLs and credential TTL 60..86400")
		}
		for _, raw := range c.Calls.TURNURLs {
			u, e := url.Parse(raw)
			if e != nil || u.User != nil || strings.Contains(u.Opaque, "@") || (u.Scheme != "turn" && u.Scheme != "turns") || (u.Opaque == "" && u.Host == "") {
				return fmt.Errorf("invalid TURN URL")
			}
		}
	}
	return nil
}

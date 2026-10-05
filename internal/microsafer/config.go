//go:build qg_ai_endpoint && qg_e2ee

package microsafer

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is the standalone endpoint deployment file.  Secret fields are
// environment variable names, never secret values.
type Config struct {
	Endpoint EndpointConfig           `toml:"endpoint"`
	Peers    map[string]PeerConfig    `toml:"peers"`
	Chats    map[string]ChatConfig    `toml:"chats"`
	Handlers map[string]HandlerConfig `toml:"handlers"`
	Models   map[string]ModelConfig   `toml:"models"`
	Storage  StorageConfig            `toml:"storage"`
}

type EndpointConfig struct {
	Name                   string `toml:"name"`
	User                   string `toml:"user"`
	Device                 string `toml:"device"`
	ServerURL              string `toml:"server_url"`
	TokenEnv               string `toml:"token_env"`
	MasterKeyEnv           string `toml:"master_key_env"`
	HPKEKeyEnv             string `toml:"hpke_key_env"`
	DBPath                 string `toml:"db_path"`
	GrantPublicKeyEnv      string `toml:"grant_public_key_env"`
	Issuer                 string `toml:"issuer"`
	Audience               string `toml:"audience"`
	AllowExternalPlaintext bool   `toml:"allow_external_plaintext"`
	PollIntervalMS         int    `toml:"poll_interval_ms"`
	RequestTimeoutMS       int    `toml:"request_timeout_ms"`
	MaxFrameBytes          int    `toml:"max_frame_bytes"`
	MaxBodyBytes           int    `toml:"max_body_bytes"`
	MaxConcurrent          int    `toml:"max_concurrent"`
	MaxPending             int    `toml:"max_pending"`
}

type PeerConfig struct {
	User         string `toml:"user"`
	Device       string `toml:"device"`
	SigningKey   string `toml:"signing_key"`
	GroupID      string `toml:"group_id"`
	GroupContext string `toml:"group_context"`
	Epoch        uint64 `toml:"epoch"`
}

type ChatConfig struct {
	Peer         string `toml:"peer"`
	GroupID      string `toml:"group_id"`
	GroupContext string `toml:"group_context"`
}

type HandlerConfig struct {
	RequireApproval bool   `toml:"require_approval"`
	Kind            string `toml:"kind"`
	Model           string `toml:"model"`
	URL             string `toml:"url"`
	Method          string `toml:"method"`
	KeyEnv          string `toml:"key_env"`
	Operation       string `toml:"operation"`
	AllowPlaintext  bool   `toml:"allow_plaintext"`
	ApprovalSet     bool   `toml:"-"`
}

type ModelConfig struct {
	URL            string `toml:"url"`
	Model          string `toml:"model"`
	MaxTokens      int    `toml:"max_tokens"`
	MaxBodyBytes   int    `toml:"max_body_bytes"`
	AllowPlaintext bool   `toml:"allow_plaintext"`
}
type StorageConfig struct {
	Enabled           bool                             `toml:"enabled"`
	DBPath            string                           `toml:"db_path"`
	MasterKeyEnv      string                           `toml:"master_key_env"`
	HPKEKeyEnv        string                           `toml:"hpke_key_env"`
	GrantPublicKeyEnv string                           `toml:"grant_public_key_env"`
	Issuer            string                           `toml:"issuer"`
	Audience          string                           `toml:"audience"`
	GrantTTLSeconds   int                              `toml:"grant_ttl_seconds"`
	MaxResources      int                              `toml:"max_resources"`
	MaxDocuments      int                              `toml:"max_documents"`
	MaxEdges          int                              `toml:"max_edges"`
	MaxItemBytes      int                              `toml:"max_item_bytes"`
	MaxTextBytes      int                              `toml:"max_text_bytes"`
	MaxResults        int                              `toml:"max_results"`
	MaxGraphDepth     int                              `toml:"max_graph_depth"`
	Resources         map[string]StorageResourceConfig `toml:"resources"`
}
type StorageResourceConfig struct {
	Scope string `toml:"scope"`
	Owner string `toml:"owner"`
}

func (c *Config) setDefaults() {
	if c.Peers == nil {
		c.Peers = map[string]PeerConfig{}
	}
	if c.Chats == nil {
		c.Chats = map[string]ChatConfig{}
	}
	if c.Handlers == nil {
		c.Handlers = map[string]HandlerConfig{}
	}
	if c.Models == nil {
		c.Models = map[string]ModelConfig{}
	}
	if c.Endpoint.PollIntervalMS == 0 {
		c.Endpoint.PollIntervalMS = 500
	}
	if c.Endpoint.RequestTimeoutMS == 0 {
		c.Endpoint.RequestTimeoutMS = 120000
	}
	if c.Endpoint.MaxFrameBytes == 0 {
		c.Endpoint.MaxFrameBytes = 32 << 10
	}
	if c.Endpoint.MaxBodyBytes == 0 {
		c.Endpoint.MaxBodyBytes = 1 << 20
	}
	if c.Endpoint.MaxConcurrent == 0 {
		c.Endpoint.MaxConcurrent = 4
	}
	if c.Endpoint.MaxPending == 0 {
		c.Endpoint.MaxPending = 4096
	}
	if c.Storage.Enabled {
		if c.Storage.GrantTTLSeconds == 0 {
			c.Storage.GrantTTLSeconds = 900
		}
		if c.Storage.MaxResources == 0 {
			c.Storage.MaxResources = 1024
		}
		if c.Storage.MaxDocuments == 0 {
			c.Storage.MaxDocuments = 10000
		}
		if c.Storage.MaxEdges == 0 {
			c.Storage.MaxEdges = 20000
		}
		if c.Storage.MaxItemBytes == 0 {
			c.Storage.MaxItemBytes = 8 << 20
		}
		if c.Storage.MaxTextBytes == 0 {
			c.Storage.MaxTextBytes = 1 << 20
		}
		if c.Storage.MaxResults == 0 {
			c.Storage.MaxResults = 100
		}
		if c.Storage.MaxGraphDepth == 0 {
			c.Storage.MaxGraphDepth = 16
		}
		if c.Storage.Resources == nil {
			c.Storage.Resources = map[string]StorageResourceConfig{}
		}
	}
}

// LoadConfig reads and validates a deployment TOML file without reading any
// configured secret.  The same validation is used by the CLI and runtime.
func LoadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	md, err := toml.Decode(string(b), &c)
	if err != nil {
		return Config{}, fmt.Errorf("microsafer config: %w", err)
	}
	if unknown := md.Undecoded(); len(unknown) > 0 {
		return Config{}, fmt.Errorf("microsafer config: unknown field %q", unknown[0].String())
	}
	c.setDefaults()
	for name, h := range c.Handlers {
		if md.IsDefined("handlers", name, "require_approval") {
			h.ApprovalSet = true
			c.Handlers[name] = h
		} else {
			h.RequireApproval = true
			c.Handlers[name] = h
		}
	}
	if err = c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c Config) Validate() error {
	e := c.Endpoint
	for name, value := range map[string]string{"endpoint.name": e.Name, "endpoint.user": e.User, "endpoint.device": e.Device, "endpoint.server_url": e.ServerURL, "endpoint.token_env": e.TokenEnv, "endpoint.master_key_env": e.MasterKeyEnv, "endpoint.hpke_key_env": e.HPKEKeyEnv, "endpoint.db_path": e.DBPath, "endpoint.grant_public_key_env": e.GrantPublicKeyEnv, "endpoint.issuer": e.Issuer, "endpoint.audience": e.Audience} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("microsafer config: %s is required", name)
		}
	}
	u, err := url.Parse(e.ServerURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackHost(u.Hostname()))) {
		return fmt.Errorf("microsafer config: server_url must be https or loopback http")
	}
	if e.RequestTimeoutMS < 1 || e.RequestTimeoutMS > 1800000 || e.MaxFrameBytes < 1024 || e.MaxFrameBytes > 16<<20 || e.MaxBodyBytes < 1024 || e.MaxBodyBytes > 16<<20 || e.MaxConcurrent < 1 || e.MaxConcurrent > 64 || e.MaxPending < 1 || e.MaxPending > 1<<20 {
		return errors.New("microsafer config: invalid limits")
	}
	if dir := filepath.Dir(e.DBPath); dir != "." {
		if st, statErr := os.Stat(dir); statErr == nil && !st.IsDir() {
			return fmt.Errorf("microsafer config: db directory is not a directory")
		}
	}
	for chat, p := range c.Peers {
		if chat == "" || p.User == "" || p.Device == "" || p.SigningKey == "" || p.GroupID == "" {
			return fmt.Errorf("microsafer config: peer %q must pin user/device/signing_key/group_id", chat)
		}
		if _, err = decodePinned(p.SigningKey); err != nil || lenMustDecode(p.SigningKey) != 32 {
			return fmt.Errorf("microsafer config: peer %q signing_key must be 32-byte base64/hex", chat)
		}
		if _, err = decodePinned(p.GroupID); err != nil {
			return fmt.Errorf("microsafer config: peer %q group_id is invalid", chat)
		}
	}
	for chat, ch := range c.Chats {
		if chat == "" || ch.Peer == "" {
			return fmt.Errorf("microsafer config: chat %q must name peer", chat)
		}
		if _, ok := c.Peers[ch.Peer]; !ok {
			return fmt.Errorf("microsafer config: chat %q references unknown peer %q", chat, ch.Peer)
		}
	}
	for name, m := range c.Models {
		if name == "" || m.URL == "" || m.Model == "" {
			return fmt.Errorf("microsafer config: model %q is incomplete", name)
		}
		mu, xerr := url.Parse(m.URL)
		if xerr != nil || mu.Host == "" || (mu.Scheme != "https" && !(mu.Scheme == "http" && isLoopbackHost(mu.Hostname()))) {
			return fmt.Errorf("microsafer config: model %q URL must be https or loopback http", name)
		}
		if mu.User != nil || mu.RawQuery != "" || mu.Fragment != "" {
			return fmt.Errorf("microsafer config: model %q URL must not contain credentials, query, or fragment", name)
		}
	}
	for name, h := range c.Handlers {
		kind := strings.ToLower(strings.TrimSpace(h.Kind))
		if kind == "" {
			kind = "http"
		}
		if kind != "http" && kind != "mcp" && kind != "tool" && kind != "tools" && kind != "model" && kind != "llm" && kind != "storage" {
			return fmt.Errorf("microsafer config: handler %q has unsupported kind %q", name, h.Kind)
		}
		if (kind == "model" || kind == "llm") && h.Model == "" {
			return fmt.Errorf("microsafer config: model handler %q must name a model", name)
		}
		if (kind == "http" || kind == "mcp" || kind == "tool") && h.URL == "" {
			return fmt.Errorf("microsafer config: %s handler %q must configure url", kind, name)
		}
		if (kind == "mcp" || kind == "tool") && h.Operation != "" && h.Operation != "initialize" && h.Operation != "tools/call" {
			return fmt.Errorf("microsafer config: handler %q has unsupported MCP operation", name)
		}
		if h.URL == "" {
			continue
		}
		hu, xerr := url.Parse(h.URL)
		if xerr != nil || hu.Host == "" || (hu.Scheme != "https" && !(hu.Scheme == "http" && isLoopbackHost(hu.Hostname()))) {
			return fmt.Errorf("microsafer config: handler %q URL must be https or loopback http", name)
		}
		if hu.User != nil || hu.RawQuery != "" || hu.Fragment != "" {
			return fmt.Errorf("microsafer config: handler %q URL must not contain credentials, query, or fragment", name)
		}
	}
	if c.Storage.Enabled {
		if c.Storage.DBPath == "" {
			c.Storage.DBPath = e.DBPath + ".vault"
		}
		for field, value := range map[string]string{"storage.grant_public_key_env": c.Storage.GrantPublicKeyEnv, "storage.issuer": c.Storage.Issuer, "storage.audience": c.Storage.Audience} {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("microsafer config: %s is required when storage is enabled", field)
			}
		}
		if c.Storage.Audience == e.Audience {
			return errors.New("microsafer config: storage audience must differ from endpoint audience")
		}
		if c.Storage.MasterKeyEnv == "" {
			c.Storage.MasterKeyEnv = e.MasterKeyEnv
		}
		if c.Storage.HPKEKeyEnv == "" {
			c.Storage.HPKEKeyEnv = e.HPKEKeyEnv
		}
		for id, allow := range c.Storage.Resources {
			if id == "" || allow.Owner == "" || (allow.Scope != "shared" && allow.Scope != "user" && allow.Scope != "bot") {
				return fmt.Errorf("microsafer config: invalid storage resource allowlist %q", id)
			}
		}
	}
	return nil
}

func (c Config) peerForChat(chat string) (PeerConfig, bool) {
	if binding, ok := c.Chats[chat]; ok {
		peer, found := c.Peers[binding.Peer]
		return peer, found
	}
	peer, found := c.Peers[chat]
	return peer, found
}

func isLoopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback() || strings.EqualFold(host, "localhost")
}

func decodePinned(s string) ([]byte, error) {
	b, err := base64.StdEncoding.Strict().DecodeString(s)
	if err == nil {
		return b, nil
	}
	return hex.DecodeString(s)
}
func lenMustDecode(s string) int { b, _ := decodePinned(s); return len(b) }

func decodeSecretEnv(name string) ([]byte, error) {
	s := strings.TrimSpace(os.Getenv(name))
	if s == "" {
		return nil, fmt.Errorf("microsafer: secret environment %q is empty", name)
	}
	if b, err := base64.StdEncoding.Strict().DecodeString(s); err == nil && len(b) == 32 {
		return b, nil
	}
	if b, err := hex.DecodeString(s); err == nil && len(b) == 32 {
		return b, nil
	}
	return nil, fmt.Errorf("microsafer: secret environment %q must contain 32-byte base64 or hex", name)
}

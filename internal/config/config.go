// Package config defines the versioned runtime configuration. Secrets are
// environment variable references and are deliberately never loaded here.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"reflect"
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

// ConfigDetails describes the effective configuration without reading any
// runtime secret values. It is useful to build diagnostics and deployment
// plans while keeping Load's historical Config-only API intact.
type ConfigDetails struct {
	Config           Config
	Preset           string
	ExplicitPaths    []string
	PresetPaths      []string
	Sources          map[string]string
	DeclaredSources  map[string]string
	Resources        Resources
	DeclaredCapacity Capacity
	DerivedCapacity  Capacity
}

var presetNames = []string{"ai-anthropic", "ai-openai", "community", "minimal", "support"}

// presetValues returns only the curated fields owned by a preset. Every value
// remains overridable by an explicit TOML key decoded afterwards.
func presetValues(name string, c *Config) error {
	set := func(value func()) {
		value()
	}
	switch name {
	case "":
		return nil
	case "minimal":
		return nil
	case "support":
		set(func() { c.Features.Groups = true })
		set(func() { c.Features.Files = true })
		set(func() { c.Features.Delete = true })
		set(func() { c.Features.Edit = true })
		set(func() { c.Features.Reply = true })
		set(func() { c.Features.Reactions = true })
		return nil
	case "community":
		if err := presetValues("support", c); err != nil {
			return err
		}
		c.Features.Forward = true
		return nil
	case "ai-openai":
		c.Features.OpenAI = true
		c.AI.OpenAIKeyEnv = "QGRAMM_OPENAI_KEY"
		return nil
	case "ai-anthropic":
		c.Features.Anthropic = true
		c.AI.AnthropicKeyEnv = "QGRAMM_ANTHROPIC_KEY"
		return nil
	default:
		return fmt.Errorf("unknown preset; choose one of %s", strings.Join(presetNames, ", "))
	}
}

func presetPaths(name string) []string {
	switch name {
	case "support":
		return []string{"features.delete", "features.edit", "features.files", "features.groups", "features.reactions", "features.reply"}
	case "community":
		return []string{"features.delete", "features.edit", "features.files", "features.forward", "features.groups", "features.reactions", "features.reply"}
	case "ai-openai":
		return []string{"ai.openai_key_env", "features.openai"}
	case "ai-anthropic":
		return []string{"ai.anthropic_key_env", "features.anthropic"}
	default:
		return nil
	}
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
	Path                 string `toml:"path"`
	Files                string `toml:"files"`
	CheckpointIntervalMS int    `toml:"checkpoint_interval_ms"`
	CheckpointWALBytes   int64  `toml:"wal_checkpoint_bytes"`
}
type Security struct {
	Issuer                string   `toml:"issuer"`
	Audience              string   `toml:"audience"`
	TokenPublicKeyEnv     string   `toml:"token_public_key_env"`
	ManagementSecretEnv   string   `toml:"management_secret_env"`
	MasterKeyEnv          string   `toml:"master_key_env"`
	HPKEKeyEnv            string   `toml:"hpke_key_env"`
	PreviousMasterKeyEnvs []string `toml:"previous_master_key_envs"`
	PreviousHPKEKeyEnvs   []string `toml:"previous_hpke_key_envs"`
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
	OpenAIURL        string `toml:"openai_url"`
	AnthropicURL     string `toml:"anthropic_url"`
	OpenAIKeyEnv     string `toml:"openai_key_env"`
	AnthropicKeyEnv  string `toml:"anthropic_key_env"`
	Model            string `toml:"model"`
	MaxSteps         int    `toml:"max_steps"`
	MaxContextTurns  int    `toml:"max_context_turns"`
	MaxContextBytes  int    `toml:"max_context_bytes"`
	TimeoutSeconds   int    `toml:"timeout_seconds"`
	MaxResponseBytes int    `toml:"max_response_bytes"`
	Tools            []Tool `toml:"tools"`
}
type Tool struct {
	Name             string         `toml:"name"`
	Kind             string         `toml:"kind"`
	URL              string         `toml:"url"`
	SecretEnv        string         `toml:"secret_env"`
	Methods          []string       `toml:"methods"`
	AllowPrivate     bool           `toml:"allow_private"`
	Schema           map[string]any `toml:"schema"`
	TimeoutSeconds   int            `toml:"timeout_seconds"`
	MaxResponseBytes int            `toml:"max_response_bytes"`
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
		AI:       AI{OpenAIURL: "https://api.openai.com/v1", AnthropicURL: "https://api.anthropic.com/v1", MaxSteps: 8, MaxContextTurns: 20, MaxContextBytes: 262144, TimeoutSeconds: 45, MaxResponseBytes: 1048576},
		Calls:    Calls{CredentialTTLSeconds: 600},
	}
}

// Load preserves the original runtime API while using the same effective
// configuration path as qgramm-build.
func Load(path string) (Config, error) {
	d, err := LoadDetailed(path)
	if err != nil {
		return d.Config, err
	}
	return d.Config, nil
}

// LoadDetailed reads and resolves a configuration using the current host's
// resource inputs. It never reads environment variable values.
func LoadDetailed(path string) (ConfigDetails, error) {
	c := Defaults()
	data, err := os.ReadFile(path)
	if err != nil {
		return ConfigDetails{Config: c}, fmt.Errorf("read configuration: %w", err)
	}
	return parseDetailed(data, DetectResources())
}

// ParseDetailed resolves a TOML document using the current host's resource
// inputs. It is intended for offline validation and diagnostics.
func ParseDetailed(data []byte) (ConfigDetails, error) {
	return parseDetailed(data, DetectResources())
}

// Parse is the in-memory counterpart to Load and is useful to callers that
// already own a TOML document.
func Parse(data []byte) (Config, error) {
	d, err := ParseDetailed(data)
	return d.Config, err
}

// ParseDetailedWithResources makes capacity derivation deterministic for
// planning and tests while preserving the same schema and semantic checks.
func ParseDetailedWithResources(data []byte, resources Resources) (ConfigDetails, error) {
	return parseDetailed(data, resources)
}

func parseDetailed(data []byte, resources Resources) (ConfigDetails, error) {
	base := Defaults()
	var header struct {
		Preset string `toml:"preset"`
	}
	if _, err := toml.Decode(string(data), &header); err != nil {
		return ConfigDetails{Config: base}, fmt.Errorf("decode configuration: %w", err)
	}
	if err := presetValues(header.Preset, &base); err != nil {
		return ConfigDetails{Config: base, Preset: header.Preset}, err
	}
	md, err := toml.Decode(string(data), &base)
	if err != nil {
		return ConfigDetails{Config: base, Preset: header.Preset}, fmt.Errorf("decode configuration: %w", err)
	}
	unknown := make([]string, 0)
	explicit := make(map[string]bool)
	for _, k := range md.Keys() {
		path := strings.Join([]string(k), ".")
		if path == "preset" {
			continue
		}
		explicit[path] = true
	}
	for _, k := range md.Undecoded() {
		path := strings.Join([]string(k), ".")
		if path != "preset" {
			unknown = append(unknown, k.String())
		}
	}
	if len(unknown) > 0 {
		return ConfigDetails{Config: base, Preset: header.Preset, ExplicitPaths: sortedKeys(explicit), PresetPaths: presetPaths(header.Preset)}, fmt.Errorf("unknown configuration fields: %s", strings.Join(unknown, ", "))
	}
	if err = validateAIPresetModel(base, explicit, header.Preset); err != nil {
		return ConfigDetails{Config: base, Preset: header.Preset, ExplicitPaths: sortedKeys(explicit), PresetPaths: presetPaths(header.Preset)}, err
	}
	if err = base.Validate(); err != nil {
		return ConfigDetails{Config: base, Preset: header.Preset, ExplicitPaths: sortedKeys(explicit), PresetPaths: presetPaths(header.Preset)}, err
	}
	declared := base.Capacity
	base.Capacity = DeriveCapacity(base.Capacity, resources)
	d := ConfigDetails{
		Config:           base,
		Preset:           header.Preset,
		ExplicitPaths:    sortedKeys(explicit),
		PresetPaths:      presetPaths(header.Preset),
		Resources:        resources,
		DeclaredCapacity: declared,
		DerivedCapacity:  base.Capacity,
	}
	d.Sources = sourceMap(d, explicit)
	d.DeclaredSources = declaredSourceMap(d, explicit)
	if md.IsDefined("preset") {
		d.Sources["preset"] = "explicit"
		d.DeclaredSources["preset"] = "explicit"
	}
	return d, nil
}

func validateAIPresetModel(c Config, explicit map[string]bool, preset string) error {
	if (c.Features.OpenAI || c.Features.Anthropic) && (!explicit["ai.model"] || strings.TrimSpace(c.AI.Model) == "") {
		if strings.HasPrefix(preset, "ai-") {
			return fmt.Errorf("AI preset requires explicit ai.model")
		}
		return fmt.Errorf("AI requires an explicit ai.model")
	}
	return nil
}

func sortedKeys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func sourceMap(d ConfigDetails, explicit map[string]bool) map[string]string {
	sources := declaredSourceMap(d, explicit)
	for _, field := range []struct {
		path string
		zero bool
	}{
		{"capacity.max_connections", d.DeclaredCapacity.MaxConnections == 0},
		{"capacity.queue_depth", d.DeclaredCapacity.QueueDepth == 0},
		{"capacity.workers", d.DeclaredCapacity.Workers == 0},
	} {
		if field.zero {
			sources[field.path] = "derived"
		}
	}
	if d.Preset != "" {
		sources["preset"] = "explicit"
	}
	return sources
}

func declaredSourceMap(d ConfigDetails, explicit map[string]bool) map[string]string {
	sources := make(map[string]string)
	preset := make(map[string]bool, len(d.PresetPaths))
	for _, path := range d.PresetPaths {
		preset[path] = true
	}
	for _, path := range configLeafPaths(reflect.ValueOf(d.Config), "") {
		sources[path] = "default"
		if hasPath(explicit, path) {
			sources[path] = "explicit"
		} else if hasPath(preset, path) {
			sources[path] = "preset"
		}
	}
	if d.Preset != "" {
		sources["preset"] = "explicit"
	}
	return sources
}

func hasPath(values map[string]bool, path string) bool {
	if values[path] {
		return true
	}
	for value := range values {
		if strings.HasPrefix(value, path+".") {
			return true
		}
	}
	return false
}

func configLeafPaths(value reflect.Value, prefix string) []string {
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return []string{prefix}
		}
		return configLeafPaths(value.Elem(), prefix)
	}
	if value.Kind() != reflect.Struct {
		if prefix == "" {
			return nil
		}
		return []string{prefix}
	}
	typeOf := value.Type()
	paths := make([]string, 0)
	for i := 0; i < value.NumField(); i++ {
		field := typeOf.Field(i)
		name := strings.Split(field.Tag.Get("toml"), ",")[0]
		if name == "" || name == "-" || field.PkgPath != "" {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		if value.Field(i).Kind() == reflect.Struct {
			paths = append(paths, configLeafPaths(value.Field(i), path)...)
		} else {
			paths = append(paths, path)
		}
	}
	return paths
}

// EffectiveMap converts the typed configuration to a stable, TOML-keyed map
// suitable for JSON diagnostics. It contains references such as
// QGRAMM_MASTER_KEY as names, never the corresponding environment values.
func EffectiveMap(c Config) map[string]any {
	value, _ := configValueMap(reflect.ValueOf(c)).(map[string]any)
	return value
}

func configValueMap(value reflect.Value) any {
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		return configValueMap(value.Elem())
	}
	switch value.Kind() {
	case reflect.Struct:
		out := make(map[string]any)
		typeOf := value.Type()
		for i := 0; i < value.NumField(); i++ {
			field := typeOf.Field(i)
			name := strings.Split(field.Tag.Get("toml"), ",")[0]
			if name == "" || name == "-" || field.PkgPath != "" {
				continue
			}
			out[name] = configValueMap(value.Field(i))
		}
		return out
	case reflect.Slice, reflect.Array:
		out := make([]any, value.Len())
		for i := range out {
			out[i] = configValueMap(value.Index(i))
		}
		return out
	case reflect.Map:
		out := make(map[string]any)
		for _, key := range value.MapKeys() {
			if key.Kind() == reflect.String {
				out[key.String()] = configValueMap(value.MapIndex(key))
			}
		}
		return out
	default:
		return value.Interface()
	}
}

// SecretReferences returns configured environment variable names in stable
// order. It deliberately does not consult os.Environ or look up any value.
func SecretReferences(c Config) []string {
	refs := []string{c.Security.TokenPublicKeyEnv, c.Security.ManagementSecretEnv, c.Security.MasterKeyEnv, c.Security.HPKEKeyEnv}
	refs = append(refs, c.Security.PreviousMasterKeyEnvs...)
	refs = append(refs, c.Security.PreviousHPKEKeyEnvs...)
	if c.AI.OpenAIKeyEnv != "" {
		refs = append(refs, c.AI.OpenAIKeyEnv)
	}
	if c.AI.AnthropicKeyEnv != "" {
		refs = append(refs, c.AI.AnthropicKeyEnv)
	}
	if c.Calls.TURNSecretEnv != "" {
		refs = append(refs, c.Calls.TURNSecretEnv)
	}
	for _, tool := range c.AI.Tools {
		if tool.SecretEnv != "" {
			refs = append(refs, tool.SecretEnv)
		}
	}
	seen := make(map[string]bool, len(refs))
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref != "" && !seen[ref] {
			seen[ref] = true
			out = append(out, ref)
		}
	}
	sort.Strings(out)
	return out
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
	if c.Storage.CheckpointIntervalMS != 0 && (c.Storage.CheckpointIntervalMS < 100 || c.Storage.CheckpointIntervalMS > 60000 || c.Storage.Path == ":memory:") {
		return fmt.Errorf("storage.checkpoint_interval_ms must be 0 (disabled) or 100..60000 for a file database")
	}
	if c.Storage.CheckpointWALBytes != 0 && (c.Storage.CheckpointWALBytes < 65536 || c.Storage.CheckpointWALBytes > 1<<30 || c.Storage.CheckpointIntervalMS == 0) {
		return fmt.Errorf("storage.wal_checkpoint_bytes must be 0 (4MiB default) or 65536..1073741824 with checkpoint enabled")
	}
	if c.Security.Issuer == "" || c.Security.Audience == "" {
		return fmt.Errorf("security issuer and audience are required")
	}
	for field, refs := range map[string][]string{"previous_master_key_envs": c.Security.PreviousMasterKeyEnvs, "previous_hpke_key_envs": c.Security.PreviousHPKEKeyEnvs} {
		if len(refs) > 4 {
			return fmt.Errorf("security.%s allows at most four retired keys", field)
		}
		seen := map[string]bool{}
		for _, ref := range refs {
			if err := secretRef("security."+field, ref, true); err != nil {
				return err
			}
			if seen[ref] || ref == c.Security.MasterKeyEnv || ref == c.Security.HPKEKeyEnv {
				return fmt.Errorf("retired key references must be unique and distinct from active keys")
			}
			seen[ref] = true
		}
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
		if c.AI.MaxContextTurns < 1 || c.AI.MaxContextTurns > 20 || c.AI.MaxContextBytes < 1 || c.AI.MaxContextBytes > 262144 || c.AI.TimeoutSeconds < 1 || c.AI.TimeoutSeconds > 45 || c.AI.MaxResponseBytes < 1 || c.AI.MaxResponseBytes > 1048576 {
			return fmt.Errorf("AI limits require context turns 1..20, context bytes 1..262144, timeout seconds 1..45, response bytes 1..1048576")
		}
	}
	seen = map[string]bool{}
	for _, tool := range c.AI.Tools {
		if tool.TimeoutSeconds < 0 || tool.TimeoutSeconds > 45 || tool.MaxResponseBytes < 0 || tool.MaxResponseBytes > 1048576 {
			return fmt.Errorf("tool limits require timeout seconds 0..45 and response bytes 0..1048576 (0 inherits AI limit)")
		}
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
		if tool.Kind == "http" && len(tool.Methods) != 1 {
			return fmt.Errorf("HTTP tools require exactly one explicit method")
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

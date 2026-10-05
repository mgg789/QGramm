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
	AIPolicy AIPolicy `toml:"ai_policy"`
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
	Groups      bool `toml:"groups"`
	Files       bool `toml:"files"`
	E2EE        bool `toml:"e2ee"`
	Calls       bool `toml:"calls"`
	Delete      bool `toml:"delete"`
	Edit        bool `toml:"edit"`
	Reply       bool `toml:"reply"`
	Forward     bool `toml:"forward"`
	Reactions   bool `toml:"reactions"`
	OpenAI      bool `toml:"openai"`
	Anthropic   bool `toml:"anthropic"`
	AIStreaming bool `toml:"ai_streaming"`
	MCP         bool `toml:"mcp"`
	HTTPTools   bool `toml:"http_tools"`
	AIPolicy    bool `toml:"ai_policy"`
}

func (f Features) Enabled() []string {
	names := map[string]bool{"groups": f.Groups, "files": f.Files, "e2ee": f.E2EE, "calls": f.Calls, "delete": f.Delete, "edit": f.Edit, "reply": f.Reply, "forward": f.Forward, "reactions": f.Reactions, "openai": f.OpenAI, "anthropic": f.Anthropic, "ai_streaming": f.AIStreaming, "mcp": f.MCP, "http_tools": f.HTTPTools, "ai_policy": f.AIPolicy}
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
	OpenAIURL                             string                `toml:"openai_url"`
	AnthropicURL                          string                `toml:"anthropic_url"`
	OpenAIKeyEnv                          string                `toml:"openai_key_env"`
	AnthropicKeyEnv                       string                `toml:"anthropic_key_env"`
	Model                                 string                `toml:"model"`
	MaxSteps                              int                   `toml:"max_steps"`
	MaxContextTurns                       int                   `toml:"max_context_turns"`
	MaxContextBytes                       int                   `toml:"max_context_bytes"`
	TimeoutSeconds                        int                   `toml:"timeout_seconds"`
	MaxResponseBytes                      int                   `toml:"max_response_bytes"`
	MaxOutputTokens                       int                   `toml:"max_output_tokens"`
	InputPriceMicrounitsPerMillionTokens  int64                 `toml:"input_price_microunits_per_million_tokens"`
	OutputPriceMicrounitsPerMillionTokens int64                 `toml:"output_price_microunits_per_million_tokens"`
	AllowPrivate                          bool                  `toml:"allow_private"`
	Streaming                             bool                  `toml:"streaming"`
	Auth                                  string                `toml:"auth"`
	SystemPrompt                          string                `toml:"system_prompt"`
	Capabilities                          []string              `toml:"capabilities"`
	Tools                                 []Tool                `toml:"tools"`
	Endpoints                             map[string]AIEndpoint `toml:"endpoints"`
	Bots                                  map[string]AIBot      `toml:"bots"`
	DefaultBot                            string                `toml:"default_bot"`
}

// AIEndpoint is a named provider connection. Pointer overrides are deliberate:
// an explicit false or zero is distinct from an omitted value and therefore
// cannot accidentally inherit the global setting.
type AIEndpoint struct {
	Provider                              string   `toml:"provider"`
	URL                                   string   `toml:"url"`
	Model                                 string   `toml:"model"`
	KeyEnv                                string   `toml:"key_env"`
	Auth                                  string   `toml:"auth"`
	AllowPrivate                          *bool    `toml:"allow_private"`
	Streaming                             *bool    `toml:"streaming"`
	Capabilities                          []string `toml:"capabilities"`
	MaxSteps                              *int     `toml:"max_steps"`
	MaxContextTurns                       *int     `toml:"max_context_turns"`
	MaxContextBytes                       *int     `toml:"max_context_bytes"`
	TimeoutSeconds                        *int     `toml:"timeout_seconds"`
	MaxOutputTokens                       *int     `toml:"max_output_tokens"`
	MaxResponseBytes                      *int     `toml:"max_response_bytes"`
	InputPriceMicrounitsPerMillionTokens  *int64   `toml:"input_price_microunits_per_million_tokens"`
	OutputPriceMicrounitsPerMillionTokens *int64   `toml:"output_price_microunits_per_million_tokens"`
}

// AIBot names an AI participant configuration and points at one endpoint.
// Tools is nil when all configured global tools are allowed; an explicit empty
// array is a useful, safe way to deny tools for a bot.
type AIBot struct {
	Endpoint         string   `toml:"endpoint"`
	SystemPrompt     string   `toml:"system_prompt"`
	Tools            []string `toml:"tools"`
	AllowPrivate     *bool    `toml:"allow_private"`
	Streaming        *bool    `toml:"streaming"`
	MaxSteps         *int     `toml:"max_steps"`
	MaxContextTurns  *int     `toml:"max_context_turns"`
	MaxContextBytes  *int     `toml:"max_context_bytes"`
	TimeoutSeconds   *int     `toml:"timeout_seconds"`
	MaxOutputTokens  *int     `toml:"max_output_tokens"`
	MaxResponseBytes *int     `toml:"max_response_bytes"`
}

// ResolveBot returns a legacy-compatible AI settings value and its provider.
// Named bots are resolved only after Parse/Load validation, but the method
// still checks references so callers cannot accidentally run an unconfigured
// profile. An empty name resolves default_bot when configured, otherwise the
// existing global AI settings with no provider override.
func (c Config) ResolveBot(name string) (AI, string, error) {
	settings, provider, err := c.AI.resolveBot(name)
	if err != nil {
		return AI{}, "", err
	}
	if provider == "openai" && !c.Features.OpenAI {
		return AI{}, "", fmt.Errorf("AI bot provider openai is not enabled")
	}
	if provider == "anthropic" && !c.Features.Anthropic {
		return AI{}, "", fmt.Errorf("AI bot provider anthropic is not enabled")
	}
	if settings.Streaming && !c.Features.AIStreaming {
		return AI{}, "", fmt.Errorf("AI bot streaming requires ai_streaming feature")
	}
	return settings, provider, nil
}

// ResolveBot is also available directly on AI for runtimes that already hold
// the AI section of Config.
func (a AI) ResolveBot(name string) (AI, string, error) {
	return a.resolveBot(name)
}

func (a AI) resolveBot(name string) (AI, string, error) {
	if name == "" {
		name = a.DefaultBot
		if name == "" {
			if strings.TrimSpace(a.Model) == "" && len(a.Endpoints) > 0 {
				return AI{}, "", fmt.Errorf("AI default_bot is required when named endpoints are configured without a global model")
			}
			return a, "", nil
		}
	}
	bot, ok := a.Bots[name]
	if !ok {
		return AI{}, "", fmt.Errorf("AI bot %q is not configured", name)
	}
	ep, ok := a.Endpoints[bot.Endpoint]
	if !ok {
		return AI{}, "", fmt.Errorf("AI bot %q references unknown endpoint %q", name, bot.Endpoint)
	}
	out := a
	// The resolved value is intentionally still an AI, so existing adapters can
	// consume it without a second provider-specific configuration path.
	if ep.Provider == "openai" {
		out.OpenAIURL = ep.URL
		out.OpenAIKeyEnv = ep.KeyEnv
	} else if ep.Provider == "anthropic" {
		out.AnthropicURL = ep.URL
		out.AnthropicKeyEnv = ep.KeyEnv
	}
	out.Model = ep.Model
	out.Auth = ep.Auth
	if out.Auth == "" {
		out.Auth = "bearer"
	}
	out.Capabilities = append([]string(nil), ep.Capabilities...)
	if ep.Capabilities != nil && !containsAICapability(ep.Capabilities, "tool") {
		out.Tools = []Tool{}
	}
	if ep.AllowPrivate != nil {
		out.AllowPrivate = *ep.AllowPrivate
	}
	if ep.Streaming != nil {
		out.Streaming = *ep.Streaming
	}
	if ep.MaxSteps != nil {
		out.MaxSteps = *ep.MaxSteps
	}
	if ep.MaxContextTurns != nil {
		out.MaxContextTurns = *ep.MaxContextTurns
	}
	if ep.MaxContextBytes != nil {
		out.MaxContextBytes = *ep.MaxContextBytes
	}
	if ep.TimeoutSeconds != nil {
		out.TimeoutSeconds = *ep.TimeoutSeconds
	}
	if ep.MaxOutputTokens != nil {
		out.MaxOutputTokens = *ep.MaxOutputTokens
	}
	if ep.MaxResponseBytes != nil {
		out.MaxResponseBytes = *ep.MaxResponseBytes
	}
	if ep.InputPriceMicrounitsPerMillionTokens != nil {
		out.InputPriceMicrounitsPerMillionTokens = *ep.InputPriceMicrounitsPerMillionTokens
	}
	if ep.OutputPriceMicrounitsPerMillionTokens != nil {
		out.OutputPriceMicrounitsPerMillionTokens = *ep.OutputPriceMicrounitsPerMillionTokens
	}
	if bot.SystemPrompt != "" {
		out.SystemPrompt = bot.SystemPrompt
	}
	if bot.AllowPrivate != nil {
		out.AllowPrivate = *bot.AllowPrivate
	}
	if bot.Streaming != nil {
		out.Streaming = *bot.Streaming
	}
	if bot.MaxSteps != nil {
		out.MaxSteps = *bot.MaxSteps
	}
	if bot.MaxContextTurns != nil {
		out.MaxContextTurns = *bot.MaxContextTurns
	}
	if bot.MaxContextBytes != nil {
		out.MaxContextBytes = *bot.MaxContextBytes
	}
	if bot.TimeoutSeconds != nil {
		out.TimeoutSeconds = *bot.TimeoutSeconds
	}
	if bot.MaxOutputTokens != nil {
		out.MaxOutputTokens = *bot.MaxOutputTokens
	}
	if bot.MaxResponseBytes != nil {
		out.MaxResponseBytes = *bot.MaxResponseBytes
	}
	if bot.Tools != nil {
		allowed := make(map[string]bool, len(bot.Tools))
		for _, name := range bot.Tools {
			allowed[name] = true
		}
		tools := make([]Tool, 0, len(bot.Tools))
		for _, tool := range a.Tools {
			if allowed[tool.Name] {
				tools = append(tools, tool)
			}
		}
		out.Tools = tools
	}
	return out, ep.Provider, nil
}

type Tool struct {
	Name             string         `toml:"name"`
	Kind             string         `toml:"kind"`
	URL              string         `toml:"url"`
	SecretEnv        string         `toml:"secret_env"`
	Methods          []string       `toml:"methods"`
	AllowPrivate     bool           `toml:"allow_private"`
	RequireApproval  bool           `toml:"require_approval"`
	CostMicrounits   int64          `toml:"cost_microunits"`
	Schema           map[string]any `toml:"schema"`
	TimeoutSeconds   int            `toml:"timeout_seconds"`
	MaxResponseBytes int            `toml:"max_response_bytes"`
}

// AIPolicy contains the signed-grant, approval and accounting policy for the
// optional AI control plane. Values are policy estimates and enforcement
// inputs; they do not represent an external provider billing limit.
type AIPolicy struct {
	GrantPublicKeyEnv            string `toml:"grant_public_key_env"`
	Issuer                       string `toml:"issuer"`
	Audience                     string `toml:"audience"`
	ApprovalTTLSeconds           int    `toml:"approval_ttl_seconds"`
	RequireProviderApproval      bool   `toml:"require_provider_approval"`
	ProviderReserveMicrounits    int64  `toml:"provider_reserve_microunits"`
	GlobalDailyBudgetMicrounits  int64  `toml:"global_daily_budget_microunits"`
	PerBotDailyBudgetMicrounits  int64  `toml:"per_bot_daily_budget_microunits"`
	PerUserDailyBudgetMicrounits int64  `toml:"per_user_daily_budget_microunits"`
	Currency                     string `toml:"currency"`
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
		AI:       AI{OpenAIURL: "https://api.openai.com/v1", AnthropicURL: "https://api.anthropic.com/v1", MaxSteps: 8, MaxContextTurns: 20, MaxContextBytes: 262144, TimeoutSeconds: 45, MaxResponseBytes: 1048576, MaxOutputTokens: 2048, Auth: "bearer"},
		AIPolicy: AIPolicy{GrantPublicKeyEnv: "QGRAMM_AI_GRANT_PUBLIC_KEY", Issuer: "qgramm-backend", Audience: "qgramm-ai-control", ApprovalTTLSeconds: 300, ProviderReserveMicrounits: 1000000, Currency: "USD"},
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
	if (c.Features.OpenAI || c.Features.Anthropic) && strings.TrimSpace(c.AI.Model) == "" && (len(c.AI.Endpoints) == 0 || c.AI.DefaultBot == "") {
		if strings.HasPrefix(preset, "ai-") {
			return fmt.Errorf("AI preset requires explicit ai.model or ai.default_bot")
		}
		return fmt.Errorf("AI requires an explicit ai.model or ai.default_bot")
	}
	return nil
}

func hasAIEndpointProvider(endpoints map[string]AIEndpoint, provider string) bool {
	for _, endpoint := range endpoints {
		if endpoint.Provider == provider {
			return true
		}
	}
	return false
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
	for _, endpoint := range c.AI.Endpoints {
		if endpoint.KeyEnv != "" {
			refs = append(refs, endpoint.KeyEnv)
		}
	}
	if c.Calls.TURNSecretEnv != "" {
		refs = append(refs, c.Calls.TURNSecretEnv)
	}
	for _, tool := range c.AI.Tools {
		if tool.SecretEnv != "" {
			refs = append(refs, tool.SecretEnv)
		}
	}
	if c.Features.AIPolicy && c.AIPolicy.GrantPublicKeyEnv != "" {
		refs = append(refs, c.AIPolicy.GrantPublicKeyEnv)
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

var aiProfileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

const maxAIOutputTokens = 65536

func validateAIOverride(name string, value *int, min, max int) error {
	if value == nil {
		return nil
	}
	if *value < min || *value > max {
		return fmt.Errorf("%s must be %d..%d", name, min, max)
	}
	return nil
}

const maxAIPricingMicrounits = int64(1_000_000_000_000)

func validateAIPricing(name string, value int64) error {
	if value < 0 || value > maxAIPricingMicrounits {
		return fmt.Errorf("%s must be 0..%d", name, maxAIPricingMicrounits)
	}
	return nil
}

func validateAIPricingOverride(name string, value *int64) error {
	if value == nil {
		return nil
	}
	return validateAIPricing(name, *value)
}

func validateAIOverrides(prefix string, profile AIEndpoint) error {
	if err := validateAIOverride(prefix+".max_steps", profile.MaxSteps, 1, 64); err != nil {
		return err
	}
	if err := validateAIOverride(prefix+".max_context_turns", profile.MaxContextTurns, 1, 20); err != nil {
		return err
	}
	if err := validateAIOverride(prefix+".max_context_bytes", profile.MaxContextBytes, 1, 262144); err != nil {
		return err
	}
	if err := validateAIOverride(prefix+".timeout_seconds", profile.TimeoutSeconds, 1, 45); err != nil {
		return err
	}
	if err := validateAIOverride(prefix+".max_output_tokens", profile.MaxOutputTokens, 1, maxAIOutputTokens); err != nil {
		return err
	}
	if err := validateAIOverride(prefix+".max_response_bytes", profile.MaxResponseBytes, 1, 1048576); err != nil {
		return err
	}
	if err := validateAIPricingOverride(prefix+".input_price_microunits_per_million_tokens", profile.InputPriceMicrounitsPerMillionTokens); err != nil {
		return err
	}
	return validateAIPricingOverride(prefix+".output_price_microunits_per_million_tokens", profile.OutputPriceMicrounitsPerMillionTokens)
}

func validateAIBotOverrides(prefix string, profile AIBot) error {
	if err := validateAIOverride(prefix+".max_steps", profile.MaxSteps, 1, 64); err != nil {
		return err
	}
	if err := validateAIOverride(prefix+".max_context_turns", profile.MaxContextTurns, 1, 20); err != nil {
		return err
	}
	if err := validateAIOverride(prefix+".max_context_bytes", profile.MaxContextBytes, 1, 262144); err != nil {
		return err
	}
	if err := validateAIOverride(prefix+".timeout_seconds", profile.TimeoutSeconds, 1, 45); err != nil {
		return err
	}
	if err := validateAIOverride(prefix+".max_output_tokens", profile.MaxOutputTokens, 1, maxAIOutputTokens); err != nil {
		return err
	}
	return validateAIOverride(prefix+".max_response_bytes", profile.MaxResponseBytes, 1, 1048576)
}

func validateAIProfileName(kind, name string) error {
	if !aiProfileName.MatchString(name) {
		return fmt.Errorf("AI %s name must match %s", kind, aiProfileName.String())
	}
	return nil
}

func validateAICapabilities(name string, capabilities []string) error {
	seen := make(map[string]bool, len(capabilities))
	for _, capability := range capabilities {
		if capability != "basic_text" && capability != "tool" {
			return fmt.Errorf("%s has unsupported capability %q", name, capability)
		}
		if seen[capability] {
			return fmt.Errorf("%s capabilities must be unique", name)
		}
		seen[capability] = true
	}
	return nil
}

func containsAICapability(capabilities []string, wanted string) bool {
	for _, capability := range capabilities {
		if capability == wanted {
			return true
		}
	}
	return false
}

func validateAIAuth(name, auth string, allowPrivate bool) error {
	if auth == "" {
		auth = "bearer"
	}
	if auth != "bearer" && auth != "none" {
		return fmt.Errorf("%s.auth must be bearer or none", name)
	}
	if auth == "none" && !allowPrivate {
		return fmt.Errorf("%s.auth=none requires allow_private", name)
	}
	return nil
}

func (c Config) validateAINamedProfiles() error {
	for name, profile := range c.AI.Endpoints {
		if err := validateAIProfileName("endpoint", name); err != nil {
			return err
		}
		switch profile.Provider {
		case "openai":
			if !c.Features.OpenAI {
				return fmt.Errorf("AI endpoint %q requires the openai feature", name)
			}
		case "anthropic":
			if !c.Features.Anthropic {
				return fmt.Errorf("AI endpoint %q requires the anthropic feature", name)
			}
		default:
			return fmt.Errorf("AI endpoint %q provider must be openai or anthropic", name)
		}
		if profile.URL == "" {
			return fmt.Errorf("ai.endpoints.%s.url is required", name)
		}
		if err := endpoint("ai.endpoints."+name+".url", profile.URL); err != nil {
			return err
		}
		allowPrivate := c.AI.AllowPrivate
		if profile.AllowPrivate != nil {
			allowPrivate = *profile.AllowPrivate
		}
		if err := validateAIAuth("ai.endpoints."+name, profile.Auth, allowPrivate); err != nil {
			return err
		}
		if profile.Auth == "none" && profile.KeyEnv != "" {
			return fmt.Errorf("ai.endpoints.%s.key_env must be empty when auth is none", name)
		}
		urlAllowsPrivate := allowPrivate
		if !urlAllowsPrivate {
			for _, bot := range c.AI.Bots {
				if bot.Endpoint == name && bot.AllowPrivate != nil && *bot.AllowPrivate {
					urlAllowsPrivate = true
					break
				}
			}
		}
		if parsed, _ := url.Parse(profile.URL); parsed.Scheme == "http" && !urlAllowsPrivate {
			return fmt.Errorf("ai.endpoints.%s.http URL requires allow_private", name)
		}
		if strings.TrimSpace(profile.Model) == "" {
			return fmt.Errorf("ai.endpoints.%s.model is required", name)
		}
		if err := secretRef("ai.endpoints."+name+".key_env", profile.KeyEnv, profile.Auth != "none"); err != nil {
			return err
		}
		if profile.Streaming != nil && *profile.Streaming && !c.Features.AIStreaming {
			return fmt.Errorf("ai.endpoints.%s.streaming requires ai_streaming feature", name)
		}
		if err := validateAIOverrides("ai.endpoints."+name, profile); err != nil {
			return err
		}
		if err := validateAICapabilities("ai.endpoints."+name, profile.Capabilities); err != nil {
			return err
		}
	}
	for name, bot := range c.AI.Bots {
		if err := validateAIProfileName("bot", name); err != nil {
			return err
		}
		if bot.Endpoint == "" {
			return fmt.Errorf("ai.bots.%s.endpoint is required", name)
		}
		if _, ok := c.AI.Endpoints[bot.Endpoint]; !ok {
			return fmt.Errorf("AI bot %q references unknown endpoint %q", name, bot.Endpoint)
		}
		ep := c.AI.Endpoints[bot.Endpoint]
		if len(bot.Tools) > 0 && ep.Capabilities != nil && !containsAICapability(ep.Capabilities, "tool") {
			return fmt.Errorf("ai.bots.%s.tools require endpoint capability tool", name)
		}
		if bot.Streaming != nil && *bot.Streaming && !c.Features.AIStreaming {
			return fmt.Errorf("ai.bots.%s.streaming requires ai_streaming feature", name)
		}
		if err := validateAIBotOverrides("ai.bots."+name, bot); err != nil {
			return err
		}
		knownTools := make(map[string]bool, len(c.AI.Tools))
		for _, tool := range c.AI.Tools {
			knownTools[tool.Name] = true
		}
		seenTools := make(map[string]bool, len(bot.Tools))
		for _, tool := range bot.Tools {
			if tool == "" || !knownTools[tool] {
				return fmt.Errorf("ai.bots.%s.tools references unknown tool %q", name, tool)
			}
			if seenTools[tool] {
				return fmt.Errorf("ai.bots.%s.tools must be unique", name)
			}
			seenTools[tool] = true
		}
	}
	if c.AI.DefaultBot != "" {
		if _, ok := c.AI.Bots[c.AI.DefaultBot]; !ok {
			return fmt.Errorf("ai.default_bot references unknown bot %q", c.AI.DefaultBot)
		}
	}
	return nil
}

var aiCurrency = regexp.MustCompile(`^[A-Z]{3}$`)

func (c Config) validateAIPolicyConfig() error {
	defaults := Defaults().AIPolicy
	if !c.Features.AIPolicy {
		if c.AIPolicy != defaults {
			return fmt.Errorf("AI policy settings require the ai_policy feature")
		}
		if c.AI.InputPriceMicrounitsPerMillionTokens != 0 || c.AI.OutputPriceMicrounitsPerMillionTokens != 0 {
			return fmt.Errorf("AI pricing requires the ai_policy feature")
		}
		for _, endpoint := range c.AI.Endpoints {
			if (endpoint.InputPriceMicrounitsPerMillionTokens != nil && *endpoint.InputPriceMicrounitsPerMillionTokens != 0) || (endpoint.OutputPriceMicrounitsPerMillionTokens != nil && *endpoint.OutputPriceMicrounitsPerMillionTokens != 0) {
				return fmt.Errorf("AI endpoint pricing requires the ai_policy feature")
			}
		}
		for _, tool := range c.AI.Tools {
			if tool.RequireApproval || tool.CostMicrounits != 0 {
				return fmt.Errorf("AI tool approval and cost require the ai_policy feature")
			}
		}
		return nil
	}
	if !c.Features.OpenAI && !c.Features.Anthropic {
		return fmt.Errorf("ai_policy requires a compiled AI provider")
	}
	if err := secretRef("ai_policy.grant_public_key_env", c.AIPolicy.GrantPublicKeyEnv, true); err != nil {
		return err
	}
	if c.AIPolicy.Issuer == "" || c.AIPolicy.Audience == "" {
		return fmt.Errorf("ai_policy issuer and audience are required")
	}
	if c.AIPolicy.Issuer == c.AIPolicy.Audience || c.AIPolicy.Issuer == c.Security.Issuer || c.AIPolicy.Issuer == c.Security.Audience || c.AIPolicy.Audience == c.Security.Issuer || c.AIPolicy.Audience == c.Security.Audience {
		return fmt.Errorf("ai_policy issuer and audience must be separate from device authentication")
	}
	if c.AIPolicy.ApprovalTTLSeconds < 1 || c.AIPolicy.ApprovalTTLSeconds > 900 {
		return fmt.Errorf("ai_policy.approval_ttl_seconds must be 1..900")
	}
	if c.AIPolicy.ProviderReserveMicrounits < 0 || c.AIPolicy.ProviderReserveMicrounits > maxAIPricingMicrounits {
		return fmt.Errorf("ai_policy.provider_reserve_microunits must be 0..%d", maxAIPricingMicrounits)
	}
	for name, value := range map[string]int64{
		"global_daily_budget_microunits":   c.AIPolicy.GlobalDailyBudgetMicrounits,
		"per_bot_daily_budget_microunits":  c.AIPolicy.PerBotDailyBudgetMicrounits,
		"per_user_daily_budget_microunits": c.AIPolicy.PerUserDailyBudgetMicrounits,
	} {
		if value < 0 || value > 1_000_000_000_000_000 {
			return fmt.Errorf("ai_policy.%s must be 0..1000000000000000", name)
		}
	}
	if (c.AIPolicy.GlobalDailyBudgetMicrounits > 0 || c.AIPolicy.PerBotDailyBudgetMicrounits > 0 || c.AIPolicy.PerUserDailyBudgetMicrounits > 0) && c.AIPolicy.ProviderReserveMicrounits == 0 {
		return fmt.Errorf("ai_policy provider reserve must be positive when a daily budget is configured")
	}
	if !aiCurrency.MatchString(c.AIPolicy.Currency) {
		return fmt.Errorf("ai_policy.currency must be exactly three uppercase letters")
	}
	if err := validateAIPricing("ai.input_price_microunits_per_million_tokens", c.AI.InputPriceMicrounitsPerMillionTokens); err != nil {
		return err
	}
	return validateAIPricing("ai.output_price_microunits_per_million_tokens", c.AI.OutputPriceMicrounitsPerMillionTokens)
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
	if err := c.validateAIPolicyConfig(); err != nil {
		return err
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
	}{{"security.token_public_key_env", c.Security.TokenPublicKeyEnv, true}, {"security.management_secret_env", c.Security.ManagementSecretEnv, true}, {"security.master_key_env", c.Security.MasterKeyEnv, true}, {"security.hpke_key_env", c.Security.HPKEKeyEnv, true}, {"ai.openai_key_env", c.AI.OpenAIKeyEnv, c.Features.OpenAI && c.AI.Auth != "none" && !hasAIEndpointProvider(c.AI.Endpoints, "openai")}, {"ai.anthropic_key_env", c.AI.AnthropicKeyEnv, c.Features.Anthropic && c.AI.Auth != "none" && !hasAIEndpointProvider(c.AI.Endpoints, "anthropic")}, {"calls.turn_secret_env", c.Calls.TURNSecretEnv, c.Features.Calls}} {
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
	if c.Features.AIStreaming && !(c.Features.OpenAI || c.Features.Anthropic) {
		return fmt.Errorf("AI streaming requires an enabled provider")
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
		if err := validateAIAuth("ai", c.AI.Auth, c.AI.AllowPrivate); err != nil {
			return err
		}
		if c.AI.Auth == "none" && (c.AI.OpenAIKeyEnv != "" || c.AI.AnthropicKeyEnv != "") {
			return fmt.Errorf("ai key_env values must be empty when auth is none")
		}
		if (c.AI.Model == "" && (len(c.AI.Endpoints) == 0 || c.AI.DefaultBot == "")) || c.AI.MaxSteps < 1 || c.AI.MaxSteps > 64 {
			return fmt.Errorf("AI requires a model (or named endpoints with default_bot) and max_steps 1..64")
		}
		if c.AI.MaxContextTurns < 1 || c.AI.MaxContextTurns > 20 || c.AI.MaxContextBytes < 1 || c.AI.MaxContextBytes > 262144 || c.AI.TimeoutSeconds < 1 || c.AI.TimeoutSeconds > 45 || c.AI.MaxResponseBytes < 1 || c.AI.MaxResponseBytes > 1048576 || c.AI.MaxOutputTokens < 1 || c.AI.MaxOutputTokens > maxAIOutputTokens {
			return fmt.Errorf("AI limits require context turns 1..20, context bytes 1..262144, timeout seconds 1..45, response bytes 1..1048576, output tokens 1..%d", maxAIOutputTokens)
		}
		if c.AI.Streaming && !c.Features.AIStreaming {
			return fmt.Errorf("ai.streaming requires ai_streaming feature")
		}
	}
	if err := validateAICapabilities("ai", c.AI.Capabilities); err != nil {
		return err
	}
	if err := c.validateAINamedProfiles(); err != nil {
		return err
	}
	seen = map[string]bool{}
	for _, tool := range c.AI.Tools {
		if tool.TimeoutSeconds < 0 || tool.TimeoutSeconds > 45 || tool.MaxResponseBytes < 0 || tool.MaxResponseBytes > 1048576 {
			return fmt.Errorf("tool limits require timeout seconds 0..45 and response bytes 0..1048576 (0 inherits AI limit)")
		}
		if tool.Name == "" || seen[tool.Name] {
			return fmt.Errorf("AI tool names must be unique and nonempty")
		}
		if tool.CostMicrounits < 0 || tool.CostMicrounits > maxAIPricingMicrounits {
			return fmt.Errorf("tool cost_microunits must be 0..%d", maxAIPricingMicrounits)
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

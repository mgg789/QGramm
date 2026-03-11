package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type Config struct {
	Env      string
	Server   ServerConfig
	Database DatabaseConfig
	Security SecurityConfig
	Limits   LimitsConfig
	Storage  StorageConfig
	Root     RootConfig
	Runtime  RuntimeConfig
}

type ServerConfig struct {
	Host string
	Port int
}

type DatabaseConfig struct {
	DSN string
}

type SecurityConfig struct {
	JWTSecret          string
	JWTIssuer          string
	AccessTokenTTL     time.Duration
	VerificationPepper string
}

type LimitsConfig struct {
	MaxUploadChunkSizeBytes    int64       `json:"max_upload_chunk_size_bytes"`
	MaxFileSizeBytes           int64       `json:"max_file_size_bytes"`
	MaxVoiceSizeBytes          int64       `json:"max_voice_size_bytes"`
	MaxVideoNoteSizeBytes      int64       `json:"max_video_note_size_bytes"`
	MaxAvatarSizeBytes         int64       `json:"max_avatar_size_bytes"`
	MaxStoragePerUserBytes     int64       `json:"max_storage_per_user_bytes"`
	MaxProjectStorageBytes     int64       `json:"max_project_storage_bytes"`
	MaxParallelUploadsPerUser  int         `json:"max_parallel_uploads_per_user"`
	MessageRateLimitPerMinute  int         `json:"message_rate_limit_per_minute"`
	MaxUniqueRecipientsPerHour int         `json:"max_unique_recipients_per_hour"`
	InviteWeeklyLimits         map[int]int `json:"invite_weekly_limits"`
}

type StorageConfig struct {
	DataDir                   string `json:"data_dir"`
	TmpDir                    string `json:"tmp_dir"`
	AttachmentsDir            string `json:"attachments_dir"`
	AvatarsDir                string `json:"avatars_dir"`
	RetentionDaysTmpUploads   int    `json:"retention_days_tmp_uploads"`
	RetentionDaysOrphanChunks int    `json:"retention_days_orphan_chunks"`
	CleanupIntervalMinutes    int    `json:"cleanup_interval_minutes"`
}

type RootConfig struct {
	RootEmail              string `json:"root_email"`
	RootNickname           string `json:"root_nickname"`
	RootFirstName          string `json:"root_first_name"`
	RootLastName           string `json:"root_last_name"`
	RootUID                string `json:"root_uid"`
	QGrammServiceEmail     string `json:"qgramm_service_email"`
	QGrammServiceNickname  string `json:"qgramm_service_nickname"`
	QGrammServiceFirstName string `json:"qgramm_service_first_name"`
	QGrammServiceLastName  string `json:"qgramm_service_last_name"`
	QGrammServiceUID       string `json:"qgramm_service_uid"`
}

type RuntimeConfig struct {
	Debug                       bool          `json:"debug"`
	ExposeDebugVerificationCode bool          `json:"expose_debug_verification_code"`
	Captcha                     CaptchaConfig `json:"captcha"`
	SMTP                        SMTPConfig    `json:"smtp"`
	Trust                       TrustConfig   `json:"trust"`
	Invite                      InviteConfig  `json:"invite"`
}

type CaptchaConfig struct {
	Mode            string `json:"mode"`
	MockValidToken  string `json:"mock_valid_token"`
	TurnstileSecret string `json:"turnstile_secret"`
	VerifyURL       string `json:"verify_url"`
}

type SMTPConfig struct {
	Enabled  bool   `json:"enabled"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	From     string `json:"from"`
}

type TrustConfig struct {
	Level2MinAccountAgeDays   int `json:"level2_min_account_age_days"`
	Level2MinActiveDays       int `json:"level2_min_active_days"`
	Level3MinAccountAgeDays   int `json:"level3_min_account_age_days"`
	Level4MinQualifiedDays    int `json:"level4_min_qualified_days"`
	QualifiedDayMinActiveSecs int `json:"qualified_day_min_active_seconds"`
	Level5MinAccountAgeDays   int `json:"level5_min_account_age_days"`
	Level5MinActiveDays       int `json:"level5_min_active_days"`
}

type InviteConfig struct {
	InviteLifetimeHours           int `json:"invite_lifetime_hours"`
	DeviceActivationLifetimeHours int `json:"device_activation_lifetime_hours"`
}

func Load() (Config, error) {
	cfg := Config{}
	cfg.Env = getEnv("QGRAMM_ENV", "development")
	cfg.Server.Host = getEnv("QGRAMM_SERVER_HOST", "0.0.0.0")
	cfg.Server.Port = getEnvAsInt("QGRAMM_SERVER_PORT", 8080)

	cfg.Database.DSN = os.Getenv("QGRAMM_DATABASE_DSN")
	if cfg.Database.DSN == "" {
		return Config{}, errors.New("QGRAMM_DATABASE_DSN is required")
	}

	cfg.Security.JWTSecret = os.Getenv("QGRAMM_JWT_SECRET")
	if cfg.Security.JWTSecret == "" {
		return Config{}, errors.New("QGRAMM_JWT_SECRET is required")
	}
	cfg.Security.JWTIssuer = getEnv("QGRAMM_JWT_ISSUER", "qgramm-backend")
	cfg.Security.AccessTokenTTL = time.Duration(getEnvAsInt("QGRAMM_ACCESS_TOKEN_TTL_HOURS", 72)) * time.Hour
	cfg.Security.VerificationPepper = getEnv("QGRAMM_VERIFICATION_PEPPER", cfg.Security.JWTSecret)

	configDir := getEnv("QGRAMM_CONFIG_DIR", "./configs")

	if err := loadJSON(filepath.Join(configDir, "limits.json"), &cfg.Limits); err != nil {
		return Config{}, fmt.Errorf("load limits.json: %w", err)
	}
	if err := loadJSON(filepath.Join(configDir, "storage.json"), &cfg.Storage); err != nil {
		return Config{}, fmt.Errorf("load storage.json: %w", err)
	}
	if err := loadJSON(filepath.Join(configDir, "root.json"), &cfg.Root); err != nil {
		return Config{}, fmt.Errorf("load root.json: %w", err)
	}
	if err := loadJSON(filepath.Join(configDir, "runtime.json"), &cfg.Runtime); err != nil {
		return Config{}, fmt.Errorf("load runtime.json: %w", err)
	}

	applyConfigDefaults(&cfg)

	if err := validate(cfg); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func applyConfigDefaults(cfg *Config) {
	if cfg.Limits.InviteWeeklyLimits == nil {
		cfg.Limits.InviteWeeklyLimits = map[int]int{1: 0, 2: 3, 3: 3, 4: 3, 5: 10}
	}
	if cfg.Runtime.Captcha.Mode == "" {
		cfg.Runtime.Captcha.Mode = "mock"
	}
	if cfg.Runtime.Invite.InviteLifetimeHours <= 0 {
		cfg.Runtime.Invite.InviteLifetimeHours = 96
	}
	if cfg.Runtime.Invite.DeviceActivationLifetimeHours <= 0 {
		cfg.Runtime.Invite.DeviceActivationLifetimeHours = 720
	}
	if cfg.Security.AccessTokenTTL <= 0 {
		cfg.Security.AccessTokenTTL = 72 * time.Hour
	}
}

func validate(cfg Config) error {
	if cfg.Server.Port <= 0 || cfg.Server.Port > 65535 {
		return errors.New("invalid server port")
	}
	if cfg.Limits.MaxUploadChunkSizeBytes <= 0 {
		return errors.New("max_upload_chunk_size_bytes must be > 0")
	}
	if cfg.Limits.MaxStoragePerUserBytes <= 0 {
		return errors.New("max_storage_per_user_bytes must be > 0")
	}
	if cfg.Root.RootEmail == "" || cfg.Root.RootNickname == "" {
		return errors.New("root account settings are required")
	}
	if cfg.Root.QGrammServiceEmail == "" || cfg.Root.QGrammServiceNickname == "" {
		return errors.New("qgramm service account settings are required")
	}
	return nil
}

func loadJSON(path string, dst any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return err
	}
	return nil
}

func getEnv(key, defaultValue string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return defaultValue
}

func getEnvAsInt(key string, defaultValue int) int {
	valueStr := os.Getenv(key)
	if valueStr == "" {
		return defaultValue
	}
	value, err := strconv.Atoi(valueStr)
	if err != nil {
		return defaultValue
	}
	return value
}

// Package config manages secure application configuration via environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all configuration for the Memory Core service.
type Config struct {
	Server    ServerConfig
	Database  DatabaseConfig
	Redis     RedisConfig
	SQLite    SQLiteConfig
	Object    ObjectStorageConfig
	Embedding EmbeddingConfig
	LLM       LLMConfig
	Worker    WorkerConfig
	Auth      AuthConfig
	Security  SecurityConfig
	Plugins   PluginsConfig
}

// ServerConfig defines HTTP/gRPC server settings.
type ServerConfig struct {
	HTTPPort      int           `env:"HTTP_PORT" default:"8080"`
	GRPCPort      int           `env:"GRPC_PORT" default:"9090"`
	ReadTimeout   time.Duration `env:"READ_TIMEOUT" default:"30s"`
	WriteTimeout  time.Duration `env:"WRITE_TIMEOUT" default:"30s"`
	MaxBodySize   int64         `env:"MAX_BODY_SIZE" default:"10485760"` // 10 MB
	EnableCORS    bool          `env:"ENABLE_CORS" default:"true"`
	EnableMetrics bool          `env:"ENABLE_METRICS" default:"true"`
	TLS           TLSConfig
}

// TLSConfig defines optional TLS settings for the HTTP server.
type TLSConfig struct {
	Enabled  bool   `env:"TLS_ENABLED" default:"false"`
	CertFile string `env:"TLS_CERT_FILE" default:""`
	KeyFile  string `env:"TLS_KEY_FILE" default:""`
}

// DatabaseConfig defines PostgreSQL connection settings.
type DatabaseConfig struct {
	Host            string        `env:"DB_HOST" default:"localhost"`
	Port            int           `env:"DB_PORT" default:"5432"`
	User            string        `env:"DB_USER" default:"memoryos"`
	Password        string        `env:"DB_PASSWORD" default:""`
	DBName          string        `env:"DB_NAME" default:"memoryos"`
	SSLMode         string        `env:"DB_SSLMODE" default:"require"`
	MaxOpenConns    int           `env:"DB_MAX_OPEN_CONNS" default:"25"`
	MaxIdleConns    int           `env:"DB_MAX_IDLE_CONNS" default:"10"`
	ConnMaxLifetime time.Duration `env:"DB_CONN_MAX_LIFETIME" default:"5m"`
}

// DSN returns the PostgreSQL connection string.
// The password is never logged; this string is only used for sql.Open.
func (d DatabaseConfig) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		d.Host, d.Port, d.User, d.Password, d.DBName, d.SSLMode,
	)
}

// RedisConfig defines Redis connection settings.
type RedisConfig struct {
	Host       string `env:"REDIS_HOST" default:"localhost"`
	Port       int    `env:"REDIS_PORT" default:"6379"`
	Password   string `env:"REDIS_PASSWORD" default:""`
	DB         int    `env:"REDIS_DB" default:"0"`
	MaxRetries int    `env:"REDIS_MAX_RETRIES" default:"3"`
	PoolSize   int    `env:"REDIS_POOL_SIZE" default:"20"`
}

// Addr returns the Redis address string.
func (r RedisConfig) Addr() string {
	return fmt.Sprintf("%s:%d", r.Host, r.Port)
}

// SQLiteConfig defines SQLite connection settings.
type SQLiteConfig struct {
	Path          string `env:"SQLITE_PATH" default:"./data/sessions.db"`
	JournalMode   string `env:"SQLITE_JOURNAL_MODE" default:"WAL"`
	CacheSize     int    `env:"SQLITE_CACHE_SIZE" default:"-2000"`
	BusyTimeoutMs int    `env:"SQLITE_BUSY_TIMEOUT" default:"5000"`
}

// ObjectStorageConfig defines S3-compatible object storage settings.
type ObjectStorageConfig struct {
	Endpoint  string `env:"OBJ_ENDPOINT" default:"localhost:9000"`
	AccessKey string `env:"OBJ_ACCESS_KEY" default:""`
	SecretKey string `env:"OBJ_SECRET_KEY" default:""`
	Bucket    string `env:"OBJ_BUCKET" default:"memoryos-raw"`
	UseSSL    bool   `env:"OBJ_USE_SSL" default:"false"`
}

// EmbeddingConfig defines the embedding model settings.
type EmbeddingConfig struct {
	Provider   string `env:"EMBEDDING_PROVIDER" default:"openai"`
	Model      string `env:"EMBEDDING_MODEL" default:"text-embedding-3-small"`
	Dimensions int    `env:"EMBEDDING_DIMENSIONS" default:"1536"`
	BatchSize  int    `env:"EMBEDDING_BATCH_SIZE" default:"32"`
	APIKey     string `env:"EMBEDDING_API_KEY" default:""`
	BaseURL    string `env:"EMBEDDING_BASE_URL" default:""`
}

// LLMConfig defines the LLM settings for summary and extraction.
type LLMConfig struct {
	Provider  string `env:"LLM_PROVIDER" default:"openai"`
	Model     string `env:"LLM_MODEL" default:"gpt-4o-mini"`
	APIKey    string `env:"LLM_API_KEY" default:""`
	BaseURL   string `env:"LLM_BASE_URL" default:""`
	MaxTokens int    `env:"LLM_MAX_TOKENS" default:"4096"`
}

// WorkerConfig defines the async worker pool settings.
type WorkerConfig struct {
	Concurrency      int           `env:"WORKER_CONCURRENCY" default:"4"`
	QueueSize        int           `env:"WORKER_QUEUE_SIZE" default:"1000"`
	PollInterval     time.Duration `env:"WORKER_POLL_INTERVAL" default:"100ms"`
	TaskTimeout      time.Duration `env:"WORKER_TASK_TIMEOUT" default:"5m"`
	RetryMaxAttempts int           `env:"WORKER_RETRY_MAX" default:"3"`
	RetryBackoff     time.Duration `env:"WORKER_RETRY_BACKOFF" default:"1s"`
}

// AuthConfig defines authentication settings.
type AuthConfig struct {
	JWTSecret        string        `env:"AUTH_JWT_SECRET" default:""`
	JWTExpiry        time.Duration `env:"AUTH_JWT_EXPIRY" default:"24h"`
	APIKeyHeader     string        `env:"AUTH_API_KEY_HEADER" default:"X-API-Key"`
	EnableAPIKeyAuth bool          `env:"AUTH_ENABLE_API_KEY" default:"true"`
}

// SecurityConfig defines security-related settings.
type SecurityConfig struct {
	RateLimitRPS   float64 `env:"SEC_RATE_LIMIT_RPS" default:"100"`
	RateLimitBurst int     `env:"SEC_RATE_LIMIT_BURST" default:"200"`
	MaxBodySize    int64   `env:"SEC_MAX_BODY_SIZE" default:"10485760"`
}

// PluginsConfig defines plugin loading settings.
type PluginsConfig struct {
	Dir      string   `env:"PLUGINS_DIR" default:"./plugins"`
	Enabled  []string `env:"PLUGINS_ENABLED"`
	Disabled []string `env:"PLUGINS_DISABLED"`
}

// Load reads configuration from environment variables.
func Load() (*Config, error) {
	cfg := &Config{}

	cfg.Server = ServerConfig{
		HTTPPort:      getEnvInt("HTTP_PORT", 8080),
		GRPCPort:      getEnvInt("GRPC_PORT", 9090),
		ReadTimeout:   getEnvDuration("READ_TIMEOUT", 30*time.Second),
		WriteTimeout:  getEnvDuration("WRITE_TIMEOUT", 30*time.Second),
		MaxBodySize:   getEnvInt64("MAX_BODY_SIZE", 10*1024*1024),
		EnableCORS:    getEnvBool("ENABLE_CORS", true),
		EnableMetrics: getEnvBool("ENABLE_METRICS", true),
		TLS: TLSConfig{
			Enabled:  getEnvBool("TLS_ENABLED", false),
			CertFile: getEnv("TLS_CERT_FILE", ""),
			KeyFile:  getEnv("TLS_KEY_FILE", ""),
		},
	}

	cfg.Database = DatabaseConfig{
		Host:            getEnv("DB_HOST", "localhost"),
		Port:            getEnvInt("DB_PORT", 5432),
		User:            getEnv("DB_USER", "memoryos"),
		Password:        getEnv("DB_PASSWORD", ""),
		DBName:          getEnv("DB_NAME", "memoryos"),
		SSLMode:         getEnv("DB_SSLMODE", "require"),
		MaxOpenConns:    getEnvInt("DB_MAX_OPEN_CONNS", 25),
		MaxIdleConns:    getEnvInt("DB_MAX_IDLE_CONNS", 10),
		ConnMaxLifetime: getEnvDuration("DB_CONN_MAX_LIFETIME", 5*time.Minute),
	}

	cfg.Redis = RedisConfig{
		Host:       getEnv("REDIS_HOST", "localhost"),
		Port:       getEnvInt("REDIS_PORT", 6379),
		Password:   getEnv("REDIS_PASSWORD", ""),
		DB:         getEnvInt("REDIS_DB", 0),
		MaxRetries: getEnvInt("REDIS_MAX_RETRIES", 3),
		PoolSize:   getEnvInt("REDIS_POOL_SIZE", 20),
	}

	cfg.SQLite = SQLiteConfig{
		Path:          getEnv("SQLITE_PATH", "./data/sessions.db"),
		JournalMode:   getEnv("SQLITE_JOURNAL_MODE", "WAL"),
		CacheSize:     getEnvInt("SQLITE_CACHE_SIZE", -2000),
		BusyTimeoutMs: getEnvInt("SQLITE_BUSY_TIMEOUT", 5000),
	}

	cfg.Object = ObjectStorageConfig{
		Endpoint:  getEnv("OBJ_ENDPOINT", "localhost:9000"),
		AccessKey: getEnv("OBJ_ACCESS_KEY", ""),
		SecretKey: getEnv("OBJ_SECRET_KEY", ""),
		Bucket:    getEnv("OBJ_BUCKET", "memoryos-raw"),
		UseSSL:    getEnvBool("OBJ_USE_SSL", false),
	}

	cfg.Embedding = EmbeddingConfig{
		Provider:   getEnv("EMBEDDING_PROVIDER", "openai"),
		Model:      getEnv("EMBEDDING_MODEL", "text-embedding-3-small"),
		Dimensions: getEnvInt("EMBEDDING_DIMENSIONS", 1536),
		BatchSize:  getEnvInt("EMBEDDING_BATCH_SIZE", 32),
		APIKey:     getEnv("EMBEDDING_API_KEY", ""),
		BaseURL:    getEnv("EMBEDDING_BASE_URL", ""),
	}

	cfg.LLM = LLMConfig{
		Provider:  getEnv("LLM_PROVIDER", "openai"),
		Model:     getEnv("LLM_MODEL", "gpt-4o-mini"),
		APIKey:    getEnv("LLM_API_KEY", ""),
		BaseURL:   getEnv("LLM_BASE_URL", ""),
		MaxTokens: getEnvInt("LLM_MAX_TOKENS", 4096),
	}

	cfg.Worker = WorkerConfig{
		Concurrency:      getEnvInt("WORKER_CONCURRENCY", 4),
		QueueSize:        getEnvInt("WORKER_QUEUE_SIZE", 1000),
		PollInterval:     getEnvDuration("WORKER_POLL_INTERVAL", 100*time.Millisecond),
		TaskTimeout:      getEnvDuration("WORKER_TASK_TIMEOUT", 5*time.Minute),
		RetryMaxAttempts: getEnvInt("WORKER_RETRY_MAX", 3),
		RetryBackoff:     getEnvDuration("WORKER_RETRY_BACKOFF", time.Second),
	}

	cfg.Auth = AuthConfig{
		JWTSecret:        getEnv("AUTH_JWT_SECRET", ""),
		JWTExpiry:        getEnvDuration("AUTH_JWT_EXPIRY", 24*time.Hour),
		APIKeyHeader:     getEnv("AUTH_API_KEY_HEADER", "X-API-Key"),
		EnableAPIKeyAuth: getEnvBool("AUTH_ENABLE_API_KEY", true),
	}

	cfg.Security = SecurityConfig{
		RateLimitRPS:   getEnvFloat("SEC_RATE_LIMIT_RPS", 100),
		RateLimitBurst: getEnvInt("SEC_RATE_LIMIT_BURST", 200),
		MaxBodySize:    getEnvInt64("SEC_MAX_BODY_SIZE", 10*1024*1024),
	}

	cfg.Plugins = PluginsConfig{
		Dir:      getEnv("PLUGINS_DIR", "./plugins"),
		Enabled:  parseStringSlice(getEnv("PLUGINS_ENABLED", "")),
		Disabled: parseStringSlice(getEnv("PLUGINS_DISABLED", "")),
	}

	return cfg, nil
}

// MustLoad calls Load and panics on error.
func MustLoad() *Config {
	cfg, err := Load()
	if err != nil {
		panic(fmt.Sprintf("failed to load config: %v", err))
	}
	return cfg
}

// Helper functions

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	if val := os.Getenv(key); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			return n
		}
	}
	return defaultVal
}

func getEnvInt64(key string, defaultVal int64) int64 {
	if val := os.Getenv(key); val != "" {
		if n, err := strconv.ParseInt(val, 10, 64); err == nil {
			return n
		}
	}
	return defaultVal
}

func getEnvBool(key string, defaultVal bool) bool {
	if val := os.Getenv(key); val != "" {
		lower := strings.ToLower(val)
		return lower == "true" || lower == "1" || lower == "yes"
	}
	return defaultVal
}

func getEnvDuration(key string, defaultVal time.Duration) time.Duration {
	if val := os.Getenv(key); val != "" {
		if d, err := time.ParseDuration(val); err == nil {
			return d
		}
	}
	return defaultVal
}

func getEnvFloat(key string, defaultVal float64) float64 {
	if val := os.Getenv(key); val != "" {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return f
		}
	}
	return defaultVal
}

func parseStringSlice(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

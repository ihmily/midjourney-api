package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/viper"
	"github.com/trae/midjourney-api/internal/safehttp"
)

type Config struct {
	Server   ServerConfig   `mapstructure:"server"`
	Database DatabaseConfig `mapstructure:"database"`
	Redis    RedisConfig    `mapstructure:"redis"`
	Discord  DiscordConfig  `mapstructure:"discord"`
	Task     TaskConfig     `mapstructure:"task"`
	OSS      OSSConfig      `mapstructure:"oss"`
}

type ServerConfig struct {
	Port int    `mapstructure:"port"`
	Mode string `mapstructure:"mode"`
}

type DatabaseConfig struct {
	Host         string `mapstructure:"host"`
	Port         int    `mapstructure:"port"`
	User         string `mapstructure:"user"`
	Password     string `mapstructure:"password"`
	DBName       string `mapstructure:"dbname"`
	SSLMode      string `mapstructure:"sslmode"`
	MaxIdleConns int    `mapstructure:"max_idle_conns"`
	MaxOpenConns int    `mapstructure:"max_open_conns"`
	LogLevel     string `mapstructure:"log_level"` // SQL log level: silent, error, warn, info
}

type RedisConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
	PoolSize int    `mapstructure:"pool_size"`
}

type DiscordConfig struct {
	ApplicationID          string `mapstructure:"application_id"`
	ImagineCommandID       string `mapstructure:"imagine_command_id"`
	ImagineCommandVersion  string `mapstructure:"imagine_command_version"`
	DescribeCommandID      string `mapstructure:"describe_command_id"`
	DescribeCommandVersion string `mapstructure:"describe_command_version"`
	APIBaseURL             string `mapstructure:"api_base_url"`
}

type TaskConfig struct {
	Timeout     int    `mapstructure:"timeout"`
	MaxRetries  int    `mapstructure:"max_retries"`
	QueueName   string `mapstructure:"queue_name"`
	WorkerCount int    `mapstructure:"worker_count"`
}

type OSSConfig struct {
	Enable   bool            `mapstructure:"enable"`
	Provider string          `mapstructure:"provider"`
	S3       S3Config        `mapstructure:"s3"`
	Aliyun   AliyunOSSConfig `mapstructure:"aliyun"`
}

type S3Config struct {
	EndpointURL     string `mapstructure:"endpoint_url"`
	AccessKeyID     string `mapstructure:"access_key_id"`
	SecretAccessKey string `mapstructure:"secret_access_key"`
	Region          string `mapstructure:"region"`
	BucketName      string `mapstructure:"bucket_name"`
	Prefix          string `mapstructure:"prefix"`
}

type AliyunOSSConfig struct {
	AccessKeyID     string `mapstructure:"access_key_id"`
	AccessKeySecret string `mapstructure:"access_key_secret"`
	Endpoint        string `mapstructure:"endpoint"`
	BucketName      string `mapstructure:"bucket_name"`
	Prefix          string `mapstructure:"prefix"`
	ToSign          bool   `mapstructure:"to_sign"`
	SignExpires     int    `mapstructure:"sign_expires"`
	IsCname         bool   `mapstructure:"is_cname"`
	CnameDomain     string `mapstructure:"cname_domain"`
}

func Load(configPath string) (*Config, error) {
	v := viper.New()
	v.SetConfigFile(configPath)
	v.SetConfigType("yaml")

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}
	if err := rejectDeprecatedConfigKeys(v); err != nil {
		return nil, err
	}

	var cfg Config
	if err := v.UnmarshalExact(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	if err := applyEnvOverrides(&cfg); err != nil {
		return nil, fmt.Errorf("failed to apply environment overrides: %w", err)
	}

	cfg.Normalize()

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, nil
}

func rejectDeprecatedConfigKeys(v *viper.Viper) error {
	if v == nil {
		return fmt.Errorf("config reader is required")
	}
	if v.IsSet("discord.accounts") {
		return fmt.Errorf("discord.accounts is no longer supported; manage accounts through /api/v1/accounts and the database")
	}
	return nil
}

func applyEnvOverrides(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config is required")
	}

	applyStringEnv("MJ_SERVER_MODE", &cfg.Server.Mode)
	if err := applyIntEnv("MJ_SERVER_PORT", &cfg.Server.Port); err != nil {
		return err
	}

	applyStringEnv("MJ_DATABASE_HOST", &cfg.Database.Host)
	if err := applyIntEnv("MJ_DATABASE_PORT", &cfg.Database.Port); err != nil {
		return err
	}
	applyStringEnv("MJ_DATABASE_USER", &cfg.Database.User)
	applyStringEnv("MJ_DATABASE_PASSWORD", &cfg.Database.Password)
	applyStringEnv("MJ_DATABASE_DBNAME", &cfg.Database.DBName)
	applyStringEnv("MJ_DATABASE_SSLMODE", &cfg.Database.SSLMode)
	if err := applyIntEnv("MJ_DATABASE_MAX_IDLE_CONNS", &cfg.Database.MaxIdleConns); err != nil {
		return err
	}
	if err := applyIntEnv("MJ_DATABASE_MAX_OPEN_CONNS", &cfg.Database.MaxOpenConns); err != nil {
		return err
	}
	applyStringEnv("MJ_DATABASE_LOG_LEVEL", &cfg.Database.LogLevel)

	applyStringEnv("MJ_REDIS_HOST", &cfg.Redis.Host)
	if err := applyIntEnv("MJ_REDIS_PORT", &cfg.Redis.Port); err != nil {
		return err
	}
	applyStringEnv("MJ_REDIS_PASSWORD", &cfg.Redis.Password)
	if err := applyIntEnv("MJ_REDIS_DB", &cfg.Redis.DB); err != nil {
		return err
	}
	if err := applyIntEnv("MJ_REDIS_POOL_SIZE", &cfg.Redis.PoolSize); err != nil {
		return err
	}

	applyStringEnv("MJ_DISCORD_APPLICATION_ID", &cfg.Discord.ApplicationID)
	applyStringEnv("MJ_DISCORD_IMAGINE_COMMAND_ID", &cfg.Discord.ImagineCommandID)
	applyStringEnv("MJ_DISCORD_IMAGINE_COMMAND_VERSION", &cfg.Discord.ImagineCommandVersion)
	applyStringEnv("MJ_DISCORD_DESCRIBE_COMMAND_ID", &cfg.Discord.DescribeCommandID)
	applyStringEnv("MJ_DISCORD_DESCRIBE_COMMAND_VERSION", &cfg.Discord.DescribeCommandVersion)
	applyStringEnv("MJ_DISCORD_API_BASE_URL", &cfg.Discord.APIBaseURL)

	if err := applyIntEnv("MJ_TASK_TIMEOUT", &cfg.Task.Timeout); err != nil {
		return err
	}
	if err := applyIntEnv("MJ_TASK_MAX_RETRIES", &cfg.Task.MaxRetries); err != nil {
		return err
	}
	applyStringEnv("MJ_TASK_QUEUE_NAME", &cfg.Task.QueueName)
	if err := applyIntEnv("MJ_TASK_WORKER_COUNT", &cfg.Task.WorkerCount); err != nil {
		return err
	}

	if err := applyBoolEnv("MJ_OSS_ENABLE", &cfg.OSS.Enable); err != nil {
		return err
	}
	applyStringEnv("MJ_OSS_PROVIDER", &cfg.OSS.Provider)
	applyStringEnv("MJ_OSS_S3_ENDPOINT_URL", &cfg.OSS.S3.EndpointURL)
	applyStringEnv("MJ_OSS_S3_ACCESS_KEY_ID", &cfg.OSS.S3.AccessKeyID)
	applyStringEnv("MJ_OSS_S3_SECRET_ACCESS_KEY", &cfg.OSS.S3.SecretAccessKey)
	applyStringEnv("MJ_OSS_S3_REGION", &cfg.OSS.S3.Region)
	applyStringEnv("MJ_OSS_S3_BUCKET_NAME", &cfg.OSS.S3.BucketName)
	applyStringEnv("MJ_OSS_S3_PREFIX", &cfg.OSS.S3.Prefix)
	applyStringEnv("MJ_OSS_ALIYUN_ACCESS_KEY_ID", &cfg.OSS.Aliyun.AccessKeyID)
	applyStringEnv("MJ_OSS_ALIYUN_ACCESS_KEY_SECRET", &cfg.OSS.Aliyun.AccessKeySecret)
	applyStringEnv("MJ_OSS_ALIYUN_ENDPOINT", &cfg.OSS.Aliyun.Endpoint)
	applyStringEnv("MJ_OSS_ALIYUN_BUCKET_NAME", &cfg.OSS.Aliyun.BucketName)
	applyStringEnv("MJ_OSS_ALIYUN_PREFIX", &cfg.OSS.Aliyun.Prefix)
	if err := applyBoolEnv("MJ_OSS_ALIYUN_TO_SIGN", &cfg.OSS.Aliyun.ToSign); err != nil {
		return err
	}
	if err := applyIntEnv("MJ_OSS_ALIYUN_SIGN_EXPIRES", &cfg.OSS.Aliyun.SignExpires); err != nil {
		return err
	}
	if err := applyBoolEnv("MJ_OSS_ALIYUN_IS_CNAME", &cfg.OSS.Aliyun.IsCname); err != nil {
		return err
	}
	applyStringEnv("MJ_OSS_ALIYUN_CNAME_DOMAIN", &cfg.OSS.Aliyun.CnameDomain)

	return nil
}

func (c *Config) Normalize() {
	if c == nil {
		return
	}

	c.Server.Mode = strings.ToLower(strings.TrimSpace(c.Server.Mode))

	c.Database.Host = strings.TrimSpace(c.Database.Host)
	c.Database.User = strings.TrimSpace(c.Database.User)
	c.Database.DBName = strings.TrimSpace(c.Database.DBName)
	c.Database.SSLMode = strings.TrimSpace(c.Database.SSLMode)
	c.Database.LogLevel = strings.ToLower(strings.TrimSpace(c.Database.LogLevel))

	c.Redis.Host = strings.TrimSpace(c.Redis.Host)

	c.Discord.ApplicationID = strings.TrimSpace(c.Discord.ApplicationID)
	c.Discord.ImagineCommandID = strings.TrimSpace(c.Discord.ImagineCommandID)
	c.Discord.ImagineCommandVersion = strings.TrimSpace(c.Discord.ImagineCommandVersion)
	c.Discord.DescribeCommandID = strings.TrimSpace(c.Discord.DescribeCommandID)
	c.Discord.DescribeCommandVersion = strings.TrimSpace(c.Discord.DescribeCommandVersion)
	c.Discord.APIBaseURL = strings.TrimSpace(c.Discord.APIBaseURL)

	c.Task.QueueName = strings.TrimSpace(c.Task.QueueName)

	c.OSS.Provider = strings.ToLower(strings.TrimSpace(c.OSS.Provider))
	c.OSS.S3.EndpointURL = strings.TrimSpace(c.OSS.S3.EndpointURL)
	c.OSS.S3.AccessKeyID = strings.TrimSpace(c.OSS.S3.AccessKeyID)
	c.OSS.S3.Region = strings.TrimSpace(c.OSS.S3.Region)
	c.OSS.S3.BucketName = strings.TrimSpace(c.OSS.S3.BucketName)
	c.OSS.S3.Prefix = strings.TrimSpace(c.OSS.S3.Prefix)
	c.OSS.Aliyun.AccessKeyID = strings.TrimSpace(c.OSS.Aliyun.AccessKeyID)
	c.OSS.Aliyun.Endpoint = strings.TrimSpace(c.OSS.Aliyun.Endpoint)
	c.OSS.Aliyun.BucketName = strings.TrimSpace(c.OSS.Aliyun.BucketName)
	c.OSS.Aliyun.Prefix = strings.TrimSpace(c.OSS.Aliyun.Prefix)
	c.OSS.Aliyun.CnameDomain = strings.TrimSpace(c.OSS.Aliyun.CnameDomain)
}

func applyStringEnv(name string, target *string) {
	if value := os.Getenv(name); strings.TrimSpace(value) != "" {
		*target = value
	}
}

func applyIntEnv(name string, target *int) error {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("%s must be an integer", name)
	}
	*target = parsed
	return nil
}

func applyBoolEnv(name string, target *bool) error {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fmt.Errorf("%s must be a boolean", name)
	}
	*target = parsed
	return nil
}

func (c *Config) Validate() error {
	if c.Server.Port <= 0 || c.Server.Port > 65535 {
		return fmt.Errorf("invalid server port: %d", c.Server.Port)
	}
	if c.Server.Mode != "debug" && c.Server.Mode != "release" && c.Server.Mode != "test" {
		return fmt.Errorf("invalid server mode: %s (must be debug/release/test)", c.Server.Mode)
	}

	if strings.TrimSpace(c.Database.Host) == "" {
		return fmt.Errorf("database host is required")
	}
	if c.Database.Port <= 0 || c.Database.Port > 65535 {
		return fmt.Errorf("invalid database port: %d", c.Database.Port)
	}
	if strings.TrimSpace(c.Database.User) == "" {
		return fmt.Errorf("database user is required")
	}
	if strings.TrimSpace(c.Database.DBName) == "" {
		return fmt.Errorf("database name is required")
	}
	if c.Database.MaxIdleConns < 0 {
		return fmt.Errorf("database max_idle_conns must be >= 0")
	}
	if c.Database.MaxOpenConns < 0 {
		return fmt.Errorf("database max_open_conns must be >= 0")
	}
	if c.Database.MaxOpenConns > 0 && c.Database.MaxIdleConns > c.Database.MaxOpenConns {
		return fmt.Errorf("database max_idle_conns must be <= max_open_conns")
	}
	if c.Database.LogLevel != "" &&
		c.Database.LogLevel != "silent" &&
		c.Database.LogLevel != "error" &&
		c.Database.LogLevel != "warn" &&
		c.Database.LogLevel != "info" {
		return fmt.Errorf("invalid database log_level: %s (must be silent/error/warn/info)", c.Database.LogLevel)
	}

	if err := c.Redis.Validate(); err != nil {
		return err
	}

	if strings.TrimSpace(c.Discord.ApplicationID) == "" {
		return fmt.Errorf("discord application_id is required")
	}
	if strings.TrimSpace(c.Discord.ImagineCommandID) == "" {
		return fmt.Errorf("discord imagine_command_id is required")
	}
	if strings.TrimSpace(c.Discord.ImagineCommandVersion) == "" {
		return fmt.Errorf("discord imagine_command_version is required")
	}
	if strings.TrimSpace(c.Discord.DescribeCommandID) == "" {
		return fmt.Errorf("discord describe_command_id is required")
	}
	if strings.TrimSpace(c.Discord.DescribeCommandVersion) == "" {
		return fmt.Errorf("discord describe_command_version is required")
	}
	if strings.TrimSpace(c.Discord.APIBaseURL) == "" {
		return fmt.Errorf("discord api_base_url is required")
	}
	if err := validateHTTPURL("discord api_base_url", c.Discord.APIBaseURL); err != nil {
		return err
	}
	if strings.TrimSpace(c.Task.QueueName) == "" {
		return fmt.Errorf("task queue_name is required")
	}
	if c.Task.Timeout <= 0 {
		return fmt.Errorf("task timeout must be > 0")
	}
	if c.Task.MaxRetries < 0 {
		return fmt.Errorf("task max_retries must be >= 0")
	}
	if c.Task.WorkerCount < 0 {
		return fmt.Errorf("task worker_count must be >= 0")
	}

	if err := c.validateOSS(); err != nil {
		return err
	}

	return nil
}

func (c RedisConfig) Validate() error {
	if _, err := c.NormalizedHost(); err != nil {
		return err
	}
	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("invalid redis port: %d", c.Port)
	}
	if c.DB < 0 {
		return fmt.Errorf("redis db must be >= 0")
	}
	if c.PoolSize <= 0 {
		return fmt.Errorf("redis pool_size must be > 0")
	}
	return nil
}

func (c RedisConfig) NormalizedHost() (string, error) {
	return NormalizeRedisHost(c.Host)
}

func NormalizeRedisHost(host string) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", fmt.Errorf("redis host is required")
	}
	if invalidRedisHostSyntax(host) {
		return "", fmt.Errorf("redis host must be a hostname or IP address, not a URL")
	}
	if strings.HasPrefix(host, "[") || strings.HasSuffix(host, "]") {
		if !strings.HasPrefix(host, "[") || !strings.HasSuffix(host, "]") {
			return "", fmt.Errorf("redis host must be a hostname or IP address")
		}
		inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"))
		if net.ParseIP(inner) == nil {
			return "", fmt.Errorf("redis host must be a hostname or IP address")
		}
		return inner, nil
	}
	if strings.Contains(host, ":") && net.ParseIP(host) == nil {
		return "", fmt.Errorf("redis host must not include a port; configure redis.port separately")
	}
	return host, nil
}

func invalidRedisHostSyntax(host string) bool {
	return strings.Contains(host, "://") || strings.ContainsAny(host, "/?#@")
}

func (c *Config) validateOSS() error {
	if !c.OSS.Enable {
		return nil
	}

	provider := strings.ToLower(strings.TrimSpace(c.OSS.Provider))
	switch provider {
	case "s3":
		return validateS3Config(c.OSS.S3)
	case "aliyun":
		return validateAliyunOSSConfig(c.OSS.Aliyun)
	case "":
		return fmt.Errorf("oss provider is required when oss is enabled")
	default:
		return fmt.Errorf("unsupported oss provider: %s (supported: s3, aliyun)", c.OSS.Provider)
	}
}

func validateS3Config(cfg S3Config) error {
	if err := requireConfigValue("oss.s3.region", cfg.Region); err != nil {
		return err
	}
	if err := requireConfigValue("oss.s3.bucket_name", cfg.BucketName); err != nil {
		return err
	}
	if err := requireConfigValue("oss.s3.access_key_id", cfg.AccessKeyID); err != nil {
		return err
	}
	if err := requireConfigValue("oss.s3.secret_access_key", cfg.SecretAccessKey); err != nil {
		return err
	}
	if cfg.EndpointURL != "" {
		if err := validateHTTPURL("oss.s3.endpoint_url", cfg.EndpointURL); err != nil {
			return err
		}
	}
	return nil
}

func validateAliyunOSSConfig(cfg AliyunOSSConfig) error {
	if err := requireConfigValue("oss.aliyun.endpoint", cfg.Endpoint); err != nil {
		return err
	}
	if err := validateHTTPURL("oss.aliyun.endpoint", cfg.Endpoint); err != nil {
		return err
	}
	if err := requireConfigValue("oss.aliyun.bucket_name", cfg.BucketName); err != nil {
		return err
	}
	if err := requireConfigValue("oss.aliyun.access_key_id", cfg.AccessKeyID); err != nil {
		return err
	}
	if err := requireConfigValue("oss.aliyun.access_key_secret", cfg.AccessKeySecret); err != nil {
		return err
	}
	if cfg.ToSign && cfg.SignExpires <= 0 {
		return fmt.Errorf("oss.aliyun.sign_expires must be > 0 when to_sign is enabled")
	}
	if cfg.IsCname {
		if err := requireConfigValue("oss.aliyun.cname_domain", cfg.CnameDomain); err != nil {
			return err
		}
		if err := validateHTTPURL("oss.aliyun.cname_domain", cfg.CnameDomain); err != nil {
			return err
		}
	}
	return nil
}

func requireConfigValue(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	return nil
}

func validateHTTPURL(name, value string) error {
	if err := safehttp.ValidateHTTPURL(value, name); err != nil {
		return err
	}
	parsed, _ := url.Parse(strings.TrimSpace(value))
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%s must not contain userinfo, query, or fragment", name)
	}
	return nil
}

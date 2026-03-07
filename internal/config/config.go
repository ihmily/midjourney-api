package config

import (
	"fmt"
	"os"

	"github.com/spf13/viper"
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

type DiscordAccount struct {
	Name      string `mapstructure:"name"`
	BotToken  string `mapstructure:"bot_token"`
	UserToken string `mapstructure:"user_token"`
	GuildID   string `mapstructure:"guild_id"`
	ChannelID string `mapstructure:"channel_id"`
}

type DiscordConfig struct {
	ApplicationID         string           `mapstructure:"application_id"`
	ImagineCommandID      string           `mapstructure:"imagine_command_id"`
	ImagineCommandVersion string           `mapstructure:"imagine_command_version"`
	APIBaseURL            string           `mapstructure:"api_base_url"`
	Accounts              []DiscordAccount `mapstructure:"accounts"`
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
	UseSSL          bool   `mapstructure:"use_ssl"`
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
	viper.SetConfigFile(configPath)
	viper.SetConfigType("yaml")

	viper.AutomaticEnv()
	viper.SetEnvPrefix("MJ") //  MJ_ prefix for environment variables

	if err := viper.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	if dbHost := os.Getenv("MJ_DATABASE_HOST"); dbHost != "" {
		cfg.Database.Host = dbHost
	}
	if dbUser := os.Getenv("MJ_DATABASE_USER"); dbUser != "" {
		cfg.Database.User = dbUser
	}
	if dbPassword := os.Getenv("MJ_DATABASE_PASSWORD"); dbPassword != "" {
		cfg.Database.Password = dbPassword
	}
	if redisHost := os.Getenv("MJ_REDIS_HOST"); redisHost != "" {
		cfg.Redis.Host = redisHost
	}
	if redisPassword := os.Getenv("MJ_REDIS_PASSWORD"); redisPassword != "" {
		cfg.Redis.Password = redisPassword
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, nil
}

func (c *Config) Validate() error {
	if c.Server.Port <= 0 || c.Server.Port > 65535 {
		return fmt.Errorf("invalid server port: %d", c.Server.Port)
	}
	if c.Server.Mode != "debug" && c.Server.Mode != "release" && c.Server.Mode != "test" {
		return fmt.Errorf("invalid server mode: %s (must be debug/release/test)", c.Server.Mode)
	}

	if c.Database.Host == "" {
		return fmt.Errorf("database host is required")
	}
	if c.Database.User == "" {
		return fmt.Errorf("database user is required")
	}
	if c.Database.DBName == "" {
		return fmt.Errorf("database name is required")
	}

	if c.Redis.Host == "" {
		return fmt.Errorf("redis host is required")
	}

	if c.Discord.ApplicationID == "" {
		return fmt.Errorf("discord application_id is required")
	}
	if c.Discord.ImagineCommandID == "" {
		return fmt.Errorf("discord imagine_command_id is required")
	}
	if len(c.Discord.Accounts) == 0 {
		return fmt.Errorf("at least one discord account is required")
	}

	for i, acc := range c.Discord.Accounts {
		if acc.GuildID == "" {
			return fmt.Errorf("discord account[%d] guild_id is required", i)
		}
		if acc.ChannelID == "" {
			return fmt.Errorf("discord account[%d] channel_id is required", i)
		}
		if acc.UserToken == "" {
			return fmt.Errorf("discord account[%d] user_token is required", i)
		}
	}

	if c.Task.QueueName == "" {
		return fmt.Errorf("task queue_name is required")
	}
	if c.Task.WorkerCount < 0 {
		return fmt.Errorf("task worker_count must be >= 0")
	}

	return nil
}

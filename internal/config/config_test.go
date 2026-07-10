package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateAcceptsValidConfig(t *testing.T) {
	cfg := validConfig()

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
}

func TestValidateHTTPURLAcceptsCaseInsensitiveScheme(t *testing.T) {
	for _, value := range []string{
		"HTTP://example.com/api",
		"HTTPS://example.com/api",
	} {
		if err := validateHTTPURL("test_url", value); err != nil {
			t.Fatalf("validateHTTPURL(%q) returned error: %v", value, err)
		}
	}
}

func TestExampleConfigLoadsAndDoesNotContainAccounts(t *testing.T) {
	clearConfigEnv(t)

	path := filepath.Join("..", "..", "config", "config.yaml.example")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read example config: %v", err)
	}
	if strings.Contains(string(content), "accounts:") {
		t.Fatalf("example config must not contain discord.accounts")
	}

	if _, err := Load(path); err != nil {
		t.Fatalf("Load(example config) returned error: %v", err)
	}
}

func TestLoadRejectsDeprecatedDiscordAccounts(t *testing.T) {
	clearConfigEnv(t)
	path := writeConfigFile(t, strings.Replace(validConfigYAML(),
		`  api_base_url: "https://discord.com/api/v9"`,
		`  api_base_url: "https://discord.com/api/v9"
  accounts:
    - guild_id: "guild-1"
      channel_id: "channel-1"
      user_token: "secret-token"`,
		1,
	))

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load returned nil error, want deprecated accounts error")
	}
	if !strings.Contains(err.Error(), "discord.accounts") ||
		!strings.Contains(err.Error(), "/api/v1/accounts") {
		t.Fatalf("error = %q, want deprecated accounts guidance", err.Error())
	}
}

func TestLoadRejectsUnknownConfigKeys(t *testing.T) {
	tests := []struct {
		name        string
		replacer    string
		replacement string
		wantErr     string
	}{
		{
			name:     "unknown top level key",
			replacer: "oss:\n",
			replacement: `unexpected:
  enabled: true

oss:
`,
			wantErr: "unexpected",
		},
		{
			name:     "misspelled nested key",
			replacer: "  port: 8080",
			replacement: `  port: 8080
  ports: 9090`,
			wantErr: "ports",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearConfigEnv(t)
			path := writeConfigFile(t, strings.Replace(validConfigYAML(), tt.replacer, tt.replacement, 1))

			_, err := Load(path)
			if err == nil {
				t.Fatal("Load returned nil error, want unknown key error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestLoadAppliesEnvironmentOverrides(t *testing.T) {
	clearConfigEnv(t)
	path := writeConfigFile(t, validConfigYAML())

	t.Setenv("MJ_SERVER_PORT", "9090")
	t.Setenv("MJ_SERVER_MODE", " release ")
	t.Setenv("MJ_DATABASE_HOST", " db.internal ")
	t.Setenv("MJ_DATABASE_PORT", "15432")
	t.Setenv("MJ_DATABASE_USER", " mj_env ")
	t.Setenv("MJ_DATABASE_PASSWORD", " env-password ")
	t.Setenv("MJ_DATABASE_DBNAME", " midjourney_env ")
	t.Setenv("MJ_DATABASE_SSLMODE", " require ")
	t.Setenv("MJ_DATABASE_MAX_IDLE_CONNS", "3")
	t.Setenv("MJ_DATABASE_MAX_OPEN_CONNS", "9")
	t.Setenv("MJ_DATABASE_LOG_LEVEL", " WARN ")
	t.Setenv("MJ_REDIS_HOST", " redis.internal ")
	t.Setenv("MJ_REDIS_PORT", "16379")
	t.Setenv("MJ_REDIS_PASSWORD", "redis-secret")
	t.Setenv("MJ_REDIS_DB", "2")
	t.Setenv("MJ_REDIS_POOL_SIZE", "12")
	t.Setenv("MJ_DISCORD_APPLICATION_ID", " app-env ")
	t.Setenv("MJ_DISCORD_IMAGINE_COMMAND_ID", "imagine-env")
	t.Setenv("MJ_DISCORD_IMAGINE_COMMAND_VERSION", "imagine-version-env")
	t.Setenv("MJ_DISCORD_DESCRIBE_COMMAND_ID", "describe-env")
	t.Setenv("MJ_DISCORD_DESCRIBE_COMMAND_VERSION", "describe-version-env")
	t.Setenv("MJ_DISCORD_API_BASE_URL", " https://discord.example.com/api ")
	t.Setenv("MJ_TASK_TIMEOUT", "600")
	t.Setenv("MJ_TASK_MAX_RETRIES", "5")
	t.Setenv("MJ_TASK_QUEUE_NAME", " mj:env:queue ")
	t.Setenv("MJ_TASK_WORKER_COUNT", "7")
	t.Setenv("MJ_OSS_ENABLE", "true")
	t.Setenv("MJ_OSS_PROVIDER", " S3 ")
	t.Setenv("MJ_OSS_S3_ENDPOINT_URL", " https://s3.example.com ")
	t.Setenv("MJ_OSS_S3_ACCESS_KEY_ID", " s3-key ")
	t.Setenv("MJ_OSS_S3_SECRET_ACCESS_KEY", "s3-secret")
	t.Setenv("MJ_OSS_S3_REGION", " us-east-1 ")
	t.Setenv("MJ_OSS_S3_BUCKET_NAME", " bucket-env ")
	t.Setenv("MJ_OSS_S3_PREFIX", " prefix-env ")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Server.Port != 9090 || cfg.Server.Mode != "release" {
		t.Fatalf("server override = %#v, want env values", cfg.Server)
	}
	if cfg.Database.Host != "db.internal" ||
		cfg.Database.Port != 15432 ||
		cfg.Database.User != "mj_env" ||
		cfg.Database.Password != " env-password " ||
		cfg.Database.DBName != "midjourney_env" ||
		cfg.Database.SSLMode != "require" ||
		cfg.Database.MaxIdleConns != 3 ||
		cfg.Database.MaxOpenConns != 9 ||
		cfg.Database.LogLevel != "warn" {
		t.Fatalf("database override = %#v, want env values", cfg.Database)
	}
	if cfg.Redis.Host != "redis.internal" ||
		cfg.Redis.Port != 16379 ||
		cfg.Redis.Password != "redis-secret" ||
		cfg.Redis.DB != 2 ||
		cfg.Redis.PoolSize != 12 {
		t.Fatalf("redis override = %#v, want env values", cfg.Redis)
	}
	if cfg.Discord.ApplicationID != "app-env" ||
		cfg.Discord.ImagineCommandID != "imagine-env" ||
		cfg.Discord.ImagineCommandVersion != "imagine-version-env" ||
		cfg.Discord.DescribeCommandID != "describe-env" ||
		cfg.Discord.DescribeCommandVersion != "describe-version-env" ||
		cfg.Discord.APIBaseURL != "https://discord.example.com/api" {
		t.Fatalf("discord override = %#v, want env values", cfg.Discord)
	}
	if cfg.Task.Timeout != 600 ||
		cfg.Task.MaxRetries != 5 ||
		cfg.Task.QueueName != "mj:env:queue" ||
		cfg.Task.WorkerCount != 7 {
		t.Fatalf("task override = %#v, want env values", cfg.Task)
	}
	if !cfg.OSS.Enable ||
		cfg.OSS.Provider != "s3" ||
		cfg.OSS.S3.EndpointURL != "https://s3.example.com" ||
		cfg.OSS.S3.AccessKeyID != "s3-key" ||
		cfg.OSS.S3.SecretAccessKey != "s3-secret" ||
		cfg.OSS.S3.Region != "us-east-1" ||
		cfg.OSS.S3.BucketName != "bucket-env" ||
		cfg.OSS.S3.Prefix != "prefix-env" {
		t.Fatalf("oss override = %#v, want env values", cfg.OSS)
	}
}

func TestNormalizeTrimsRuntimeFieldsAndPreservesSecrets(t *testing.T) {
	cfg := validConfig()
	cfg.Server.Mode = " TEST "
	cfg.Database.Host = " db.internal "
	cfg.Database.User = " mj_user "
	cfg.Database.Password = " db password with spaces "
	cfg.Database.DBName = " midjourney "
	cfg.Database.SSLMode = " disable "
	cfg.Database.LogLevel = " WARN "
	cfg.Redis.Host = " redis.internal "
	cfg.Redis.Password = " redis password with spaces "
	cfg.Discord.ApplicationID = " app-id "
	cfg.Discord.APIBaseURL = " https://discord.example.com/api "
	cfg.Task.QueueName = " mj:queue "
	cfg.OSS = validS3OSSConfig()
	cfg.OSS.Provider = " S3 "
	cfg.OSS.S3.EndpointURL = " https://s3.example.com "
	cfg.OSS.S3.AccessKeyID = " s3-key "
	cfg.OSS.S3.SecretAccessKey = " s3 secret with spaces "
	cfg.OSS.S3.Region = " us-east-1 "
	cfg.OSS.S3.BucketName = " bucket "
	cfg.OSS.S3.Prefix = " prefix "

	cfg.Normalize()

	if cfg.Server.Mode != "test" ||
		cfg.Database.Host != "db.internal" ||
		cfg.Database.User != "mj_user" ||
		cfg.Database.DBName != "midjourney" ||
		cfg.Database.SSLMode != "disable" ||
		cfg.Database.LogLevel != "warn" ||
		cfg.Redis.Host != "redis.internal" ||
		cfg.Discord.ApplicationID != "app-id" ||
		cfg.Discord.APIBaseURL != "https://discord.example.com/api" ||
		cfg.Task.QueueName != "mj:queue" ||
		cfg.OSS.Provider != "s3" ||
		cfg.OSS.S3.EndpointURL != "https://s3.example.com" ||
		cfg.OSS.S3.AccessKeyID != "s3-key" ||
		cfg.OSS.S3.Region != "us-east-1" ||
		cfg.OSS.S3.BucketName != "bucket" ||
		cfg.OSS.S3.Prefix != "prefix" {
		t.Fatalf("Normalize did not trim runtime fields: %#v", cfg)
	}
	if cfg.Database.Password != " db password with spaces " ||
		cfg.Redis.Password != " redis password with spaces " ||
		cfg.OSS.S3.SecretAccessKey != " s3 secret with spaces " {
		t.Fatalf("Normalize should preserve secret field bytes")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("normalized config should validate: %v", err)
	}
}

func TestLoadRejectsInvalidEnvironmentOverride(t *testing.T) {
	clearConfigEnv(t)
	path := writeConfigFile(t, validConfigYAML())
	t.Setenv("MJ_TASK_WORKER_COUNT", "not-an-int")

	_, err := Load(path)

	if err == nil {
		t.Fatal("Load returned nil error, want invalid env error")
	}
	if !strings.Contains(err.Error(), "MJ_TASK_WORKER_COUNT") {
		t.Fatalf("error = %q, want env var name", err.Error())
	}
}

func TestLoadRejectsInvalidRedisHostOverrideWithoutLeakingSecrets(t *testing.T) {
	clearConfigEnv(t)
	path := writeConfigFile(t, validConfigYAML())
	t.Setenv("MJ_REDIS_HOST", "redis://:secret-pass@localhost?token=secret#fragment")

	_, err := Load(path)

	if err == nil {
		t.Fatal("Load returned nil error, want invalid redis host")
	}
	if !strings.Contains(err.Error(), "redis host") {
		t.Fatalf("error = %q, want redis host context", err.Error())
	}
	for _, forbidden := range []string{"secret-pass", "token=secret", "#fragment"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("error exposed %q: %s", forbidden, err.Error())
		}
	}
}

func TestNormalizeRedisHost(t *testing.T) {
	tests := []struct {
		name string
		host string
		want string
	}{
		{
			name: "hostname",
			host: " redis.internal ",
			want: "redis.internal",
		},
		{
			name: "raw ipv6",
			host: " ::1 ",
			want: "::1",
		},
		{
			name: "bracketed ipv6",
			host: " [::1] ",
			want: "::1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeRedisHost(tt.host)
			if err != nil {
				t.Fatalf("NormalizeRedisHost returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeRedisHost = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateAcceptsEnabledOSSProviders(t *testing.T) {
	tests := []struct {
		name string
		oss  OSSConfig
	}{
		{
			name: "s3",
			oss:  validS3OSSConfig(),
		},
		{
			name: "s3 uppercase provider",
			oss: func() OSSConfig {
				oss := validS3OSSConfig()
				oss.Provider = "S3"
				return oss
			}(),
		},
		{
			name: "aliyun",
			oss:  validAliyunOSSConfig(),
		},
		{
			name: "aliyun cname",
			oss: func() OSSConfig {
				oss := validAliyunOSSConfig()
				oss.Aliyun.IsCname = true
				oss.Aliyun.CnameDomain = "https://cdn.example.com"
				return oss
			}(),
		},
		{
			name: "aliyun signed urls",
			oss: func() OSSConfig {
				oss := validAliyunOSSConfig()
				oss.Aliyun.ToSign = true
				oss.Aliyun.SignExpires = 3600
				return oss
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.OSS = tt.oss

			if err := cfg.Validate(); err != nil {
				t.Fatalf("Validate returned error: %v", err)
			}
		})
	}
}

func TestValidateRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name: "missing describe command id",
			mutate: func(c *Config) {
				c.Discord.DescribeCommandID = ""
			},
			wantErr: "describe_command_id",
		},
		{
			name: "missing describe command version",
			mutate: func(c *Config) {
				c.Discord.DescribeCommandVersion = ""
			},
			wantErr: "describe_command_version",
		},
		{
			name: "blank database host",
			mutate: func(c *Config) {
				c.Database.Host = "   "
			},
			wantErr: "database host",
		},
		{
			name: "blank redis host",
			mutate: func(c *Config) {
				c.Redis.Host = "   "
			},
			wantErr: "redis host",
		},
		{
			name: "blank discord application id",
			mutate: func(c *Config) {
				c.Discord.ApplicationID = "   "
			},
			wantErr: "application_id",
		},
		{
			name: "blank task queue name",
			mutate: func(c *Config) {
				c.Task.QueueName = "   "
			},
			wantErr: "queue_name",
		},
		{
			name: "invalid discord api base url",
			mutate: func(c *Config) {
				c.Discord.APIBaseURL = "discord.com/api/v9"
			},
			wantErr: "discord api_base_url",
		},
		{
			name: "discord api base url with query",
			mutate: func(c *Config) {
				c.Discord.APIBaseURL = "https://discord.com/api/v9?token=secret"
			},
			wantErr: "userinfo, query, or fragment",
		},
		{
			name: "negative redis db",
			mutate: func(c *Config) {
				c.Redis.DB = -1
			},
			wantErr: "redis db",
		},
		{
			name: "redis url host",
			mutate: func(c *Config) {
				c.Redis.Host = "redis://:secret-pass@localhost"
			},
			wantErr: "not a URL",
		},
		{
			name: "redis host with port",
			mutate: func(c *Config) {
				c.Redis.Host = "localhost:6379"
			},
			wantErr: "must not include a port",
		},
		{
			name: "redis host with query",
			mutate: func(c *Config) {
				c.Redis.Host = "localhost?token=secret"
			},
			wantErr: "not a URL",
		},
		{
			name: "invalid redis pool size",
			mutate: func(c *Config) {
				c.Redis.PoolSize = 0
			},
			wantErr: "pool_size",
		},
		{
			name: "idle conns greater than open conns",
			mutate: func(c *Config) {
				c.Database.MaxIdleConns = 11
				c.Database.MaxOpenConns = 10
			},
			wantErr: "max_idle_conns",
		},
		{
			name: "invalid database log level",
			mutate: func(c *Config) {
				c.Database.LogLevel = "verbose"
			},
			wantErr: "log_level",
		},
		{
			name: "enabled oss missing provider",
			mutate: func(c *Config) {
				c.OSS.Enable = true
				c.OSS.Provider = ""
			},
			wantErr: "oss provider",
		},
		{
			name: "enabled oss unsupported provider",
			mutate: func(c *Config) {
				c.OSS.Enable = true
				c.OSS.Provider = "minio"
			},
			wantErr: "unsupported oss provider",
		},
		{
			name: "s3 missing bucket",
			mutate: func(c *Config) {
				c.OSS = validS3OSSConfig()
				c.OSS.S3.BucketName = ""
			},
			wantErr: "oss.s3.bucket_name",
		},
		{
			name: "s3 invalid endpoint url",
			mutate: func(c *Config) {
				c.OSS = validS3OSSConfig()
				c.OSS.S3.EndpointURL = "s3.example.com"
			},
			wantErr: "oss.s3.endpoint_url",
		},
		{
			name: "aliyun missing endpoint",
			mutate: func(c *Config) {
				c.OSS = validAliyunOSSConfig()
				c.OSS.Aliyun.Endpoint = ""
			},
			wantErr: "oss.aliyun.endpoint",
		},
		{
			name: "aliyun invalid endpoint",
			mutate: func(c *Config) {
				c.OSS = validAliyunOSSConfig()
				c.OSS.Aliyun.Endpoint = "oss-cn-hangzhou.aliyuncs.com"
			},
			wantErr: "oss.aliyun.endpoint",
		},
		{
			name: "aliyun sign expiration required",
			mutate: func(c *Config) {
				c.OSS = validAliyunOSSConfig()
				c.OSS.Aliyun.ToSign = true
				c.OSS.Aliyun.SignExpires = 0
			},
			wantErr: "sign_expires",
		},
		{
			name: "aliyun cname domain required",
			mutate: func(c *Config) {
				c.OSS = validAliyunOSSConfig()
				c.OSS.Aliyun.IsCname = true
				c.OSS.Aliyun.CnameDomain = ""
			},
			wantErr: "cname_domain",
		},
		{
			name: "aliyun cname domain must be url",
			mutate: func(c *Config) {
				c.OSS = validAliyunOSSConfig()
				c.OSS.Aliyun.IsCname = true
				c.OSS.Aliyun.CnameDomain = "cdn.example.com"
			},
			wantErr: "cname_domain",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.mutate(cfg)

			err := cfg.Validate()
			if err == nil {
				t.Fatalf("Validate returned nil, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestValidateSkipsOSSProviderConfigWhenDisabled(t *testing.T) {
	cfg := validConfig()
	cfg.OSS = OSSConfig{
		Enable:   false,
		Provider: "unsupported",
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
}

func validConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Port: 8080,
			Mode: "test",
		},
		Database: DatabaseConfig{
			Host:         "localhost",
			Port:         5432,
			User:         "mj_admin",
			DBName:       "midjourney",
			SSLMode:      "disable",
			MaxIdleConns: 10,
			MaxOpenConns: 100,
			LogLevel:     "silent",
		},
		Redis: RedisConfig{
			Host:     "localhost",
			Port:     6379,
			DB:       0,
			PoolSize: 10,
		},
		Discord: DiscordConfig{
			ApplicationID:          "936929561302675456",
			ImagineCommandID:       "938956540159881230",
			ImagineCommandVersion:  "1237876415471554623",
			DescribeCommandID:      "1092492867185950852",
			DescribeCommandVersion: "1493662068505706617",
			APIBaseURL:             "https://discord.com/api/v9",
		},
		Task: TaskConfig{
			Timeout:     300,
			MaxRetries:  3,
			QueueName:   "mj:task:queue",
			WorkerCount: 3,
		},
	}
}

func validS3OSSConfig() OSSConfig {
	return OSSConfig{
		Enable:   true,
		Provider: "s3",
		S3: S3Config{
			EndpointURL:     "https://s3.ap-southeast-1.amazonaws.com",
			AccessKeyID:     "access-key-id",
			SecretAccessKey: "secret-access-key",
			Region:          "ap-southeast-1",
			BucketName:      "midjourney-api",
			Prefix:          "midjourney",
		},
	}
}

func validAliyunOSSConfig() OSSConfig {
	return OSSConfig{
		Enable:   true,
		Provider: "aliyun",
		Aliyun: AliyunOSSConfig{
			AccessKeyID:     "access-key-id",
			AccessKeySecret: "access-key-secret",
			Endpoint:        "https://oss-cn-hangzhou.aliyuncs.com",
			BucketName:      "midjourney-api",
			Prefix:          "midjourney",
			SignExpires:     3600,
		},
	}
}

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}
	return path
}

func validConfigYAML() string {
	return `
server:
  port: 8080
  mode: test

database:
  host: localhost
  port: 5432
  user: mj_admin
  password: ""
  dbname: midjourney
  sslmode: disable
  max_idle_conns: 10
  max_open_conns: 100
  log_level: silent

redis:
  host: localhost
  port: 6379
  password: ""
  db: 0
  pool_size: 10

discord:
  application_id: "936929561302675456"
  imagine_command_id: "938956540159881230"
  imagine_command_version: "1237876415471554623"
  describe_command_id: "1092492867185950852"
  describe_command_version: "1493662068505706617"
  api_base_url: "https://discord.com/api/v9"

task:
  timeout: 300
  max_retries: 3
  queue_name: "mj:task:queue"
  worker_count: 3

oss:
  enable: false
  provider: ""
`
}

func clearConfigEnv(t *testing.T) {
	t.Helper()

	for _, name := range []string{
		"MJ_SERVER_MODE",
		"MJ_SERVER_PORT",
		"MJ_DATABASE_HOST",
		"MJ_DATABASE_PORT",
		"MJ_DATABASE_USER",
		"MJ_DATABASE_PASSWORD",
		"MJ_DATABASE_DBNAME",
		"MJ_DATABASE_SSLMODE",
		"MJ_DATABASE_MAX_IDLE_CONNS",
		"MJ_DATABASE_MAX_OPEN_CONNS",
		"MJ_DATABASE_LOG_LEVEL",
		"MJ_REDIS_HOST",
		"MJ_REDIS_PORT",
		"MJ_REDIS_PASSWORD",
		"MJ_REDIS_DB",
		"MJ_REDIS_POOL_SIZE",
		"MJ_DISCORD_APPLICATION_ID",
		"MJ_DISCORD_IMAGINE_COMMAND_ID",
		"MJ_DISCORD_IMAGINE_COMMAND_VERSION",
		"MJ_DISCORD_DESCRIBE_COMMAND_ID",
		"MJ_DISCORD_DESCRIBE_COMMAND_VERSION",
		"MJ_DISCORD_API_BASE_URL",
		"MJ_TASK_TIMEOUT",
		"MJ_TASK_MAX_RETRIES",
		"MJ_TASK_QUEUE_NAME",
		"MJ_TASK_WORKER_COUNT",
		"MJ_OSS_ENABLE",
		"MJ_OSS_PROVIDER",
		"MJ_OSS_S3_ENDPOINT_URL",
		"MJ_OSS_S3_ACCESS_KEY_ID",
		"MJ_OSS_S3_SECRET_ACCESS_KEY",
		"MJ_OSS_S3_REGION",
		"MJ_OSS_S3_BUCKET_NAME",
		"MJ_OSS_S3_PREFIX",
		"MJ_OSS_ALIYUN_ACCESS_KEY_ID",
		"MJ_OSS_ALIYUN_ACCESS_KEY_SECRET",
		"MJ_OSS_ALIYUN_ENDPOINT",
		"MJ_OSS_ALIYUN_BUCKET_NAME",
		"MJ_OSS_ALIYUN_PREFIX",
		"MJ_OSS_ALIYUN_TO_SIGN",
		"MJ_OSS_ALIYUN_SIGN_EXPIRES",
		"MJ_OSS_ALIYUN_IS_CNAME",
		"MJ_OSS_ALIYUN_CNAME_DOMAIN",
	} {
		t.Setenv(name, "")
	}
}

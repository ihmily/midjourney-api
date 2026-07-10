package redis

import (
	"errors"
	"strings"
	"testing"

	"github.com/trae/midjourney-api/internal/config"
)

func TestInitRejectsNilConfig(t *testing.T) {
	client, err := Init(nil)

	if client != nil {
		t.Fatalf("client = %#v, want nil", client)
	}
	if err == nil {
		t.Fatal("Init returned nil error, want error")
	}
	if !strings.Contains(err.Error(), "redis config") {
		t.Fatalf("error = %q, want redis config context", err.Error())
	}
}

func TestValidateRedisConfigRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.RedisConfig
		want string
	}{
		{
			name: "blank host",
			cfg: &config.RedisConfig{
				Host:     "   ",
				Port:     6379,
				PoolSize: 10,
			},
			want: "redis host",
		},
		{
			name: "invalid port",
			cfg: &config.RedisConfig{
				Host:     "localhost",
				Port:     0,
				PoolSize: 10,
			},
			want: "redis port",
		},
		{
			name: "negative db",
			cfg: &config.RedisConfig{
				Host:     "localhost",
				Port:     6379,
				DB:       -1,
				PoolSize: 10,
			},
			want: "redis db",
		},
		{
			name: "invalid pool",
			cfg: &config.RedisConfig{
				Host:     "localhost",
				Port:     6379,
				PoolSize: 0,
			},
			want: "pool_size",
		},
		{
			name: "url host",
			cfg: &config.RedisConfig{
				Host:     "redis://:secret-pass@localhost",
				Port:     6379,
				PoolSize: 10,
			},
			want: "not a URL",
		},
		{
			name: "userinfo host",
			cfg: &config.RedisConfig{
				Host:     "user@localhost",
				Port:     6379,
				PoolSize: 10,
			},
			want: "not a URL",
		},
		{
			name: "path host",
			cfg: &config.RedisConfig{
				Host:     "localhost/path",
				Port:     6379,
				PoolSize: 10,
			},
			want: "not a URL",
		},
		{
			name: "query host",
			cfg: &config.RedisConfig{
				Host:     "localhost?token=secret",
				Port:     6379,
				PoolSize: 10,
			},
			want: "not a URL",
		},
		{
			name: "fragment host",
			cfg: &config.RedisConfig{
				Host:     "localhost#secret",
				Port:     6379,
				PoolSize: 10,
			},
			want: "not a URL",
		},
		{
			name: "host includes port",
			cfg: &config.RedisConfig{
				Host:     "localhost:6379",
				Port:     6379,
				PoolSize: 10,
			},
			want: "must not include a port",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRedisConfig(tt.cfg)
			if err == nil {
				t.Fatal("validateRedisConfig returned nil error, want error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.want)
			}
		})
	}
}

func TestValidateRedisConfigDoesNotExposeInvalidHostSecrets(t *testing.T) {
	err := validateRedisConfig(&config.RedisConfig{
		Host:     "redis://:secret-pass@localhost?token=secret#fragment",
		Port:     6379,
		PoolSize: 10,
	})

	if err == nil {
		t.Fatal("validateRedisConfig returned nil error, want error")
	}
	for _, forbidden := range []string{"secret-pass", "token=secret", "#fragment"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("error exposed %q: %s", forbidden, err.Error())
		}
	}
}

func TestValidateRedisConfigAcceptsTrimmedHost(t *testing.T) {
	err := validateRedisConfig(&config.RedisConfig{
		Host:     " localhost ",
		Port:     6379,
		DB:       0,
		PoolSize: 10,
	})

	if err != nil {
		t.Fatalf("validateRedisConfig returned error: %v", err)
	}
}

func TestValidateRedisConfigAcceptsIPv6Host(t *testing.T) {
	tests := []string{"::1", " [::1] "}

	for _, host := range tests {
		t.Run(host, func(t *testing.T) {
			err := validateRedisConfig(&config.RedisConfig{
				Host:     host,
				Port:     6379,
				DB:       0,
				PoolSize: 10,
			})

			if err != nil {
				t.Fatalf("validateRedisConfig returned error: %v", err)
			}
		})
	}
}

func TestRedisAddressFormatsHosts(t *testing.T) {
	tests := []struct {
		name string
		host string
		want string
	}{
		{
			name: "hostname",
			host: " localhost ",
			want: "localhost:6379",
		},
		{
			name: "ipv6",
			host: " ::1 ",
			want: "[::1]:6379",
		},
		{
			name: "bracketed ipv6",
			host: " [::1] ",
			want: "[::1]:6379",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := redisAddress(tt.host, 6379); got != tt.want {
				t.Fatalf("redisAddress = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRedisConnectErrorRedactsSecretsAndPreservesCause(t *testing.T) {
	cause := errors.New(`dial tcp redis://:secret-pass@localhost:6379?token=secret#frag password=redis-secret`)

	err := redisConnectError(cause)

	if err == nil {
		t.Fatal("redisConnectError returned nil")
	}
	if !errors.Is(err, cause) {
		t.Fatal("redisConnectError did not preserve the original cause")
	}
	message := err.Error()
	for _, forbidden := range []string{"secret-pass", "token=secret", "redis-secret", "#frag"} {
		if strings.Contains(message, forbidden) {
			t.Fatalf("redisConnectError exposed %q: %s", forbidden, message)
		}
	}
	if !strings.Contains(message, "failed to connect redis") || !strings.Contains(message, "<redacted>") {
		t.Fatalf("redisConnectError did not keep useful redacted context: %s", message)
	}
}

func TestRedisConnectErrorAllowsNil(t *testing.T) {
	if err := redisConnectError(nil); err != nil {
		t.Fatalf("redisConnectError(nil) = %v, want nil", err)
	}
}

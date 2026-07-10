package discord

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/trae/midjourney-api/internal/config"
	"github.com/trae/midjourney-api/pkg/constants"
)

func TestDiscordClientRejectsInvalidRequestsBeforeHTTP(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewClient(testDiscordConfig(server.URL))

	tests := []struct {
		name string
		run  func() error
		want string
	}{
		{
			name: "nil imagine",
			run: func() error {
				return client.Imagine(context.Background(), nil)
			},
			want: "imagine request is required",
		},
		{
			name: "blank prompt",
			run: func() error {
				return client.Imagine(context.Background(), &ImagineRequest{
					Prompt:    "   ",
					UserToken: "token",
					GuildID:   "guild",
					ChannelID: "channel",
				})
			},
			want: "prompt is required",
		},
		{
			name: "blank token",
			run: func() error {
				return client.Imagine(context.Background(), &ImagineRequest{
					Prompt:    "prompt",
					UserToken: "   ",
					GuildID:   "guild",
					ChannelID: "channel",
				})
			},
			want: "user_token is required",
		},
		{
			name: "nil describe",
			run: func() error {
				return client.Describe(context.Background(), nil)
			},
			want: "describe request is required",
		},
		{
			name: "blank image",
			run: func() error {
				return client.Describe(context.Background(), &DescribeRequest{
					ImageURL:  "   ",
					UserToken: "token",
					GuildID:   "guild",
					ChannelID: "channel",
				})
			},
			want: "image_url is required",
		},
		{
			name: "invalid image url",
			run: func() error {
				return client.Describe(context.Background(), &DescribeRequest{
					ImageURL:  "ftp://example.com/image.png",
					UserToken: "token",
					GuildID:   "guild",
					ChannelID: "channel",
				})
			},
			want: "image_url must use http or https",
		},
		{
			name: "image url userinfo",
			run: func() error {
				return client.Describe(context.Background(), &DescribeRequest{
					ImageURL:  "https://user:pass@example.com/image.png",
					UserToken: "token",
					GuildID:   "guild",
					ChannelID: "channel",
				})
			},
			want: "image_url must not contain userinfo",
		},
		{
			name: "nil action",
			run: func() error {
				return client.PerformButtonAction(context.Background(), nil)
			},
			want: "button action request is required",
		},
		{
			name: "blank custom id",
			run: func() error {
				return client.PerformButtonAction(context.Background(), &ButtonActionRequest{
					CustomID:  "   ",
					MessageID: "message",
					UserToken: "token",
					GuildID:   "guild",
					ChannelID: "channel",
				})
			},
			want: "custom_id is required",
		},
		{
			name: "blank message id",
			run: func() error {
				return client.PerformButtonAction(context.Background(), &ButtonActionRequest{
					CustomID:  "custom",
					MessageID: "   ",
					UserToken: "token",
					GuildID:   "guild",
					ChannelID: "channel",
				})
			},
			want: "message_id is required",
		},
		{
			name: "direct nil payload",
			run: func() error {
				return client.doDiscordRequest(context.Background(), nil, "token", "guild", "channel")
			},
			want: "discord payload is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			if err == nil {
				t.Fatalf("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want to contain %q", err.Error(), tt.want)
			}
			if calls != 0 {
				t.Fatalf("HTTP calls = %d, want 0", calls)
			}
		})
	}
}

func TestDiscordClientRejectsNilConfig(t *testing.T) {
	client := NewClient(nil)

	err := client.Imagine(context.Background(), &ImagineRequest{
		Prompt:    "prompt",
		UserToken: "token",
		GuildID:   "guild",
		ChannelID: "channel",
	})

	if err == nil || !strings.Contains(err.Error(), "discord config is nil") {
		t.Fatalf("error = %v, want nil config error", err)
	}
}

func TestDiscordClientRejectsInvalidAPIBaseURLBeforeHTTP(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{
			name: "unsupported scheme",
			url:  "ftp://discord.example.com/api",
			want: "discord api_base_url must use http or https",
		},
		{
			name: "query",
			url:  "https://discord.example.com/api?token=secret",
			want: "query or fragment",
		},
		{
			name: "fragment",
			url:  "https://discord.example.com/api#fragment",
			want: "query or fragment",
		},
		{
			name: "userinfo",
			url:  "https://user:pass@discord.example.com/api",
			want: "discord api_base_url must not contain userinfo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()

			client := NewClient(testDiscordConfig(tt.url))

			err := client.Imagine(context.Background(), &ImagineRequest{
				Prompt:    "prompt",
				UserToken: "token",
				GuildID:   "guild",
				ChannelID: "channel",
			})

			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want API base URL validation containing %q", err, tt.want)
			}
			if calls != 0 {
				t.Fatalf("HTTP calls = %d, want 0", calls)
			}
		})
	}
}

func TestDiscordClientTrimsRequestFields(t *testing.T) {
	var gotAuth string
	var gotReferer string
	var gotPayload InteractionPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotReferer = r.Header.Get("Referer")
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewClient(testDiscordConfig(server.URL))
	err := client.Imagine(context.Background(), &ImagineRequest{
		Prompt:    "  a quiet harbor  ",
		UserToken: "  token  ",
		GuildID:   "  guild  ",
		ChannelID: "  channel  ",
	})
	if err != nil {
		t.Fatalf("Imagine returned error: %v", err)
	}

	if gotAuth != "token" {
		t.Fatalf("Authorization = %q, want trimmed token", gotAuth)
	}
	if gotReferer != "https://discord.com/channels/guild/channel" {
		t.Fatalf("Referer = %q, want trimmed guild/channel", gotReferer)
	}
	if gotPayload.GuildID != "guild" || gotPayload.ChannelID != "channel" {
		t.Fatalf("payload guild/channel = %q/%q, want trimmed", gotPayload.GuildID, gotPayload.ChannelID)
	}
	if len(gotPayload.Data.Options) != 1 || gotPayload.Data.Options[0].Value != "a quiet harbor" {
		t.Fatalf("payload options = %#v, want trimmed prompt", gotPayload.Data.Options)
	}
}

func TestDiscordClientTrimsConfigFieldsWithoutMutatingInput(t *testing.T) {
	var gotPayload InteractionPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	cfg := &config.DiscordConfig{
		ApplicationID:         " application ",
		ImagineCommandID:      " imagine-command ",
		ImagineCommandVersion: " imagine-version ",
		APIBaseURL:            " " + server.URL + " ",
	}
	client := NewClient(cfg)

	err := client.Imagine(context.Background(), &ImagineRequest{
		Prompt:    "a quiet harbor",
		UserToken: "token",
		GuildID:   "guild",
		ChannelID: "channel",
	})
	if err != nil {
		t.Fatalf("Imagine returned error: %v", err)
	}

	if gotPayload.ApplicationID != "application" ||
		gotPayload.Data.ID != "imagine-command" ||
		gotPayload.Data.Version != "imagine-version" ||
		gotPayload.Data.ApplicationCommand.ApplicationID != "application" ||
		gotPayload.Data.ApplicationCommand.ID != "imagine-command" ||
		gotPayload.Data.ApplicationCommand.Version != "imagine-version" {
		t.Fatalf("payload used untrimmed config fields: %#v", gotPayload)
	}
	if cfg.ApplicationID != " application " ||
		cfg.ImagineCommandID != " imagine-command " ||
		cfg.ImagineCommandVersion != " imagine-version " ||
		cfg.APIBaseURL != " "+server.URL+" " {
		t.Fatalf("NewClient mutated caller config: %#v", cfg)
	}
}

func TestDiscordRequestAllowsNilHTTPClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := &Client{cfg: testDiscordConfig(server.URL)}

	if err := client.doDiscordRequest(context.Background(), map[string]string{"ok": "true"}, "token", "guild", "channel"); err != nil {
		t.Fatalf("doDiscordRequest returned error: %v", err)
	}
}

func TestDiscordClientNilHTTPClientUsesTimeoutFallback(t *testing.T) {
	client := &Client{}

	got := client.httpClientOrDefault()
	if got == nil {
		t.Fatal("fallback HTTP client was nil")
	}
	if got == http.DefaultClient {
		t.Fatal("fallback HTTP client must not be http.DefaultClient without a timeout")
	}
	if got.Timeout != constants.DefaultHTTPTimeout {
		t.Fatalf("fallback timeout = %s, want %s", got.Timeout, constants.DefaultHTTPTimeout)
	}
	transport, ok := got.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("fallback transport = %T, want *http.Transport", got.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("fallback HTTP client must not use environment proxies for Discord tokens")
	}
}

func TestDiscordRequestDoesNotFollowRedirects(t *testing.T) {
	redirected := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirected" {
			redirected++
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Redirect(w, r, "/redirected", http.StatusFound)
	}))
	defer server.Close()

	client := NewClient(testDiscordConfig(server.URL))

	err := client.doDiscordRequest(context.Background(), map[string]string{"ok": "true"}, "token", "guild", "channel")
	if err == nil {
		t.Fatal("expected redirect response to be treated as Discord API error")
	}
	if !strings.Contains(err.Error(), "status=302") {
		t.Fatalf("error = %q, want 302 status", err.Error())
	}
	if redirected != 0 {
		t.Fatalf("redirect target calls = %d, want 0", redirected)
	}
}

func TestDiscordRequestUsesContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewClient(testDiscordConfig(server.URL))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := client.doDiscordRequest(ctx, map[string]string{"ok": "true"}, "token", "guild", "channel")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed >= 150*time.Millisecond {
		t.Fatalf("request took %s, context deadline was not applied promptly", elapsed)
	}
}

func TestDiscordRequestRedactsTransportError(t *testing.T) {
	cause := errors.New(`transport failed for https://user:pass@discord.example.com/api/interactions?token=secret#frag custom_id="secret-custom-id" user_token="secret-token"`)
	client := NewClient(testDiscordConfig("https://discord.example.com/api"))
	client.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, cause
		}),
	}

	err := client.doDiscordRequest(context.Background(), map[string]string{"ok": "true"}, "token", "guild", "channel")

	if err == nil {
		t.Fatal("expected transport error")
	}
	if !errors.Is(err, cause) {
		t.Fatal("transport error did not preserve original cause")
	}
	msg := err.Error()
	for _, forbidden := range []string{"secret-token", "token=secret", "user:pass", "#frag", "custom_id", "secret-custom-id"} {
		if strings.Contains(msg, forbidden) {
			t.Fatalf("transport error exposed %q: %s", forbidden, msg)
		}
	}
	if !strings.Contains(msg, "failed to send request") ||
		!strings.Contains(msg, "https://discord.example.com/api/interactions") ||
		!strings.Contains(msg, `user_token="<redacted>"`) {
		t.Fatalf("transport error did not keep useful redacted context: %s", msg)
	}
}

func TestDiscordDescribeAllowsImageURLQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewClient(testDiscordConfig(server.URL))

	err := client.Describe(context.Background(), &DescribeRequest{
		ImageURL:  "https://cdn.example.com/image.png?token=secret#fragment",
		UserToken: "token",
		GuildID:   "guild",
		ChannelID: "channel",
	})
	if err != nil {
		t.Fatalf("Describe returned error for image URL with query/fragment: %v", err)
	}
}

func TestDiscordRequestLimitsErrorBody(t *testing.T) {
	largeBody := strings.Repeat("x", maxDiscordErrorBodyBytes+256)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(largeBody))
	}))
	defer server.Close()

	client := NewClient(testDiscordConfig(server.URL))

	err := client.doDiscordRequest(context.Background(), map[string]string{"ok": "true"}, "token", "guild", "channel")
	if err == nil {
		t.Fatalf("expected discord API error")
	}

	msg := err.Error()
	if strings.Contains(msg, largeBody) {
		t.Fatalf("error included full response body")
	}
	if !strings.Contains(msg, "...<truncated>") {
		t.Fatalf("error did not mark truncated body: %s", msg)
	}
}

func TestReadDiscordErrorBodyKeepsUTF8AfterTruncation(t *testing.T) {
	largeBody := strings.Repeat("好", maxDiscordErrorBodyBytes)

	got := readDiscordErrorBody(bytes.NewBufferString(largeBody))

	if !utf8.ValidString(got) {
		t.Fatalf("error body is not valid UTF-8: %q", got)
	}
	if !strings.Contains(got, "...<truncated>") {
		t.Fatalf("error body missing truncated marker: %s", got)
	}
}

func TestDiscordRequestRedactsErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"user_token":"secret-token","custom_id":"MJ::JOB::upsample::1::secret-button","discord_message_id":"message-secret","buttons":["secret-button"],"callback":"https://user:pass@example.com/hook?token=secret#frag"}`))
	}))
	defer server.Close()

	client := NewClient(testDiscordConfig(server.URL))

	err := client.doDiscordRequest(context.Background(), map[string]string{"ok": "true"}, "token", "guild", "channel")
	if err == nil {
		t.Fatalf("expected discord API error")
	}

	msg := err.Error()
	for _, forbidden := range []string{
		"secret-token",
		"token=secret",
		"user:pass",
		"#frag",
		"custom_id",
		"discord_message_id",
		"buttons",
		"MJ::JOB",
		"secret-button",
		"message-secret",
	} {
		if strings.Contains(msg, forbidden) {
			t.Fatalf("discord error exposed %q: %s", forbidden, msg)
		}
	}
	if !strings.Contains(msg, `user_token\":\"<redacted>`) || !strings.Contains(msg, "https://example.com/hook") {
		t.Fatalf("discord error did not keep useful redacted context: %s", msg)
	}
}

func testDiscordConfig(apiBaseURL string) *config.DiscordConfig {
	return &config.DiscordConfig{
		ApplicationID:          "application",
		ImagineCommandID:       "imagine-command",
		ImagineCommandVersion:  "imagine-version",
		DescribeCommandID:      "describe-command",
		DescribeCommandVersion: "describe-version",
		APIBaseURL:             apiBaseURL,
	}
}

func TestDiscordInteractionsURLTrimsTrailingSlash(t *testing.T) {
	got := discordInteractionsURL("https://discord.com/api/v9/")
	want := "https://discord.com/api/v9/interactions"
	if got != want {
		t.Fatalf("discordInteractionsURL = %q, want %q", got, want)
	}
}

func TestGenerateUUIDReturnsCompactHexID(t *testing.T) {
	got := generateUUID()

	if len(got) != 32 {
		t.Fatalf("generateUUID length = %d, want 32: %q", len(got), got)
	}
	if strings.Contains(got, "-") {
		t.Fatalf("generateUUID returned dashed value: %q", got)
	}
	if _, err := hex.DecodeString(got); err != nil {
		t.Fatalf("generateUUID returned non-hex value %q: %v", got, err)
	}
}

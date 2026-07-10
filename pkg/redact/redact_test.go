package redact

import (
	"errors"
	"strings"
	"testing"
)

func TestURLDropsQueryAndFragment(t *testing.T) {
	got := URL("https://example.com/image.png?token=secret#fragment")
	want := "https://example.com/image.png"
	if got != want {
		t.Fatalf("URL = %q, want %q", got, want)
	}
}

func TestURLDropsUserInfo(t *testing.T) {
	got := URL("https://user:secret@example.com/image.png?token=secret")
	want := "https://example.com/image.png"
	if got != want {
		t.Fatalf("URL = %q, want %q", got, want)
	}
}

func TestURLHandlesInvalidInput(t *testing.T) {
	if got := URL("%zz"); got != "<invalid-url>" {
		t.Fatalf("URL = %q, want invalid placeholder", got)
	}
}

func TestTextRedactsURLsAndSecrets(t *testing.T) {
	input := `discord API error: Authorization: Bearer abc.def.ghi user_token="secret-token" callback=https://user:pass@example.com/hook?token=secret#frag api_key=secret-key`

	got := Text(input)
	for _, forbidden := range []string{
		"abc.def.ghi",
		"secret-token",
		"token=secret",
		"user:pass",
		"secret-key",
		"?",
		"#frag",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("Text exposed %q: %s", forbidden, got)
		}
	}
	for _, expected := range []string{
		"Authorization: Bearer <redacted>",
		`user_token="<redacted>"`,
		"https://example.com/hook",
		"api_key=<redacted>",
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("Text omitted %q: %s", expected, got)
		}
	}
}

func TestTextRedactsURLsWithCaseInsensitiveScheme(t *testing.T) {
	input := `callback=HTTPS://user:pass@example.com/hook?token=secret#frag`

	got := Text(input)
	for _, forbidden := range []string{
		"user:pass",
		"token=secret",
		"#frag",
		"?",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("Text exposed %q: %s", forbidden, got)
		}
	}
	if !strings.Contains(got, "https://example.com/hook") {
		t.Fatalf("Text omitted redacted URL: %s", got)
	}
}

func TestTextRedactsNonHTTPURLs(t *testing.T) {
	input := `redis dial failed: redis://:secret-pass@localhost:6379?token=secret#frag`

	got := Text(input)
	for _, forbidden := range []string{
		"secret-pass",
		"token=secret",
		"#frag",
		"?",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("Text exposed %q: %s", forbidden, got)
		}
	}
	if !strings.Contains(got, "redis://localhost:6379") {
		t.Fatalf("Text omitted redacted Redis URL: %s", got)
	}
}

func TestTextRedactsDiscordTokenShape(t *testing.T) {
	token := "aaaaaaaaaaaaaaaaaaaaaaaa.bbbbbb.cccccccccccccccccccccccc"
	got := Text("discord returned " + token)
	if strings.Contains(got, token) || !strings.Contains(got, "<redacted>") {
		t.Fatalf("Text did not redact Discord token: %s", got)
	}
}

func TestTextRedactsStorageAndAuthorizationKeyValues(t *testing.T) {
	input := `OSSAccessKeyId=oss-key-value Signature=super-sig-value authorization=auth-secret-value secret_access_key=storage-secret-value access-key-id=legacy-key "authorization":"json-secret-value"`

	got := Text(input)
	for _, forbidden := range []string{
		"oss-key-value",
		"super-sig-value",
		"auth-secret-value",
		"storage-secret-value",
		"legacy-key",
		"json-secret-value",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("Text exposed %q: %s", forbidden, got)
		}
	}
	if count := strings.Count(got, "<redacted>"); count != 6 {
		t.Fatalf("redaction count = %d, want 6 in %q", count, got)
	}
}

func TestTextRedactsAuthorizationHeaderSchemes(t *testing.T) {
	input := `proxy failed Authorization: Basic dXNlcjpwYXNz next Authorization: Token secret-token`

	got := Text(input)
	for _, forbidden := range []string{
		"dXNlcjpwYXNz",
		"secret-token",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("Text exposed %q: %s", forbidden, got)
		}
	}
	for _, expected := range []string{
		"Authorization: Basic <redacted>",
		"Authorization: Token <redacted>",
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("Text omitted %q: %s", expected, got)
		}
	}
}

func TestTextRedactsDiscordInternalFields(t *testing.T) {
	input := `discord API error: {"custom_id":"MJ::JOB::upsample::1::secret-button","discord_message_id":"message-secret","message_id":"message-secret-2","buttons":["secret-button"]} custom-id=legacy-secret`

	got := Text(input)
	for _, forbidden := range []string{
		"custom_id",
		"custom-id",
		"discord_message_id",
		"message_id",
		"buttons",
		"MJ::JOB",
		"secret-button",
		"message-secret",
		"legacy-secret",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("Text exposed %q: %s", forbidden, got)
		}
	}
	if !strings.Contains(got, "<redacted>") {
		t.Fatalf("Text did not keep redaction marker: %s", got)
	}
}

func TestErrorRedactsTextAndPreservesCause(t *testing.T) {
	cause := errors.New(`sdk failed access_key_id=storage-key callback=https://user:pass@example.com/hook?token=secret#frag`)

	err := Error(cause)

	if err == nil {
		t.Fatal("Error returned nil")
	}
	if !errors.Is(err, cause) {
		t.Fatal("Error did not preserve the original cause")
	}
	message := err.Error()
	for _, forbidden := range []string{"storage-key", "token=secret", "user:pass", "#frag"} {
		if strings.Contains(message, forbidden) {
			t.Fatalf("Error exposed %q: %s", forbidden, message)
		}
	}
	if !strings.Contains(message, "access_key_id=<redacted>") || !strings.Contains(message, "https://example.com/hook") {
		t.Fatalf("Error did not keep useful redacted context: %s", message)
	}
}

func TestErrorAllowsNil(t *testing.T) {
	if err := Error(nil); err != nil {
		t.Fatalf("Error(nil) = %v, want nil", err)
	}
}

func TestTruncateRunesUsesRuneBoundaries(t *testing.T) {
	got := TruncateRunes("好好好", 2)
	if got != "好好" {
		t.Fatalf("TruncateRunes = %q, want two runes", got)
	}
	if got := TruncateRunes("abc", 0); got != "" {
		t.Fatalf("TruncateRunes with zero max = %q, want empty", got)
	}
}

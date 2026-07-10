package redact

import (
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	textURLPattern      = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s"'<>]+`)
	discordFieldPattern = regexp.MustCompile(
		`(?i)(["']?)(custom[_-]?id|discord[_-]?message[_-]?id|message[_-]?id|buttons)(["']?)(\s*[:=]\s*)(\[[^\]]*\]|"[^"]*"|'[^']*'|[^"'\s,;&}\]]+)`,
	)
	sensitiveKeyPattern = regexp.MustCompile(
		`(?i)(["']?)(api[_-]?key|access[_-]?key(?:[_-]?id)?|ossaccesskeyid|secret[_-]?access[_-]?key|access[_-]?token|refresh[_-]?token|user[_-]?token|signature|token|secret|password)(["']?)(\s*[:=]\s*)(["']?)([^"'\s,;&}\]]+)`,
	)
	authorizationEqualsPattern = regexp.MustCompile(`(?i)(["']?authorization["']?\s*=\s*["']?)([^"'\s,;&}\]]+)`)
	authorizationJSONPattern   = regexp.MustCompile(`(?i)(["']authorization["']\s*:\s*["'])([^"']+)(["'])`)
	authorizationHeaderPattern = regexp.MustCompile(`(?i)(\bauthorization\s*:\s*[a-z]+\s+)([^"'\s,;&}\]]+)`)
	bearerTokenPattern         = regexp.MustCompile(`(?i)\b(Bearer\s+)[A-Za-z0-9._~+/=-]+`)
	discordTokenPattern        = regexp.MustCompile(`\b[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{20,}\b`)
)

const placeholder = "<redacted>"

// URL removes query parameters and fragments before a URL is written to logs or errors.
func URL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "<invalid-url>"
	}
	u.RawQuery = ""
	u.Fragment = ""
	u.User = nil
	return u.String()
}

// Text removes common secret forms before text is persisted or returned by APIs.
func Text(value string) string {
	text := strings.TrimSpace(value)
	if text == "" {
		return ""
	}

	text = textURLPattern.ReplaceAllStringFunc(text, redactURLInText)
	text = discordFieldPattern.ReplaceAllString(text, placeholder)
	text = authorizationHeaderPattern.ReplaceAllString(text, `${1}`+placeholder)
	text = bearerTokenPattern.ReplaceAllString(text, `${1}`+placeholder)
	text = sensitiveKeyPattern.ReplaceAllString(text, `${1}${2}${3}${4}${5}`+placeholder)
	text = authorizationEqualsPattern.ReplaceAllString(text, `${1}`+placeholder)
	text = authorizationJSONPattern.ReplaceAllString(text, `${1}`+placeholder+`${3}`)
	text = discordTokenPattern.ReplaceAllString(text, placeholder)
	return text
}

type redactedError struct {
	message string
	cause   error
}

func (e redactedError) Error() string {
	return e.message
}

func (e redactedError) Unwrap() error {
	return e.cause
}

// Error returns an error whose public text is redacted while preserving the original cause.
func Error(err error) error {
	if err == nil {
		return nil
	}
	return redactedError{
		message: Text(err.Error()),
		cause:   err,
	}
}

func redactURLInText(rawURL string) string {
	urlPart, suffix := splitTrailingPunctuation(rawURL)
	if urlPart == "" {
		return rawURL
	}
	return URL(urlPart) + suffix
}

func splitTrailingPunctuation(value string) (body string, suffix string) {
	body = value
	for body != "" {
		r, size := utf8.DecodeLastRuneInString(body)
		if r == utf8.RuneError && size == 0 {
			break
		}
		if !strings.ContainsRune(".,;:!?", r) {
			break
		}
		suffix = string(r) + suffix
		body = body[:len(body)-size]
	}
	return body, suffix
}

func TruncateRunes(value string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	count := 0
	for index := range value {
		if count == maxRunes {
			return value[:index]
		}
		count++
	}
	return value
}

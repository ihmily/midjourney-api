package oss

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"unicode"

	appconfig "github.com/trae/midjourney-api/internal/config"
	"github.com/trae/midjourney-api/internal/safehttp"
	"github.com/trae/midjourney-api/pkg/constants"
	"github.com/trae/midjourney-api/pkg/redact"
	"go.uber.org/zap"
)

type Uploader interface {
	// UploadFromURL downloads an image from the source URL, uploads it to OSS,
	// and returns the final access URL.
	UploadFromURL(ctx context.Context, taskID string, imageURL string) (string, error)
}

var imageDownloadClient = newImageDownloadHTTPClient()

const maxObjectFilenameLength = 128

func NewUploader(cfg *appconfig.OSSConfig, logger *zap.Logger) (Uploader, error) {
	if cfg == nil {
		return nil, fmt.Errorf("oss config is required")
	}

	if !cfg.Enable {
		return nil, nil
	}

	switch strings.ToLower(strings.TrimSpace(cfg.Provider)) {
	case "s3":
		return newS3Uploader(&cfg.S3, logger)
	case "aliyun":
		return newAliyunUploader(&cfg.Aliyun, logger)
	default:
		return nil, fmt.Errorf("unsupported OSS provider: %s (supported: s3, aliyun)", cfg.Provider)
	}
}

func logURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	return redact.URL(rawURL)
}

func ossLogger(logger *zap.Logger) *zap.Logger {
	if logger == nil {
		return zap.NewNop()
	}
	return logger
}

func ossSDKError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, redact.Error(err))
}

func downloadImage(ctx context.Context, imageURL string) ([]byte, string, error) {
	normalizedURL, err := normalizeImageDownloadURL(imageURL)
	if err != nil {
		return nil, "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, normalizedURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create image download request: %w", err)
	}

	resp, err := imageDownloadClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("failed to download image from %s: %s", redact.URL(normalizedURL), redact.Text(err.Error()))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("download image failed with status %d: %s", resp.StatusCode, redact.URL(normalizedURL))
	}

	if resp.ContentLength > constants.MaxImageDownloadBytes {
		return nil, "", fmt.Errorf("download image exceeds max size %d bytes: %s",
			constants.MaxImageDownloadBytes, redact.URL(normalizedURL))
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, constants.MaxImageDownloadBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("failed to read image body: %w", err)
	}
	if int64(len(data)) > constants.MaxImageDownloadBytes {
		return nil, "", fmt.Errorf("download image exceeds max size %d bytes: %s",
			constants.MaxImageDownloadBytes, redact.URL(normalizedURL))
	}

	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = http.DetectContentType(data)
	} else if !isImageContentType(contentType) {
		return nil, "", fmt.Errorf("download image returned non-image content type %q: %s",
			contentType, redact.URL(normalizedURL))
	}
	if !isImageContentType(contentType) {
		return nil, "", fmt.Errorf("download image returned non-image content type %q: %s",
			contentType, redact.URL(normalizedURL))
	}
	if sniffed := http.DetectContentType(data); isUnsafeImageBodyContentType(sniffed) {
		return nil, "", fmt.Errorf("download image body content type %q is not allowed: %s",
			sniffed, redact.URL(normalizedURL))
	}

	return data, contentType, nil
}

func newImageDownloadHTTPClient() *http.Client {
	return safehttp.NewPublicClient(constants.DefaultHTTPTimeout, "image download", validateImageDownloadURL)
}

func isImageContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	switch strings.ToLower(mediaType) {
	case "image/jpeg",
		"image/png",
		"image/gif",
		"image/webp",
		"image/bmp",
		"image/tiff":
		return true
	default:
		return false
	}
}

func isUnsafeImageBodyContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return true
	}
	switch strings.ToLower(mediaType) {
	case "text/html",
		"text/plain",
		"text/xml",
		"application/xml",
		"application/json",
		"application/xhtml+xml",
		"image/svg+xml":
		return true
	default:
		return false
	}
}

func validateImageDownloadURL(rawURL string) error {
	_, err := normalizeImageDownloadURL(rawURL)
	return err
}

func normalizeImageDownloadURL(rawURL string) (string, error) {
	return safehttp.NormalizePublicHTTPURL(rawURL, "image URL")
}

func buildObjectKey(prefix, taskID, imageURL string) string {
	taskSegment := sanitizeObjectKeySegment(taskID)
	if taskSegment == "" {
		taskSegment = "task"
	}

	filename := extractFilename(imageURL)
	if filename == "" || filename == "." {
		filename = "image.png"
	}

	key := taskSegment + "-" + filename
	prefix = cleanObjectPrefix(prefix)
	if prefix != "" {
		key = prefix + "/" + key
	}
	return key
}

func objectKeyURLPath(key string) string {
	segments := strings.Split(key, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

func extractFilename(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	filename := path.Base(u.EscapedPath())
	if filename == "." || filename == "/" {
		return ""
	}
	if unescaped, err := url.PathUnescape(filename); err == nil {
		filename = unescaped
	}
	return sanitizeObjectFilename(filename)
}

func cleanObjectPrefix(prefix string) string {
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	if prefix == "" {
		return ""
	}

	rawSegments := strings.Split(prefix, "/")
	segments := make([]string, 0, len(rawSegments))
	for _, segment := range rawSegments {
		sanitized := sanitizeObjectKeySegment(segment)
		if sanitized != "" {
			segments = append(segments, sanitized)
		}
	}
	return strings.Join(segments, "/")
}

func sanitizeObjectKeySegment(segment string) string {
	return sanitizeObjectFilename(segment)
}

func sanitizeObjectFilename(filename string) string {
	filename = strings.TrimSpace(filename)
	if filename == "" || filename == "." || filename == ".." {
		return ""
	}

	var builder strings.Builder
	builder.Grow(len(filename))
	lastUnderscore := false
	for _, r := range filename {
		if isObjectFilenameRune(r) {
			builder.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			builder.WriteByte('_')
			lastUnderscore = true
		}
	}

	sanitized := strings.Trim(builder.String(), "._-")
	if sanitized == "" {
		return ""
	}
	return truncateObjectFilename(sanitized, maxObjectFilenameLength)
}

func isObjectFilenameRune(r rune) bool {
	return r <= unicode.MaxASCII &&
		((r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '.' ||
			r == '-' ||
			r == '_')
}

func truncateObjectFilename(filename string, limit int) string {
	if limit <= 0 || len(filename) <= limit {
		return filename
	}

	ext := path.Ext(filename)
	if len(ext) == 0 || len(ext) >= limit {
		return filename[:limit]
	}

	stemLimit := limit - len(ext)
	if stemLimit <= 0 {
		return filename[:limit]
	}
	return filename[:stemLimit] + ext
}

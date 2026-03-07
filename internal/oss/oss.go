package oss

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"

	appconfig "github.com/trae/midjourney-api/internal/config"
	"go.uber.org/zap"
)

type Uploader interface {
	// UploadFromURL downloads an image from the original URL and uploads it to OSS, returning the OSS access URL
	UploadFromURL(ctx context.Context, taskID string, imageURL string) (string, error)
}

func NewUploader(cfg *appconfig.OSSConfig, logger *zap.Logger) (Uploader, error) {
	if !cfg.Enable {
		return nil, nil
	}
	switch strings.ToLower(cfg.Provider) {
	case "s3":
		return newS3Uploader(&cfg.S3, logger)
	case "aliyun":
		return newAliyunUploader(&cfg.Aliyun, logger)
	default:
		return nil, fmt.Errorf("unsupported OSS provider: %s (supported: s3, aliyun)", cfg.Provider)
	}
}

func downloadImage(imageURL string) ([]byte, string, error) {
	resp, err := http.Get(imageURL) //nolint:gosec
	if err != nil {
		return nil, "", fmt.Errorf("failed to download image from %s: %w", imageURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("download image failed with status %d: %s", resp.StatusCode, imageURL)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read image body: %w", err)
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/png"
	}

	return data, contentType, nil
}

// return the Key：{prefix}/{taskID}-{filename}
func buildObjectKey(prefix, taskID, imageURL string) string {
	filename := extractFilename(imageURL)
	if filename == "" || filename == "." {
		filename = taskID + ".png"
	}
	key := taskID + "-" + filename
	if prefix != "" {
		key = strings.TrimSuffix(prefix, "/") + "/" + key
	}
	return key
}

func extractFilename(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return path.Base(strings.Split(u.Path, "?")[0])
}

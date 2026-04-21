package oss

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	alioss "github.com/aliyun/aliyun-oss-go-sdk/oss"
	appconfig "github.com/trae/midjourney-api/internal/config"
	"go.uber.org/zap"
)

type aliyunUploader struct {
	bucket *alioss.Bucket
	cfg    *appconfig.AliyunOSSConfig
	logger *zap.Logger
}

func newAliyunUploader(cfg *appconfig.AliyunOSSConfig, logger *zap.Logger) (Uploader, error) {
	opts := []alioss.ClientOption{}
	endpoint := cfg.Endpoint
	if cfg.IsCname && cfg.CnameDomain != "" {
		opts = append(opts, alioss.UseCname(true))
		endpoint = cfg.CnameDomain
	}

	client, err := alioss.New(endpoint, cfg.AccessKeyID, cfg.AccessKeySecret, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create Aliyun OSS client: %w", err)
	}

	bucket, err := client.Bucket(cfg.BucketName)
	if err != nil {
		return nil, fmt.Errorf("failed to get Aliyun OSS bucket: %w", err)
	}

	logger.Info("Aliyun OSS uploader initialized",
		zap.String("endpoint", cfg.Endpoint),
		zap.String("bucket", cfg.BucketName),
	)

	return &aliyunUploader{
		bucket: bucket,
		cfg:    cfg,
		logger: logger,
	}, nil
}

func (u *aliyunUploader) UploadFromURL(ctx context.Context, taskID string, imageURL string) (string, error) {
	data, contentType, err := downloadImage(imageURL)
	if err != nil {
		return "", err
	}

	key := buildObjectKey(u.cfg.Prefix, taskID, imageURL)

	if err := u.bucket.PutObject(key, bytes.NewReader(data),
		alioss.ContentType(contentType),
	); err != nil {
		return "", fmt.Errorf("Aliyun OSS PutObject failed: %w", err)
	}

	ossURL, err := u.buildURL(key)
	if err != nil {
		return "", err
	}

	u.logger.Info("Image uploaded to Aliyun OSS",
		zap.String("task_id", taskID),
		zap.String("key", key),
		zap.String("oss_url", ossURL),
	)
	return ossURL, nil
}

// buildURL builds the object access URL (supports signed, CNAME, and standard三种 modes)
func (u *aliyunUploader) buildURL(key string) (string, error) {
	// Mode 1: Signed URL
	if u.cfg.ToSign {
		signedURL, err := u.bucket.SignURL(key, alioss.HTTPGet, int64(u.cfg.SignExpires))
		if err != nil {
			return "", fmt.Errorf("failed to sign Aliyun OSS URL: %w", err)
		}
		return signedURL, nil
	}

	// Mode 2: Custom Domain (CNAME)
	if u.cfg.IsCname && u.cfg.CnameDomain != "" {
		domain := strings.TrimSuffix(u.cfg.CnameDomain, "/")
		return fmt.Sprintf("%s/%s", domain, key), nil
	}

	// Mode 3: Standard OSS Domain: https://{bucket}.{endpoint}/{key}
	endpoint := u.cfg.Endpoint
	endpoint = strings.TrimSuffix(endpoint, "/")
	endpoint = strings.TrimPrefix(endpoint, "https://")
	endpoint = strings.TrimPrefix(endpoint, "http://")
	return fmt.Sprintf("https://%s.%s/%s", u.cfg.BucketName, endpoint, key), nil
}

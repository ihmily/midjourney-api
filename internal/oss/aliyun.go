package oss

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
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
	if cfg == nil {
		return nil, fmt.Errorf("oss.aliyun config is required")
	}
	logger = ossLogger(logger)

	opts := []alioss.ClientOption{}
	endpoint := cfg.Endpoint
	if cfg.IsCname && cfg.CnameDomain != "" {
		opts = append(opts, alioss.UseCname(true))
		endpoint = cfg.CnameDomain
	}

	client, err := alioss.New(endpoint, cfg.AccessKeyID, cfg.AccessKeySecret, opts...)
	if err != nil {
		return nil, ossSDKError("failed to create Aliyun OSS client", err)
	}

	bucket, err := client.Bucket(cfg.BucketName)
	if err != nil {
		return nil, ossSDKError("failed to get Aliyun OSS bucket", err)
	}

	logger.Info("Aliyun OSS uploader initialized",
		zap.String("endpoint", logURL(cfg.Endpoint)),
		zap.String("bucket", cfg.BucketName),
	)

	return &aliyunUploader{
		bucket: bucket,
		cfg:    cfg,
		logger: logger,
	}, nil
}

func (u *aliyunUploader) UploadFromURL(ctx context.Context, taskID string, imageURL string) (string, error) {
	data, contentType, err := downloadImage(ctx, imageURL)
	if err != nil {
		return "", err
	}

	key := buildObjectKey(u.cfg.Prefix, taskID, imageURL)

	if err := u.bucket.PutObject(key, bytes.NewReader(data),
		alioss.ContentType(contentType),
	); err != nil {
		return "", ossSDKError("Aliyun OSS PutObject failed", err)
	}

	ossURL, err := u.buildURL(key)
	if err != nil {
		return "", err
	}

	u.logger.Info("Image uploaded to Aliyun OSS",
		zap.String("task_id", taskID),
		zap.String("key", key),
		zap.String("oss_url", logURL(ossURL)),
	)
	return ossURL, nil
}

func (u *aliyunUploader) buildURL(key string) (string, error) {
	if u.cfg.ToSign {
		signedURL, err := u.bucket.SignURL(key, alioss.HTTPGet, int64(u.cfg.SignExpires))
		if err != nil {
			return "", ossSDKError("failed to sign Aliyun OSS URL", err)
		}
		return signedURL, nil
	}

	escapedKey := objectKeyURLPath(key)
	if u.cfg.IsCname && u.cfg.CnameDomain != "" {
		domain := strings.TrimSuffix(u.cfg.CnameDomain, "/")
		return fmt.Sprintf("%s/%s", domain, escapedKey), nil
	}

	endpoint := strings.TrimSuffix(u.cfg.Endpoint, "/")
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("invalid Aliyun OSS endpoint URL")
	}
	endpoint = parsed.Host
	return fmt.Sprintf("https://%s.%s/%s", u.cfg.BucketName, endpoint, escapedKey), nil
}

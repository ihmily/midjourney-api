package oss

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	appconfig "github.com/trae/midjourney-api/internal/config"
	"github.com/trae/midjourney-api/pkg/constants"
	"go.uber.org/zap"
)

type s3Uploader struct {
	client *s3.Client
	cfg    *appconfig.S3Config
	logger *zap.Logger
}

func newS3Uploader(cfg *appconfig.S3Config, logger *zap.Logger) (Uploader, error) {
	if cfg == nil {
		return nil, fmt.Errorf("oss.s3 config is required")
	}
	logger = ossLogger(logger)

	ctx, cancel := context.WithTimeout(context.Background(), constants.DefaultHTTPTimeout)
	defer cancel()

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(cfg.Region),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		),
	)
	if err != nil {
		return nil, ossSDKError("failed to load AWS config", err)
	}

	clientOpts := []func(*s3.Options){
		func(o *s3.Options) {
			// Force path style to be compatible with non-AWS S3 compatible services
			o.UsePathStyle = true
		},
	}
	if cfg.EndpointURL != "" {
		endpointURL := cfg.EndpointURL
		clientOpts = append(clientOpts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(endpointURL)
		})
	}

	client := s3.NewFromConfig(awsCfg, clientOpts...)

	logger.Info("S3 uploader initialized",
		zap.String("endpoint", logURL(cfg.EndpointURL)),
		zap.String("region", cfg.Region),
		zap.String("bucket", cfg.BucketName),
	)

	return &s3Uploader{
		client: client,
		cfg:    cfg,
		logger: logger,
	}, nil
}

func (u *s3Uploader) UploadFromURL(ctx context.Context, taskID string, imageURL string) (string, error) {
	data, contentType, err := downloadImage(ctx, imageURL)
	if err != nil {
		return "", err
	}

	key := buildObjectKey(u.cfg.Prefix, taskID, imageURL)
	size := int64(len(data))

	_, err = u.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(u.cfg.BucketName),
		Key:           aws.String(key),
		Body:          bytes.NewReader(data),
		ContentLength: aws.Int64(size),
		ContentType:   aws.String(contentType),
	})
	if err != nil {
		return "", ossSDKError("S3 PutObject failed", err)
	}

	ossURL := u.buildPublicURL(key)
	u.logger.Info("Image uploaded to S3",
		zap.String("task_id", taskID),
		zap.String("key", key),
		zap.String("oss_url", logURL(ossURL)),
	)
	return ossURL, nil
}

func (u *s3Uploader) buildPublicURL(key string) string {
	escapedKey := objectKeyURLPath(key)
	if u.cfg.EndpointURL != "" {
		endpoint := strings.TrimSuffix(u.cfg.EndpointURL, "/")
		return fmt.Sprintf("%s/%s/%s", endpoint, u.cfg.BucketName, escapedKey)
	}
	return fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", u.cfg.BucketName, u.cfg.Region, escapedKey)
}

// Package storage uploads and fetches objects from Cloudflare R2 via its
// S3-compatible API. R2 is used purely as public object storage here: audio
// files and each channel's feed.xml, served over a public r2.dev (or custom
// domain) URL with no authentication layer, since these are private-by-
// obscurity personal feeds rather than anything access-controlled.
package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Config is everything needed to reach one R2 bucket.
type Config struct {
	AccountID       string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	// PublicBaseURL is the bucket's public URL (its r2.dev subdomain, or a
	// connected custom domain), used to build the URLs objects are served at.
	PublicBaseURL string
}

// Store is a thin client bound to one bucket.
type Store struct {
	client        *s3.Client
	bucket        string
	publicBaseURL string
}

// New builds a Store, pointing the AWS S3 client at R2's account-scoped
// S3-compatible endpoint instead of AWS itself.
func New(ctx context.Context, cfg Config) (*Store, error) {
	if cfg.AccountID == "" || cfg.Bucket == "" || cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return nil, fmt.Errorf("storage: account id, bucket, access key id and secret access key are all required")
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("auto"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			cfg.AccessKeyID, cfg.SecretAccessKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("storage: loading AWS config: %w", err)
	}

	endpoint := fmt.Sprintf("https://%s.r2.cloudflarestorage.com", cfg.AccountID)
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	})

	return &Store{
		client:        client,
		bucket:        cfg.Bucket,
		publicBaseURL: strings.TrimRight(cfg.PublicBaseURL, "/"),
	}, nil
}

// Put uploads r under key, and returns the object's public URL.
func (s *Store) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) (string, error) {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          r,
		ContentLength: aws.Int64(size),
		ContentType:   aws.String(contentType),
	})
	if err != nil {
		return "", fmt.Errorf("storage: putting %s: %w", key, err)
	}
	return s.PublicURL(key), nil
}

// ErrNotFound is returned by Get when key does not exist in the bucket.
var ErrNotFound = errors.New("storage: object not found")

// Get fetches the full contents of key.
func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var nsk *types.NoSuchKey
		if errors.As(err, &nsk) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("storage: getting %s: %w", key, err)
	}
	defer out.Body.Close()

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, out.Body); err != nil {
		return nil, fmt.Errorf("storage: reading %s: %w", key, err)
	}
	return buf.Bytes(), nil
}

// PublicURL returns the URL key is served at, without checking it exists.
func (s *Store) PublicURL(key string) string {
	return s.publicBaseURL + "/" + strings.TrimLeft(key, "/")
}

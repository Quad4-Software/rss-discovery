package blob

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	appcfg "github.com/Quad4-Software/rss-discovery/internal/config"
)

// Store reads and writes fulltext blobs locally and optionally on S3.
type Store struct {
	localDir string
	s3       *s3.Client
	bucket   string
	prefix   string
	enabled  bool
}

func New(cfg appcfg.Config) (*Store, error) {
	local := filepath.Join(cfg.DataDir, "blobs")
	if err := os.MkdirAll(local, 0o755); err != nil {
		return nil, err
	}
	s := &Store{
		localDir: local,
		bucket:   cfg.S3.Bucket,
		prefix:   strings.TrimSuffix(cfg.S3.Prefix, "/") + "/",
		enabled:  cfg.S3.Enabled && cfg.S3.Bucket != "",
	}
	if !s.enabled {
		return s, nil
	}
	var opts []func(*config.LoadOptions) error
	opts = append(opts, config.WithRegion(cfg.S3.Region))
	if cfg.S3.AccessKey != "" && cfg.S3.SecretKey != "" {
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.S3.AccessKey, cfg.S3.SecretKey, ""),
		))
	}
	awsCfg, err := config.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, err
	}
	var s3opts []func(*s3.Options)
	if cfg.S3.Endpoint != "" {
		endpoint := cfg.S3.Endpoint
		forcePath := cfg.S3.ForcePathStyle
		s3opts = append(s3opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = forcePath
		})
	}
	s.s3 = s3.NewFromConfig(awsCfg, s3opts...)
	return s, nil
}

func (s *Store) Put(ctx context.Context, key string, data []byte) (storage string, err error) {
	key = strings.TrimPrefix(key, "/")
	localPath := filepath.Join(s.localDir, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(localPath, data, 0o644); err != nil {
		return "", err
	}
	if !s.enabled {
		return "local", nil
	}
	_, err = s.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.prefix + key),
		Body:   bytes.NewReader(data),
	})
	if err != nil {
		return "local", fmt.Errorf("s3 put (local ok): %w", err)
	}
	return "s3", nil
}

func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	key = strings.TrimPrefix(key, "/")
	localPath := filepath.Join(s.localDir, filepath.FromSlash(key))
	if b, err := os.ReadFile(localPath); err == nil {
		return b, nil
	}
	if !s.enabled {
		return nil, fmt.Errorf("blob not found: %s", key)
	}
	out, err := s.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.prefix + key),
	})
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	b, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, err
	}
	_ = os.MkdirAll(filepath.Dir(localPath), 0o755)
	_ = os.WriteFile(localPath, b, 0o644)
	return b, nil
}

func (s *Store) Delete(ctx context.Context, key string) error {
	key = strings.TrimPrefix(key, "/")
	localPath := filepath.Join(s.localDir, filepath.FromSlash(key))
	_ = os.Remove(localPath)
	if !s.enabled {
		return nil
	}
	_, err := s.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.prefix + key),
	})
	return err
}

func (s *Store) LocalDir() string { return s.localDir }

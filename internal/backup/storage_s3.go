package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Config addresses a bucket on an S3-compatible service.
type S3Config struct {
	// Endpoint is host[:port], optionally with an http:// or https://
	// scheme; without a scheme TLS is used. Empty means AWS S3.
	Endpoint  string
	Region    string // may be empty
	Bucket    string
	Prefix    string // key prefix, ending in "/" when not empty
	AccessKey string
	SecretKey string
	// PathStyle addresses the bucket as host/bucket instead of
	// bucket.host, which MinIO and most self-hosted services need.
	PathStyle bool
}

const (
	envS3Endpoint  = "PULSIX_BACKUP_S3_ENDPOINT"
	envS3Region    = "PULSIX_BACKUP_S3_REGION"
	envS3AccessKey = "PULSIX_BACKUP_S3_ACCESS_KEY"
	envS3SecretKey = "PULSIX_BACKUP_S3_SECRET_KEY"
	envS3PathStyle = "PULSIX_BACKUP_S3_PATH_STYLE"

	defaultS3Endpoint = "s3.amazonaws.com"
	// s3PartSize is the multipart part size of an upload and so the most
	// memory Put holds at once. S3 allows 10 000 parts per object, which
	// caps a backup at about 160 GiB.
	s3PartSize = 16 << 20
	// headerProbeLen is how much of an object List reads to find its header.
	headerProbeLen = int64(maxHeaderLen + len(magic))
)

// S3ConfigFromEnv builds an S3Config from a bucket URL of the form
// s3://bucket/prefix and the PULSIX_BACKUP_S3_* variables returned by getenv.
func S3ConfigFromEnv(getenv func(string) string, bucketURL string) (S3Config, error) {
	u, err := url.Parse(bucketURL)
	if err != nil || u.Scheme != "s3" || u.Host == "" {
		return S3Config{}, fmt.Errorf("backup storage %q: want s3://bucket/prefix", bucketURL)
	}
	cfg := S3Config{
		Endpoint:  getenv(envS3Endpoint),
		Region:    getenv(envS3Region),
		Bucket:    u.Host,
		Prefix:    normalizePrefix(u.Path),
		AccessKey: getenv(envS3AccessKey),
		SecretKey: getenv(envS3SecretKey),
	}
	if cfg.AccessKey == "" {
		return S3Config{}, fmt.Errorf("%s is not set", envS3AccessKey)
	}
	if cfg.SecretKey == "" {
		return S3Config{}, fmt.Errorf("%s is not set", envS3SecretKey)
	}
	if v := getenv(envS3PathStyle); v != "" {
		cfg.PathStyle, err = strconv.ParseBool(v)
		if err != nil {
			return S3Config{}, fmt.Errorf("%s: %q is not a boolean", envS3PathStyle, v)
		}
	}
	return cfg, nil
}

// normalizePrefix turns "/a/b" or "a/b/" into "a/b/", and "/" into "".
func normalizePrefix(p string) string {
	p = strings.Trim(p, "/")
	if p == "" {
		return ""
	}
	return p + "/"
}

type s3Storage struct {
	client *minio.Client
	bucket string
	prefix string
	label  string
}

// NewS3Storage connects to an S3-compatible bucket. It makes no request;
// use Probe to check that the bucket is reachable and writable.
func NewS3Storage(cfg S3Config) (Storage, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("backup storage: bucket is required")
	}
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, errors.New("backup storage: access key and secret key are required")
	}
	endpoint, secure, err := splitEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	lookup := minio.BucketLookupAuto
	if cfg.PathStyle {
		lookup = minio.BucketLookupPath
	}
	client, err := minio.New(endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure:       secure,
		Region:       cfg.Region,
		BucketLookup: lookup,
	})
	if err != nil {
		return nil, fmt.Errorf("backup storage: %w", err)
	}
	prefix := normalizePrefix(cfg.Prefix)
	return &s3Storage{
		client: client,
		bucket: cfg.Bucket,
		prefix: prefix,
		label:  "s3://" + cfg.Bucket + "/" + strings.TrimSuffix(prefix, "/") + " (" + endpoint + ")",
	}, nil
}

// splitEndpoint returns host[:port] and whether to use TLS.
func splitEndpoint(raw string) (host string, secure bool, err error) {
	if raw == "" {
		return defaultS3Endpoint, true, nil
	}
	if !strings.Contains(raw, "://") {
		return raw, true, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", false, fmt.Errorf("backup storage: invalid endpoint %q", raw)
	}
	return u.Host, u.Scheme == "https", nil
}

func (s *s3Storage) Describe() string { return s.label }

func (s *s3Storage) key(name string) string { return s.prefix + name }

func (s *s3Storage) Put(ctx context.Context, name string, r io.Reader) (int64, error) {
	if err := validName(name); err != nil {
		return 0, err
	}
	// Size -1: the length is unknown, so minio-go streams the body as a
	// multipart upload, buffering one part at a time. Without an explicit
	// PartSize it would pick ~537 MiB parts and hold a whole backup in memory.
	info, err := s.client.PutObject(ctx, s.bucket, s.key(name), r, -1, minio.PutObjectOptions{
		ContentType: "application/octet-stream",
		PartSize:    s3PartSize,
	})
	if err != nil {
		return 0, fmt.Errorf("upload %s: %w", name, err)
	}
	return info.Size, nil
}

func (s *s3Storage) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	if err := validName(name); err != nil {
		return nil, err
	}
	obj, err := s.client.GetObject(ctx, s.bucket, s.key(name), minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	// GetObject is lazy; stat surfaces a missing object now.
	if _, err := obj.Stat(); err != nil {
		_ = obj.Close()
		if isNotFound(err) {
			return nil, fmt.Errorf("open %s: %w", name, ErrObjectNotFound)
		}
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	return obj, nil
}

func (s *s3Storage) List(ctx context.Context) ([]Object, error) {
	// An early return stops reading the listing channel; cancelling ends
	// the client's lister goroutine instead of leaving it blocked on a send
	// (final review minor 7).
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var objs []Object
	for info := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: s.prefix}) {
		if info.Err != nil {
			return nil, fmt.Errorf("list backups: %w", info.Err)
		}
		name := strings.TrimPrefix(info.Key, s.prefix)
		if strings.Contains(name, "/") || !strings.HasSuffix(name, objectSuffix) {
			continue
		}
		o, err := s.describe(ctx, name, info.Size)
		if err != nil {
			return nil, err
		}
		if o.Name == "" {
			continue // deleted since the listing
		}
		objs = append(objs, o)
	}
	sortNewestFirst(objs)
	return objs, nil
}

// describe reads the header from the first bytes of an object. A failure to
// read this one object, whether the request or the bytes, is reported as its
// HeaderErr so one bad object cannot fail the listing; only the caller's own
// cancellation is returned as an error. An object deleted since the listing
// comes back as the zero Object.
func (s *s3Storage) describe(ctx context.Context, name string, size int64) (Object, error) {
	o := Object{Name: name, Size: size}
	opts := minio.GetObjectOptions{}
	if size > 0 {
		if err := opts.SetRange(0, min(size, headerProbeLen)-1); err != nil {
			o.HeaderErr = fmt.Errorf("read header of %s: %w", name, err)
			return o, nil
		}
	}
	obj, err := s.client.GetObject(ctx, s.bucket, s.key(name), opts)
	if err != nil {
		o.HeaderErr = fmt.Errorf("read header of %s: %w", name, err)
	} else {
		defer func() { _ = obj.Close() }()
		o = headerObject(name, size, obj)
	}
	if o.HeaderErr == nil {
		return o, nil
	}
	if err := ctx.Err(); err != nil {
		return Object{}, err
	}
	var resp minio.ErrorResponse
	if errors.As(o.HeaderErr, &resp) && resp.Code == minio.NoSuchKey {
		return Object{}, nil
	}
	return o, nil
}

func (s *s3Storage) Delete(ctx context.Context, name string) error {
	if err := validName(name); err != nil {
		return err
	}
	if err := s.client.RemoveObject(ctx, s.bucket, s.key(name), minio.RemoveObjectOptions{}); err != nil && !isNotFound(err) {
		return fmt.Errorf("delete %s: %w", name, err)
	}
	return nil
}

func isNotFound(err error) bool {
	return minio.ToErrorResponse(err).Code == minio.NoSuchKey
}

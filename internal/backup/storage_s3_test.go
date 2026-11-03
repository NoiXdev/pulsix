package backup_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/noixdev/pulsix/internal/backup"
)

// MinIO no longer publishes minio/minio to Docker Hub; this is the last
// Bitnami build of the 2025.7.23 release, still available from the legacy
// repository. Pinned to an exact tag so the test stays reproducible.
const (
	minioImage     = "bitnamilegacy/minio:2025.7.23-debian-12-r5"
	minioUser      = "pulsixtest"
	minioPassword  = "pulsixtest-secret"
	minioTestBkt   = "pulsix-backups"
	minioStartWait = 120 * time.Second
)

var (
	minioOnce     sync.Once
	minioHostPort string
	minioErr      error
	minioBuckets  atomic.Int64
)

// startMinIO runs one MinIO container of its own per test binary, never
// stopped explicitly: like testdb's database, it lives until testcontainers'
// reaper removes it when the process exits. It returns an S3Config for a
// freshly created bucket, and skips when Docker is not available.
func startMinIO(t *testing.T) backup.S3Config {
	t.Helper()
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()

	minioOnce.Do(func() {
		c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image:        minioImage,
				ExposedPorts: []string{"9000/tcp"},
				Env: map[string]string{
					"MINIO_ROOT_USER":     minioUser,
					"MINIO_ROOT_PASSWORD": minioPassword,
				},
				WaitingFor: wait.ForHTTP("/minio/health/ready").WithPort("9000/tcp").WithStartupTimeout(minioStartWait),
			},
			Started: true,
		})
		if err != nil {
			minioErr = err
			return
		}
		minioHostPort, minioErr = c.PortEndpoint(ctx, "9000/tcp", "")
	})
	if minioErr != nil {
		t.Fatalf("start minio: %v", minioErr)
	}

	cfg := backup.S3Config{
		Endpoint:  "http://" + minioHostPort,
		Bucket:    fmt.Sprintf("%s-%d", minioTestBkt, minioBuckets.Add(1)),
		Prefix:    "pulsix/",
		AccessKey: minioUser,
		SecretKey: minioPassword,
		PathStyle: true,
	}
	admin, err := minio.New(minioHostPort, &minio.Options{Creds: credentials.NewStaticV4(minioUser, minioPassword, "")})
	if err != nil {
		t.Fatal(err)
	}
	if err := admin.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	return cfg
}

func TestS3StoragePutListOpenDelete(t *testing.T) {
	cfg := startMinIO(t)
	s, err := backup.NewS3Storage(cfg)
	if err != nil {
		t.Fatal(err)
	}
	exerciseStorage(t, s)

	if d := s.Describe(); !strings.Contains(d, "s3://"+cfg.Bucket+"/pulsix") || strings.Contains(d, cfg.SecretKey) || strings.Contains(d, cfg.AccessKey) {
		t.Errorf("Describe = %q", d)
	}

	if err := backup.Probe(context.Background(), s); err != nil {
		t.Fatalf("Probe: %v", err)
	}
}

func TestS3StorageStreamsLargeUnknownSizeInput(t *testing.T) {
	cfg := startMinIO(t)
	s, err := backup.NewS3Storage(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Larger than one 16 MiB part, read through a reader that
	// hides its length so the upload has to stream.
	const size = 20 << 20
	at := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)
	head := backupFile(t, at, "manual", "")
	r := &opaqueReader{r: strings.NewReader(string(head) + strings.Repeat("z", size))}
	name := backup.ObjectName(at, "manual")
	n, err := s.Put(context.Background(), name, r)
	if err != nil || n != int64(len(head)+size) {
		t.Fatalf("Put = %d, %v", n, err)
	}
	objs, err := s.List(context.Background())
	if err != nil || len(objs) != 1 || objs[0].Size != n || objs[0].HeaderErr != nil {
		t.Fatalf("List = %+v, %v", objs, err)
	}

	// minio-go's default part size for an unknown length is ~537 MiB, which
	// would hold this whole object in one part (a plain PUT, ETag without a
	// suffix). 16 MiB parts make it a multipart upload of two parts, whose
	// ETag ends in "-2". This fails when PartSize is not set.
	admin, err := minio.New(strings.TrimPrefix(cfg.Endpoint, "http://"), &minio.Options{
		Creds: credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := admin.StatObject(context.Background(), cfg.Bucket, cfg.Prefix+name, minio.StatObjectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(strings.Trim(st.ETag, `"`), "-2") {
		t.Fatalf("ETag = %s, want a two-part multipart ETag (-2): the upload was not streamed in 16 MiB parts", st.ETag)
	}
}

// TestS3ListMarksUnreadableObjectDamaged puts a fault-injecting proxy between
// the storage and MinIO that answers every ranged read of one key with an S3
// error. SSE-C would need TLS on MinIO, and the storage takes no custom
// transport, so the proxy (reached through the configurable endpoint, with the
// Host header kept so the request signature still verifies) is the smallest
// way to make one object unreadable for the configured key.
func TestS3ListMarksUnreadableObjectDamaged(t *testing.T) {
	cfg := startMinIO(t)
	upstream, err := url.Parse(cfg.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	// The names are fixed before the proxy starts, so its handler only
	// reads them. The writes below go to MinIO directly, past the proxy.
	at := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)
	goodName := backup.ObjectName(at, "scheduled")
	badName := backup.ObjectName(at.Add(time.Hour), "manual")
	proxy := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.Out.URL.Scheme = upstream.Scheme
		r.Out.URL.Host = upstream.Host
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, badName) {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>AccessDenied</Code><Message>Access Denied.</Message></Error>`))
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	defer srv.Close()

	// Write through MinIO directly, list through the proxy.
	direct, err := backup.NewS3Storage(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Endpoint = srv.URL
	s, err := backup.NewS3Storage(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for name, kind := range map[string]string{goodName: "scheduled", badName: "manual"} {
		if _, err := direct.Put(ctx, name, bytes.NewReader(backupFile(t, at, kind, "payload"))); err != nil {
			t.Fatal(err)
		}
	}

	objs, err := s.List(ctx)
	if err != nil || len(objs) != 2 {
		t.Fatalf("List = %+v, %v; want both objects and no error", objs, err)
	}
	for _, o := range objs {
		switch o.Name {
		case goodName:
			if o.HeaderErr != nil {
				t.Errorf("good object: HeaderErr = %v", o.HeaderErr)
			}
		case badName:
			if o.HeaderErr == nil {
				t.Errorf("unreadable object listed without HeaderErr")
			}
		default:
			t.Errorf("unexpected object %s", o.Name)
		}
	}

	// A cancelled context is still the caller's error, not a damaged object.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.List(cctx); !errors.Is(err, context.Canceled) {
		t.Errorf("List with cancelled context = %v, want context.Canceled", err)
	}
}

type opaqueReader struct{ r *strings.Reader }

func (o *opaqueReader) Read(p []byte) (int, error) { return o.r.Read(p) }

func TestS3ConfigFromEnv(t *testing.T) {
	env := map[string]string{
		"PULSIX_BACKUP_S3_ENDPOINT":   "https://s3.example.com",
		"PULSIX_BACKUP_S3_REGION":     "eu-central-1",
		"PULSIX_BACKUP_S3_ACCESS_KEY": "ak",
		"PULSIX_BACKUP_S3_SECRET_KEY": "sk",
		"PULSIX_BACKUP_S3_PATH_STYLE": "true",
	}
	getenv := func(k string) string { return env[k] }

	cfg, err := backup.S3ConfigFromEnv(getenv, "s3://firma-backups/pulsix")
	if err != nil {
		t.Fatal(err)
	}
	want := backup.S3Config{
		Endpoint: "https://s3.example.com", Region: "eu-central-1", Bucket: "firma-backups", Prefix: "pulsix/",
		AccessKey: "ak", SecretKey: "sk", PathStyle: true,
	}
	if cfg != want {
		t.Fatalf("cfg = %+v, want %+v", cfg, want)
	}

	for _, tc := range []struct{ url, bucket, prefix string }{
		{"s3://b", "b", ""},
		{"s3://b/", "b", ""},
		{"s3://b/a/b/", "b", "a/b/"},
	} {
		cfg, err := backup.S3ConfigFromEnv(getenv, tc.url)
		if err != nil || cfg.Bucket != tc.bucket || cfg.Prefix != tc.prefix {
			t.Errorf("%s -> %+v, %v", tc.url, cfg, err)
		}
	}

	delete(env, "PULSIX_BACKUP_S3_PATH_STYLE")
	if cfg, err := backup.S3ConfigFromEnv(getenv, "s3://b"); err != nil || cfg.PathStyle {
		t.Errorf("unset path style: %+v, %v", cfg, err)
	}

	for _, missing := range []string{"PULSIX_BACKUP_S3_ACCESS_KEY", "PULSIX_BACKUP_S3_SECRET_KEY"} {
		saved := env[missing]
		delete(env, missing)
		if _, err := backup.S3ConfigFromEnv(getenv, "s3://b"); err == nil || !strings.Contains(err.Error(), missing) {
			t.Errorf("without %s: err = %v", missing, err)
		}
		env[missing] = saved
	}

	env["PULSIX_BACKUP_S3_PATH_STYLE"] = "maybe"
	if _, err := backup.S3ConfigFromEnv(getenv, "s3://b"); err == nil || !strings.Contains(err.Error(), "PULSIX_BACKUP_S3_PATH_STYLE") {
		t.Errorf("bad path style: err = %v", err)
	}
	env["PULSIX_BACKUP_S3_PATH_STYLE"] = ""

	for _, bad := range []string{"", "https://b/p", "s3://", "s3:///p", "b/p"} {
		if _, err := backup.S3ConfigFromEnv(getenv, bad); err == nil {
			t.Errorf("bucket URL %q accepted", bad)
		}
	}
}

func TestNewS3StorageValidatesConfig(t *testing.T) {
	good := backup.S3Config{Endpoint: "localhost:9000", Bucket: "b", AccessKey: "a", SecretKey: "s"}
	if _, err := backup.NewS3Storage(good); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*backup.S3Config){
		"no bucket":     func(c *backup.S3Config) { c.Bucket = "" },
		"no access key": func(c *backup.S3Config) { c.AccessKey = "" },
		"no secret key": func(c *backup.S3Config) { c.SecretKey = "" },
		"bad endpoint":  func(c *backup.S3Config) { c.Endpoint = "http://" },
	} {
		cfg := good
		mut(&cfg)
		if _, err := backup.NewS3Storage(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	// DefaultDir is the backup folder inside the manager container.
	DefaultDir = "/var/lib/pulsix/backups"

	// DestinationFolder and DestinationS3 are the two destinations.
	DestinationFolder = "folder"
	DestinationS3     = "s3"

	// settingsKey is the instance_settings row of the backup settings.
	settingsKey = "backup"
	// secretAAD binds the sealed S3 secret to its place.
	secretAAD = "backup.s3.secret"
)

// Settings is how and when the instance backs itself up.
type Settings struct {
	Destination string // DestinationFolder or DestinationS3
	S3          S3Settings
	Enabled     bool   // the daily schedule
	TimeOfDay   string // "HH:MM", local to TimeZone
	TimeZone    string // IANA name
	// Retention applies to scheduled backups only.
	Retention      Retention
	IncludeMetrics bool
}

// S3Settings addresses the bucket. The secret key is write-only: it is
// sealed with the master key when saved and never returned, only whether
// one is stored.
type S3Settings struct {
	Endpoint, Region, Bucket, Prefix, AccessKey string
	SecretKeySet                                bool
	PathStyle                                   bool
}

// DefaultSettings is what an instance starts with: the folder, no
// schedule, 03:00 UTC, 7 daily, 4 weekly and 6 monthly backups kept, with
// metrics.
func DefaultSettings() Settings {
	return Settings{
		Destination: DestinationFolder, TimeOfDay: "03:00", TimeZone: "UTC",
		Retention: Retention{Daily: 7, Weekly: 4, Monthly: 6}, IncludeMetrics: true,
	}
}

// StorageFor returns the storageFor NewService takes: the folder dir, or
// the configured S3-compatible bucket.
func StorageFor(dir string) func(Settings, string) (Storage, error) {
	return func(set Settings, secret string) (Storage, error) {
		switch set.Destination {
		case DestinationFolder:
			return NewDirStorage(dir), nil
		case DestinationS3:
			return NewS3Storage(S3Config{
				Endpoint: set.S3.Endpoint, Region: set.S3.Region, Bucket: set.S3.Bucket, Prefix: set.S3.Prefix,
				AccessKey: set.S3.AccessKey, SecretKey: secret, PathStyle: set.S3.PathStyle,
			})
		default:
			return nil, fmt.Errorf("%w: unknown destination %q", ErrInvalid, set.Destination)
		}
	}
}

// settingsBlob is the stored form: Settings with the secret sealed, and
// when the current schedule was set.
type settingsBlob struct {
	Destination    string        `json:"destination"`
	S3             s3Blob        `json:"s3"`
	Enabled        bool          `json:"enabled"`
	TimeOfDay      string        `json:"time_of_day"`
	TimeZone       string        `json:"time_zone"`
	Retention      retentionBlob `json:"retention"`
	IncludeMetrics bool          `json:"include_metrics"`
	// ScheduledSince is when the schedule was enabled or its time changed.
	// A slot before it is not run, so saving a schedule never starts a
	// backup for a time of day already past.
	ScheduledSince *time.Time `json:"scheduled_since,omitempty"`
}

type s3Blob struct {
	Endpoint     string `json:"endpoint"`
	Region       string `json:"region"`
	Bucket       string `json:"bucket"`
	Prefix       string `json:"prefix"`
	AccessKey    string `json:"access_key"`
	SecretSealed []byte `json:"secret_sealed,omitempty"`
	PathStyle    bool   `json:"path_style"`
}

type retentionBlob struct {
	Daily   int `json:"daily"`
	Weekly  int `json:"weekly"`
	Monthly int `json:"monthly"`
}

func (b settingsBlob) settings() Settings {
	return Settings{
		Destination: b.Destination,
		S3: S3Settings{
			Endpoint: b.S3.Endpoint, Region: b.S3.Region, Bucket: b.S3.Bucket, Prefix: b.S3.Prefix,
			AccessKey: b.S3.AccessKey, SecretKeySet: len(b.S3.SecretSealed) > 0, PathStyle: b.S3.PathStyle,
		},
		Enabled: b.Enabled, TimeOfDay: b.TimeOfDay, TimeZone: b.TimeZone,
		Retention:      Retention{Daily: b.Retention.Daily, Weekly: b.Retention.Weekly, Monthly: b.Retention.Monthly},
		IncludeMetrics: b.IncludeMetrics,
	}
}

func blobOf(in Settings, sealed []byte) settingsBlob {
	return settingsBlob{
		Destination: in.Destination,
		S3: s3Blob{
			Endpoint: in.S3.Endpoint, Region: in.S3.Region, Bucket: in.S3.Bucket, Prefix: in.S3.Prefix,
			AccessKey: in.S3.AccessKey, SecretSealed: sealed, PathStyle: in.S3.PathStyle,
		},
		Enabled: in.Enabled, TimeOfDay: in.TimeOfDay, TimeZone: in.TimeZone,
		Retention:      retentionBlob{Daily: in.Retention.Daily, Weekly: in.Retention.Weekly, Monthly: in.Retention.Monthly},
		IncludeMetrics: in.IncludeMetrics,
	}
}

// Settings returns the backup settings, the defaults when none are saved.
func (s *Service) Settings(ctx context.Context) (Settings, error) {
	b, err := s.loadBlob(ctx)
	if err != nil {
		return Settings{}, err
	}
	return b.settings(), nil
}

// SaveSettings validates and stores in. secretKey replaces the stored S3
// secret; nil keeps it and an empty string removes it. Invalid settings
// return an error wrapping ErrInvalid and store nothing.
func (s *Service) SaveSettings(ctx context.Context, in Settings, secretKey *string) error {
	old, err := s.loadBlob(ctx)
	if err != nil {
		return err
	}
	secretSet := len(old.S3.SecretSealed) > 0
	if secretKey != nil {
		secretSet = *secretKey != ""
	}
	if err := validate(in, secretSet); err != nil {
		return err
	}

	sealed := old.S3.SecretSealed
	if secretKey != nil {
		sealed = nil
		if *secretKey != "" {
			if sealed, err = s.box.SealAAD([]byte(*secretKey), []byte(secretAAD)); err != nil {
				return fmt.Errorf("seal backup storage secret: %w", err)
			}
		}
	}
	b := blobOf(in, sealed)
	if in.Enabled {
		b.ScheduledSince = old.ScheduledSince
		if !old.Enabled || old.TimeOfDay != in.TimeOfDay || old.TimeZone != in.TimeZone || old.ScheduledSince == nil {
			since := s.now().UTC()
			b.ScheduledSince = &since
		}
	}
	data, err := json.Marshal(b)
	if err != nil {
		return fmt.Errorf("marshal backup settings: %w", err)
	}
	if err := s.store.SaveSetting(ctx, settingsKey, data); err != nil {
		return fmt.Errorf("save backup settings: %w", err)
	}
	return nil
}

// Test checks in's destination without saving anything: it writes, lists,
// reads and deletes a probe object. secretKey works as in SaveSettings.
func (s *Service) Test(ctx context.Context, in Settings, secretKey *string) error {
	old, err := s.loadBlob(ctx)
	if err != nil {
		return err
	}
	var secret string
	if secretKey != nil {
		secret = *secretKey
	} else if secret, err = s.openSecret(old); err != nil {
		return err
	}
	if err := validate(in, secret != ""); err != nil {
		return err
	}
	st, err := s.storageFor(in, secret)
	if err != nil {
		return fmt.Errorf("backup destination: %w", err)
	}
	return Probe(ctx, st)
}

// validate checks in; secretSet tells whether an S3 secret is (or will be)
// stored.
func validate(in Settings, secretSet bool) error {
	switch in.Destination {
	case DestinationFolder:
	case DestinationS3:
		if in.S3.Bucket == "" {
			return fmt.Errorf("%w: the S3 bucket is required", ErrInvalid)
		}
		if in.S3.AccessKey == "" || !secretSet {
			return fmt.Errorf("%w: the S3 access key and secret key are required", ErrInvalid)
		}
		if _, _, err := splitEndpoint(in.S3.Endpoint); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}
	default:
		return fmt.Errorf("%w: unknown destination %q", ErrInvalid, in.Destination)
	}
	if _, _, err := parseTimeOfDay(in.TimeOfDay); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if _, err := location(in.TimeZone); err != nil {
		return err
	}
	if r := in.Retention; r.Daily < 0 || r.Weekly < 0 || r.Monthly < 0 {
		return fmt.Errorf("%w: retention counts cannot be negative", ErrInvalid)
	}
	return nil
}

// location loads an IANA zone; "" and "Local" are not one.
func location(name string) (*time.Location, error) {
	if name == "" || name == "Local" {
		return nil, fmt.Errorf("%w: time zone %q is not an IANA time zone", ErrInvalid, name)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("%w: time zone %q is not an IANA time zone", ErrInvalid, name)
	}
	return loc, nil
}

// loadBlob reads the stored settings, or the defaults.
func (s *Service) loadBlob(ctx context.Context) (settingsBlob, error) {
	data, found, err := s.store.LoadSetting(ctx, settingsKey)
	if err != nil {
		return settingsBlob{}, fmt.Errorf("load backup settings: %w", err)
	}
	if !found {
		return blobOf(DefaultSettings(), nil), nil
	}
	var b settingsBlob
	if err := json.Unmarshal(data, &b); err != nil {
		return settingsBlob{}, fmt.Errorf("load backup settings: %w", err)
	}
	return b, nil
}

// openSecret unseals the stored S3 secret; "" when none is stored.
func (s *Service) openSecret(b settingsBlob) (string, error) {
	if len(b.S3.SecretSealed) == 0 {
		return "", nil
	}
	plain, err := s.box.OpenAAD(b.S3.SecretSealed, []byte(secretAAD))
	if err != nil {
		return "", errors.New("open the stored backup storage secret: it does not open with this master key")
	}
	return string(plain), nil
}

// current returns the stored settings and the S3 secret in the clear.
func (s *Service) current(ctx context.Context) (Settings, string, error) {
	b, err := s.loadBlob(ctx)
	if err != nil {
		return Settings{}, "", err
	}
	secret, err := s.openSecret(b)
	if err != nil {
		return Settings{}, "", err
	}
	return b.settings(), secret, nil
}

// Destination opens the configured destination, with the stored S3
// secret. The backup command uses it when no --to is given.
func (s *Service) Destination(ctx context.Context) (Storage, error) {
	set, secret, err := s.current(ctx)
	if err != nil {
		return nil, err
	}
	st, err := s.storageFor(set, secret)
	if err != nil {
		return nil, fmt.Errorf("backup destination: %w", err)
	}
	return st, nil
}

package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/backup"
)

// backupS3Body is the S3-compatible destination in a request. secret_key is
// write-only: PUT and the test route take it, GET never returns it. The
// read-only secret_key_set is accepted and ignored, so the body GET returned
// is a valid PUT body. Every field is optional; an absent one is empty.
type backupS3Body struct {
	Endpoint     string  `json:"endpoint" required:"false" doc:"Base URL of the S3-compatible service, e.g. https://s3.example.com; empty for AWS S3"`
	Region       string  `json:"region" required:"false"`
	Bucket       string  `json:"bucket" required:"false"`
	Prefix       string  `json:"prefix" required:"false" doc:"Key prefix inside the bucket"`
	AccessKey    string  `json:"access_key" required:"false"`
	SecretKey    *string `json:"secret_key,omitempty" writeOnly:"true" doc:"Absent or null keeps the stored secret key; an empty string removes it"`
	SecretKeySet bool    `json:"secret_key_set" required:"false" readOnly:"true" doc:"Ignored in a request"`
	PathStyle    bool    `json:"path_style" required:"false" doc:"Address the bucket as endpoint/bucket instead of bucket.endpoint"`
}

// backupS3View is the destination in GET's response: the same fields, the
// secret key never among them, only whether one is stored.
type backupS3View struct {
	Endpoint     string `json:"endpoint" doc:"Base URL of the S3-compatible service; empty for AWS S3"`
	Region       string `json:"region"`
	Bucket       string `json:"bucket"`
	Prefix       string `json:"prefix" doc:"Key prefix inside the bucket"`
	AccessKey    string `json:"access_key"`
	SecretKeySet bool   `json:"secret_key_set" doc:"Whether a secret key is stored"`
	PathStyle    bool   `json:"path_style" doc:"Address the bucket as endpoint/bucket instead of bucket.endpoint"`
}

type backupRetentionBody struct {
	Daily   int `json:"daily" minimum:"0" doc:"Scheduled backups kept per day, newest days first"`
	Weekly  int `json:"weekly" minimum:"0" doc:"Scheduled backups kept per week"`
	Monthly int `json:"monthly" minimum:"0" doc:"Scheduled backups kept per month"`
}

// backupSettingsBody is the request body of PUT /admin/backups/settings and
// of the destination test.
type backupSettingsBody struct {
	Destination    string              `json:"destination" enum:"folder,s3" doc:"folder is the backup folder inside the manager container"`
	S3             backupS3Body        `json:"s3" required:"false"`
	Enabled        bool                `json:"enabled" doc:"Whether the daily schedule runs"`
	TimeOfDay      string              `json:"time_of_day" example:"03:00" doc:"HH:MM, local to time_zone"`
	TimeZone       string              `json:"time_zone" example:"Europe/Berlin" doc:"IANA time zone name"`
	Retention      backupRetentionBody `json:"retention" doc:"Applies to scheduled backups only; manual backups are never pruned"`
	IncludeMetrics bool                `json:"include_metrics" doc:"Whether the stored check samples are part of a backup"`
}

// backupSettingsView is the response body of GET /admin/backups/settings.
type backupSettingsView struct {
	Destination    string              `json:"destination" enum:"folder,s3" doc:"folder is the backup folder inside the manager container"`
	S3             backupS3View        `json:"s3"`
	Enabled        bool                `json:"enabled" doc:"Whether the daily schedule runs"`
	TimeOfDay      string              `json:"time_of_day" example:"03:00" doc:"HH:MM, local to time_zone"`
	TimeZone       string              `json:"time_zone" example:"Europe/Berlin" doc:"IANA time zone name"`
	Retention      backupRetentionBody `json:"retention" doc:"Applies to scheduled backups only; manual backups are never pruned"`
	IncludeMetrics bool                `json:"include_metrics" doc:"Whether the stored check samples are part of a backup"`
}

func (b backupSettingsBody) settings() backup.Settings {
	return backup.Settings{
		Destination: b.Destination,
		S3: backup.S3Settings{
			Endpoint: b.S3.Endpoint, Region: b.S3.Region, Bucket: b.S3.Bucket, Prefix: b.S3.Prefix,
			AccessKey: b.S3.AccessKey, PathStyle: b.S3.PathStyle,
		},
		Enabled: b.Enabled, TimeOfDay: b.TimeOfDay, TimeZone: b.TimeZone,
		Retention:      backup.Retention{Daily: b.Retention.Daily, Weekly: b.Retention.Weekly, Monthly: b.Retention.Monthly},
		IncludeMetrics: b.IncludeMetrics,
	}
}

func backupSettingsViewOf(s backup.Settings) backupSettingsView {
	return backupSettingsView{
		Destination: s.Destination,
		S3: backupS3View{
			Endpoint: s.S3.Endpoint, Region: s.S3.Region, Bucket: s.S3.Bucket, Prefix: s.S3.Prefix,
			AccessKey: s.S3.AccessKey, SecretKeySet: s.S3.SecretKeySet, PathStyle: s.S3.PathStyle,
		},
		Enabled: s.Enabled, TimeOfDay: s.TimeOfDay, TimeZone: s.TimeZone,
		Retention:      backupRetentionBody{Daily: s.Retention.Daily, Weekly: s.Retention.Weekly, Monthly: s.Retention.Monthly},
		IncludeMetrics: s.IncludeMetrics,
	}
}

type backupSettingsOutput struct {
	Body backupSettingsView
}

type backupSettingsInput struct {
	Body backupSettingsBody
}

// backupItem is one row of the backup list: a stored backup, an attempt that
// left none, or both merged.
type backupItem struct {
	Name          string    `json:"name" doc:"The backup's file or object name, also its identity in the download and delete routes"`
	CreatedAt     time.Time `json:"created_at"`
	Kind          string    `json:"kind" enum:"scheduled,manual"`
	ActorID       *string   `json:"actor_id" format:"uuid" nullable:"true" doc:"Who started a manual backup; null for a scheduled one or without a recorded attempt"`
	ActorName     string    `json:"actor_name" doc:"The actor's display name; empty without an actor or once the account is gone"`
	PulsixVersion string    `json:"pulsix_version" doc:"The Pulsix version that wrote the backup; empty while it has not been written"`
	SizeBytes     int64     `json:"size_bytes"`
	State         string    `json:"state" enum:"running,succeeded,failed,abandoned,damaged" doc:"damaged is a stored file whose header cannot be read"`
	Error         string    `json:"error" doc:"Why a backup failed or is damaged; empty otherwise"`
}

type backupListOutput struct {
	Body struct {
		Items            []backupItem `json:"items" doc:"Newest first"`
		NextRunAt        *time.Time   `json:"next_run_at" nullable:"true" doc:"When the next scheduled backup runs; null while the schedule is off"`
		DestinationLabel string       `json:"destination_label" doc:"Where backups are stored, for display"`
		StorageError     *string      `json:"storage_error" nullable:"true" doc:"Set when the destination could not be listed: items then holds only the recorded attempts"`
	}
}

type backupStartedOutput struct {
	Body struct {
		AttemptID string `json:"attempt_id" format:"uuid" doc:"The backup attempt; it appears in the list as running until it ends"`
	}
}

// backupNameInput carries the {name} path parameter of the download and
// delete routes.
type backupNameInput struct {
	Name string `path:"name" doc:"A backup name as the list returns it"`
}

// validBackupName reports whether name is one a backup could have been
// given. Everything else is refused as unknown before it reaches a
// destination, so a name can never address anything but a backup object.
func validBackupName(name string) bool {
	_, _, ok := backup.ParseObjectName(name)
	return ok
}

// mapBackupErr converts backup.Service sentinels to HTTP errors; anything
// else is logged and replaced with a generic 500.
func mapBackupErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, backup.ErrInvalid):
		return huma.Error422UnprocessableEntity(strings.TrimPrefix(err.Error(), backup.ErrInvalid.Error()+": "))
	case errors.Is(err, backup.ErrBusy):
		return huma.Error409Conflict("another backup is running")
	case errors.Is(err, backup.ErrNotFound), errors.Is(err, backup.ErrObjectNotFound):
		return huma.Error404NotFound("no such backup")
	default:
		slog.ErrorContext(ctx, "unmapped backup error", "err", err)
		return huma.Error500InternalServerError("internal error")
	}
}

// registerBackupRoutes wires the instance backup surface: the settings, the
// list with "Back up now", and delete. Download is a raw chi handler, see
// registerBackupDownloadRoute. Every route is instance-level and gated by
// requireInstanceAdmin, so neither a pending session nor an API token can
// reach one; a backup holds every organization's data.
func registerBackupRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/admin/backups/settings", func(ctx context.Context, _ *struct{}) (*backupSettingsOutput, error) {
		if _, err := requireInstanceAdmin(ctx); err != nil {
			return nil, err
		}
		set, err := deps.Backups.Settings(ctx)
		if err != nil {
			return nil, mapBackupErr(ctx, err)
		}
		return &backupSettingsOutput{Body: backupSettingsViewOf(set)}, nil
	})

	huma.Put(hapi, "/api/v1/admin/backups/settings", func(ctx context.Context, in *backupSettingsInput) (*noContentOutput, error) {
		p, err := requireInstanceAdmin(ctx)
		if err != nil {
			return nil, err
		}
		if err := deps.Backups.SaveSettings(ctx, in.Body.settings(), in.Body.S3.SecretKey); err != nil {
			return nil, mapBackupErr(ctx, err)
		}
		setAuditScope(ctx, nil, p.UserID)
		setAuditObject(ctx, "backup", "updated the backup settings")
		return &noContentOutput{}, nil
	}, withAudit("updateBackupSettings", "backup_settings"))

	huma.Post(hapi, "/api/v1/admin/backups/settings/test", func(ctx context.Context, in *backupSettingsInput) (*noContentOutput, error) {
		if _, err := requireInstanceAdmin(ctx); err != nil {
			return nil, err
		}
		err := deps.Backups.Test(ctx, in.Body.settings(), in.Body.S3.SecretKey)
		switch {
		case err == nil:
			return &noContentOutput{}, nil
		case errors.Is(err, backup.ErrInvalid):
			return nil, mapBackupErr(ctx, err)
		default:
			// The point of the test is to show why the destination does
			// not work: the storage's own error is the detail.
			return nil, huma.Error422UnprocessableEntity(err.Error())
		}
	})

	huma.Get(hapi, "/api/v1/admin/backups", func(ctx context.Context, _ *struct{}) (*backupListOutput, error) {
		if _, err := requireInstanceAdmin(ctx); err != nil {
			return nil, err
		}
		entries, listErr := deps.Backups.List(ctx)
		out := &backupListOutput{}
		if listErr != nil {
			if !errors.Is(listErr, backup.ErrStorageUnavailable) {
				return nil, mapBackupErr(ctx, listErr)
			}
			msg := listErr.Error()
			out.Body.StorageError = &msg
		}
		nextRun, label, err := deps.Backups.Status(ctx)
		if err != nil && listErr == nil {
			return nil, mapBackupErr(ctx, err)
		}
		// With an unusable destination Status fails as List did; the
		// storage error already says so, and the entries are still shown.
		out.Body.NextRunAt, out.Body.DestinationLabel = nextRun, label
		out.Body.Items = make([]backupItem, 0, len(entries))
		for _, e := range entries {
			out.Body.Items = append(out.Body.Items, backupItemOf(e))
		}
		return out, nil
	})

	huma.Post(hapi, "/api/v1/admin/backups", func(ctx context.Context, _ *struct{}) (*backupStartedOutput, error) {
		p, err := requireInstanceAdmin(ctx)
		if err != nil {
			return nil, err
		}
		id, err := deps.Backups.StartManual(ctx, p.UserID)
		if err != nil {
			return nil, mapBackupErr(ctx, err)
		}
		setAuditScope(ctx, nil, p.UserID)
		setAuditObject(ctx, id.String(), "started a backup")
		out := &backupStartedOutput{}
		out.Body.AttemptID = id.String()
		return out, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusAccepted }, withAudit("createBackup", "backup"))

	huma.Delete(hapi, "/api/v1/admin/backups/{name}", func(ctx context.Context, in *backupNameInput) (*noContentOutput, error) {
		p, err := requireInstanceAdmin(ctx)
		if err != nil {
			return nil, err
		}
		if !validBackupName(in.Name) {
			return nil, huma.Error404NotFound("no such backup")
		}
		if err := deps.Backups.Delete(ctx, in.Name); err != nil {
			return nil, mapBackupErr(ctx, err)
		}
		setAuditScope(ctx, nil, p.UserID)
		setAuditObject(ctx, in.Name, "deleted a backup")
		return &noContentOutput{}, nil
	}, withAudit("deleteBackup", "backup"))
}

func backupItemOf(e backup.Entry) backupItem {
	it := backupItem{
		Name: e.Name, CreatedAt: e.CreatedAt, Kind: e.Kind, ActorName: e.ActorName,
		PulsixVersion: e.PulsixVersion, SizeBytes: e.Size, State: e.State, Error: e.Error,
	}
	if e.Actor != nil {
		id := e.Actor.String()
		it.ActorID = &id
	}
	return it
}

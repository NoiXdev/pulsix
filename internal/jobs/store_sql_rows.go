package jobs

// Row mapping of the SQL store: gen rows to jobs, and the JSON columns
// (dry_run, resolved_vault_identities) in both directions.

import (
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// encodeDryRun stores a dry run's options; nil (every other job) is NULL.
func encodeDryRun(d *DryRun) []byte {
	if d == nil {
		return nil
	}
	raw, _ := json.Marshal(d) // a struct of strings and booleans always marshals
	return raw
}

// decodeDryRun reads jobs.dry_run back; NULL, and a value this package
// never writes, read as no record rather than failing the job read.
func decodeDryRun(raw []byte) *DryRun {
	if raw == nil {
		return nil
	}
	var d DryRun
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil
	}
	if d.Tags == nil {
		d.Tags = []string{}
	}
	return &d
}

func mapErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func toJob(r gen.Job) Job {
	var stats map[string]any
	_ = json.Unmarshal(r.Stats, &stats)
	recorded, identities := decodeResolvedVaultIdentities(r.ResolvedVaultIdentities)
	return Job{
		ID: r.ID, OrgID: r.OrgID, ZoneID: r.ZoneID, Kind: Kind(r.Kind),
		TemplateID: db.UUIDPtr(r.TemplateID), HostIDs: r.HostIds, Status: Status(r.Status),
		Attempts: int(r.Attempts), DependsOn: db.UUIDPtr(r.DependsOn), RotationCredentialID: db.UUIDPtr(r.RotationCredentialID),
		WorkerID: db.UUIDPtr(r.WorkerID), CancelStopsPlay: r.CancelStopsPlay != nil && *r.CancelStopsPlay, CheckMode: r.CheckMode,
		DryRun:    decodeDryRun(r.DryRun),
		ClaimedAt: db.TimePtr(r.ClaimedAt), StartedAt: db.TimePtr(r.StartedAt), FinishedAt: db.TimePtr(r.FinishedAt),
		Stats: stats, Error: r.Error, RunGroupID: r.RunGroupID, Trigger: r.Trigger,
		TriggerRef: db.UUIDPtr(r.TriggerRef),
		ActorID:    db.UUIDPtr(r.ActorID), CreatedAt: r.CreatedAt, Payload: json.RawMessage(r.Payload),
		VaultIdentitiesRecorded: recorded, ResolvedVaultIdentities: identities,
	}
}

// decodeResolvedVaultIdentities tells a claim-time record apart from a
// column that was never written (backlog 2.91, migration 00064's own
// comment): raw is nil exactly when the column is SQL NULL — a run still
// queued, one claimed before this migration existed, or a job cancelled
// before it was ever claimed — and a non-nil raw (including the literal
// "[]") is always a real record, even an empty one.
func decodeResolvedVaultIdentities(raw []byte) (bool, []VaultIdentity) {
	if raw == nil {
		return false, nil
	}
	var identities []VaultIdentity
	if err := json.Unmarshal(raw, &identities); err != nil {
		// A row this malformed cannot have come from RecordVaultIdentities,
		// which always writes a JSON array; treat it the same as "never
		// recorded" rather than failing the whole job read over it.
		return false, nil
	}
	return true, identities
}

// toJobDetail is toJob for the GetJob row, whose three display joins add
// the actor's, the worker's and the template's names. Empty when the
// underlying id was NULL or the joined row is gone (a dangling
// template_id reads as no template name; the API shows "(deleted)").
func toJobDetail(r gen.GetJobRow) Job {
	j := toJob(gen.Job{
		ID: r.ID, OrgID: r.OrgID, ZoneID: r.ZoneID, Kind: r.Kind, TemplateID: r.TemplateID,
		HostIds: r.HostIds, Status: r.Status, WorkerID: r.WorkerID, ClaimedAt: r.ClaimedAt,
		StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, Stats: r.Stats, Error: r.Error,
		RunGroupID: r.RunGroupID, Trigger: r.Trigger, TriggerRef: r.TriggerRef, ActorID: r.ActorID, Payload: r.Payload,
		CreatedAt: r.CreatedAt, Attempts: r.Attempts, DependsOn: r.DependsOn, RotationCredentialID: r.RotationCredentialID,
		ResolvedVaultIdentities: r.ResolvedVaultIdentities, CancelStopsPlay: r.CancelStopsPlay, CheckMode: r.CheckMode, DryRun: r.DryRun,
	})
	j.ActorName, j.WorkerName, j.TemplateName = deref(r.ActorName), deref(r.WorkerName), deref(r.TemplateName)
	return j
}

// deref renders a nullable text join column as the empty string.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

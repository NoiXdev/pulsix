// The finish-path rotation finalization's branches (package jobs,
// white-box): recordRotation is unexported, so these drive it directly
// against the real store.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/inventory/render"
)

// recordingFinisher is the RotationFinisher fake the white-box tests use.
type recordingFinisher struct {
	mu    sync.Mutex
	calls []finishedRotationCall
	err   error
}

type finishedRotationCall struct {
	orgID, credentialID, groupID uuid.UUID
	allSucceeded                 bool
	newPrivateKey                credentials.Secret
	newPublicKey                 string
	newFingerprint               string
	newKeyType                   string
}

func (f *recordingFinisher) FinishRotation(_ context.Context, orgID, credentialID, groupID uuid.UUID, allSucceeded bool, newPrivateKey credentials.Secret, newPublicKey, newFingerprint, newKeyType string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, finishedRotationCall{orgID: orgID, credentialID: credentialID, groupID: groupID,
		allSucceeded: allSucceeded, newPrivateKey: newPrivateKey, newPublicKey: newPublicKey,
		newFingerprint: newFingerprint, newKeyType: newKeyType})
	return nil
}

func (f *recordingFinisher) all() []finishedRotationCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// mustSeal seals a payload for the job row the way the service does.
func mustSeal(box Sealer, org, jobID uuid.UUID, payload json.RawMessage) json.RawMessage {
	sealed, err := box.SealAAD(payload, JobAAD(org, jobID))
	if err != nil {
		panic(err)
	}
	return json.RawMessage(`"` + base64.StdEncoding.EncodeToString(sealed) + `"`)
}

func base64Of(raw []byte) string {
	return base64.StdEncoding.EncodeToString(raw)
}

func TestRecordRotationBranches(t *testing.T) {
	d := testdb.Open(t)
	genQ := gen.New(d.Pool())
	orgID := uuid.Must(uuid.NewV7())
	if _, err := genQ.CreateOrganization(context.Background(), gen.CreateOrganizationParams{ID: orgID, Slug: "jobs-record-rotation", Name: "jobs-record-rotation"}); err != nil {
		t.Fatal(err)
	}
	org := orgID
	anyHost := uuid.Must(uuid.NewV7())
	rendered := fakeRenderer{inv: render.Inventory{
		Doc:         map[string]any{"all": map[string]any{}},
		Connections: map[string]render.Connection{"web1": {HostID: anyHost}},
	}}
	q := NewSQLQueue(d, fakeBox{}, rendered, fullProbeCreds{}, fakeTemplates{}, fakeBastions{}, time.Now, rand.Reader)
	q.SetKeyPins(fakePins{})
	finisher := &recordingFinisher{}
	q.SetRotationFinisher(finisher)
	scoped := db.WithOrgScope(context.Background(), org)
	store := NewSQLStore(d)

	// A job whose payload cannot open (ciphertext of another job): the
	// finalize is skipped, the call returns.
	corrupt := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: uuid.Must(uuid.NewV7()), Kind: KindRotateKey,
		Status: StatusQueued, RunGroupID: uuid.Must(uuid.NewV7()), CreatedAt: time.Now()}
	sealed, err := fakeBox{}.SealAAD([]byte(`{}`), JobAAD(org, uuid.Must(uuid.NewV7())))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateJob(scoped, corrupt, json.RawMessage(`"`+base64Of(sealed)+`"`)); err != nil {
		t.Fatal(err)
	}
	q.recordRotation(context.Background(), corrupt.ID, StatusSucceeded)
	if calls := finisher.all(); len(calls) != 0 {
		t.Fatalf("a corrupt payload must finalize nothing: %+v", calls)
	}

	// A well-formed group whose jobs are not all terminal records nothing.
	job := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: uuid.Must(uuid.NewV7()), Kind: KindRotateKey,
		Status: StatusQueued, RunGroupID: uuid.Must(uuid.NewV7()), CreatedAt: time.Now()}
	job.HostIDs = []uuid.UUID{anyHost}
	payload, err := json.Marshal(rotationJobPayload{
		CredentialID: uuid.Must(uuid.NewV7()), Target: rotationTarget{Kind: rotationTargetHost, ID: anyHost},
		NewPublicKey: "pub", NewFingerprint: "SHA256:pending", NewKeyType: "ed25519",
		NewPrivateKey: credentials.Secret{PrivateKey: "pem"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateJob(scoped, job, mustSeal(q.box, org, job.ID, payload)); err != nil {
		t.Fatal(err)
	}
	q.recordRotation(context.Background(), job.ID, StatusSucceeded)
	if calls := finisher.all(); len(calls) != 0 {
		t.Fatalf("an incomplete group must finalize nothing: %+v", calls)
	}

	// A single-job group whose one job is terminal finalizes; claiming
	// first (like the manager does) makes the group's one job terminal
	// once Finish ran — the count query sees the terminal state through
	// the raw pool's bypass.
	job.ID = uuid.Must(uuid.NewV7())
	job.ZoneID = uuid.Must(uuid.NewV7())
	job.RunGroupID = uuid.Must(uuid.NewV7())
	if err := store.CreateJob(scoped, job, mustSeal(q.box, org, job.ID, payload)); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Claim(context.Background(), []uuid.UUID{job.ZoneID}, "v1"); err != nil {
		t.Fatal(err)
	}
	finishErr := q.Finish(context.Background(), job.ID, StatusSucceeded, nil, "")
	var status string
	if err := d.Pool().QueryRow(context.Background(), `SELECT status, error FROM jobs WHERE id = $1`, job.ID).Scan(&status, &status); err != nil {
		t.Fatal(err)
	}
	t.Logf("finish err = %v status = %q", finishErr, status)
	q.recordRotation(context.Background(), job.ID, StatusSucceeded)
	calls := finisher.all()
	for i, c := range calls {
		t.Logf("call %d: %+v", i, c)
	}
	var total, terminal, succeeded int
	if err := d.Pool().QueryRow(context.Background(),
		`SELECT count(*),
			count(*) FILTER (WHERE status IN ('succeeded','failed','cancelled','error')),
			count(*) FILTER (WHERE status = 'succeeded')
		FROM jobs WHERE org_id = $1 AND run_group_id = $2 AND kind = 'rotate_key'`,
		org, job.RunGroupID,
	).Scan(&total, &terminal, &succeeded); err != nil {
		t.Fatal(err)
	}
	t.Logf("counts = %d/%d/%d", total, terminal, succeeded)
	if len(calls) != 1 {
		t.Fatalf("finalize calls = %d, want exactly one", len(calls))
	}
	if !calls[0].allSucceeded {
		t.Fatalf("finalize = %+v", calls[0])
	}
}

// TestRecordRotationMissingJobAndOtherKind covers the early-return
// branches: an unknown finished job and a non-rotation kind.
func TestRecordRotationMissingJobAndOtherKind(t *testing.T) {
	d := testdb.Open(t)
	orgID := uuid.Must(uuid.NewV7())
	genQ := gen.New(d.Pool())
	if _, err := genQ.CreateOrganization(context.Background(), gen.CreateOrganizationParams{ID: orgID, Slug: "jobs-record-rotation-early", Name: "x"}); err != nil {
		t.Fatal(err)
	}
	anyHost := uuid.Must(uuid.NewV7())
	rendered := fakeRenderer{inv: render.Inventory{
		Doc:         map[string]any{"all": map[string]any{}},
		Connections: map[string]render.Connection{"web1": {HostID: anyHost}},
	}}
	q := NewSQLQueue(d, fakeBox{}, rendered, fullProbeCreds{}, fakeTemplates{}, fakeBastions{}, time.Now, rand.Reader)
	q.SetKeyPins(fakePins{})
	finisher := &recordingFinisher{}
	q.SetRotationFinisher(finisher)

	// An unknown job id: the lookup logs and returns.
	q.recordRotation(context.Background(), uuid.Must(uuid.NewV7()), StatusSucceeded)
	if calls := finisher.all(); len(calls) != 0 {
		t.Fatalf("unknown job must finalize nothing: %+v", calls)
	}

	// A non-rotation job records nothing.
	q.recordRotation(context.Background(), uuid.Must(uuid.NewV7()), StatusSucceeded)
	if calls := finisher.all(); len(calls) != 0 {
		t.Fatalf("a non-rotate job must finalize nothing: %+v", calls)
	}
}

// TestRecordRotationFinisherErrorIsLoggedNotFatal pins the bookkeeping
// posture: a failing finalize never breaks the terminal record.
func TestRecordRotationFinisherErrorIsLoggedNotFatal(t *testing.T) {
	d := testdb.Open(t)
	orgID := uuid.Must(uuid.NewV7())
	genQ := gen.New(d.Pool())
	if _, err := genQ.CreateOrganization(context.Background(), gen.CreateOrganizationParams{ID: orgID, Slug: "jobs-record-rotation-err", Name: "x"}); err != nil {
		t.Fatal(err)
	}
	anyHost := uuid.Must(uuid.NewV7())
	rendered := fakeRenderer{inv: render.Inventory{
		Doc:         map[string]any{"all": map[string]any{}},
		Connections: map[string]render.Connection{"web1": {HostID: anyHost}},
	}}
	q := NewSQLQueue(d, fakeBox{}, rendered, fullProbeCreds{}, fakeTemplates{}, fakeBastions{}, time.Now, rand.Reader)
	q.SetKeyPins(fakePins{})
	finisher := &recordingFinisher{err: errors.New("disk full")}
	q.SetRotationFinisher(finisher)

	job := Job{ID: uuid.Must(uuid.NewV7()), OrgID: orgID, ZoneID: uuid.Must(uuid.NewV7()), Kind: KindRotateKey,
		Status: StatusQueued, RunGroupID: uuid.Must(uuid.NewV7()), CreatedAt: time.Now()}
	job.HostIDs = []uuid.UUID{anyHost}
	payload, err := json.Marshal(rotationJobPayload{
		CredentialID: uuid.Must(uuid.NewV7()), Target: rotationTarget{Kind: rotationTargetHost, ID: anyHost},
		NewPublicKey: "pub", NewPrivateKey: credentials.Secret{PrivateKey: "pem"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := NewSQLStore(d).CreateJob(db.WithOrgScope(context.Background(), orgID), job, mustSeal(q.box, orgID, job.ID, payload)); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Claim(context.Background(), []uuid.UUID{job.ZoneID}, "v1"); err != nil {
		t.Fatal(err)
	}
	if err := q.Finish(context.Background(), job.ID, StatusSucceeded, nil, ""); err != nil {
		t.Fatalf("finish: %v", err)
	}
	// The group is complete; the failing finisher is logged, the job stands.
	if calls := finisher.all(); len(calls) != 0 {
		t.Fatalf("a failing finisher must record nothing: %+v", calls)
	}
}

// The world the enrollment claim tests run in (enroll_tokens_claim_store_test.go):
// the real store, hosts, agent registry and activity log.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/enrolltoken"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// agentTokens adapts agents.Service to the EnrollmentTokens port, as the
// production wiring does (internal/app).
type agentTokens struct{ svc *agents.Service }

func (a agentTokens) MintJobTokens(ctx context.Context, orgID, zoneID, jobID uuid.UUID, targets []EnrollTarget, expiresAt time.Time) ([]EnrollToken, error) {
	in := agents.JobTokensInput{ZoneID: zoneID, JobID: jobID, ExpiresAt: expiresAt}
	for _, t := range targets {
		in.Targets = append(in.Targets, agents.JobTokenTarget{HostID: t.HostID, Name: t.Name})
	}
	minted, err := a.svc.MintJobTokens(ctx, orgID, in)
	if err != nil {
		return nil, err
	}
	out := make([]EnrollToken, len(minted))
	for i, m := range minted {
		out[i] = EnrollToken{HostID: m.HostID, Name: m.Name, Token: m.Token}
	}
	return out, nil
}

func (a agentTokens) RevokeJobTokens(ctx context.Context, orgID, jobID uuid.UUID) (int, error) {
	return a.svc.RevokeJobTokens(ctx, orgID, jobID)
}

type enrolPin struct{}

func (enrolPin) CAPin() [32]byte { return [32]byte{7} }

// enrolRenderer renders the world's hosts afresh on every call (the claim
// writes tokens into the document it gets). hook, when set, runs before
// every render: a test moves the job on while a claim builds its payload.
type enrolRenderer struct {
	names map[uuid.UUID]string
	hook  func()
}

func (r enrolRenderer) Snapshot(_ context.Context, _ uuid.UUID, scope render.Scope) (render.Inventory, error) {
	if r.hook != nil {
		r.hook()
	}
	hostvars := map[string]any{}
	conns := map[string]render.Connection{}
	for _, id := range scope.HostIDs {
		name := r.names[id]
		hostvars[name] = map[string]any{"ansible_host": name + ".example.test"}
		conns[name] = render.Connection{HostID: id, BastionSource: "none"}
	}
	return render.Inventory{
		Doc: map[string]any{
			"web":   map[string]any{"vars": map[string]any{"pulsix_agent_enroll_tokens": map[string]any{"web-1": "stale-group-token"}}},
			"all":   map[string]any{},
			"_meta": map[string]any{"hostvars": hostvars},
		},
		Connections: conns,
	}, nil
}

type enrolWorld struct {
	*atomicWorld
	zone      uuid.UUID
	tmpl      uuid.UUID
	hosts     map[string]uuid.UUID // web-1 … web-4; web-3 runs an agent
	agents    *agents.Service
	templates fakeTemplates
}

func newEnrolWorld(t *testing.T, slug string) *enrolWorld {
	t.Helper()
	aw := newAtomicWorld(t, slug)
	w := &enrolWorld{atomicWorld: aw, zone: testZone(t, aw.d, aw.org), tmpl: uuid.Must(uuid.NewV7()), hosts: map[string]uuid.UUID{}}
	hostSvc := hosts.NewService(hosts.NewSQLStore(aw.d), time.Now)
	names := map[uuid.UUID]string{}
	for _, name := range []string{"web-1", "web-2", "web-3", "web-4"} {
		h, err := hostSvc.CreateHost(db.WithOrgScope(context.Background(), aw.org), aw.org, hosts.HostInput{Name: name, ZoneID: w.zone})
		if err != nil {
			t.Fatal(err)
		}
		w.hosts[name], names[h.ID] = h.ID, name
	}
	w.agents = agents.NewService(agents.NewSQLStore(aw.d), agents.WithCA(enrolPin{}), agents.WithProvisioner(enrolProvisioner{}))
	if err := w.agents.Register(context.Background(), agents.Registration{OrgID: aw.org, HostID: w.hosts["web-3"], AgentName: "web-3", Version: "v1",
		Serial: "9" + strings.Repeat("1", 8), NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	w.templates = fakeTemplates{byID: map[uuid.UUID]TemplateRow{
		w.tmpl: {ID: w.tmpl, OrgID: aw.org, PlaybookInline: "- hosts: all\n", Scope: json.RawMessage(`{}`), AgentEnrollment: true},
	}}
	w.q = w.newQueue(w.agents)
	return w
}

// newQueue builds the world's queue over tokens' agent registry.
func (w *enrolWorld) newQueue(tokens *agents.Service) *SQLQueue {
	return w.newHookedQueue(tokens, nil)
}

// newHookedQueue is newQueue whose renderer runs hook before every render.
func (w *enrolWorld) newHookedQueue(tokens *agents.Service, hook func()) *SQLQueue {
	names := map[uuid.UUID]string{}
	for n, id := range w.hosts {
		names[id] = n
	}
	q := NewSQLQueue(w.d, fakeBox{}, enrolRenderer{names: names, hook: hook}, fullProbeCreds{}, w.templates, fakeBastions{}, time.Now, rand.Reader)
	q.SetKeyPins(fakePins{})
	q.SetEnrollment(agentTokens{svc: tokens}, audit.NewRecorder(audit.NewSQLStore(w.d), time.Now))
	return q
}

// enrolProvisioner resolves every bound host (ClaimToken's check).
type enrolProvisioner struct{}

func (enrolProvisioner) GetHost(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (enrolProvisioner) CreateHost(context.Context, uuid.UUID, uuid.UUID, string) (uuid.UUID, error) {
	return uuid.Nil, errors.New("not used")
}
func (enrolProvisioner) SetMonitored(context.Context, uuid.UUID, uuid.UUID) error { return nil }

func (w *enrolWorld) ids(names ...string) []uuid.UUID {
	out := make([]uuid.UUID, len(names))
	for i, n := range names {
		out[i] = w.hosts[n]
		if out[i] == uuid.Nil {
			out[i] = uuid.Must(uuid.NewV7()) // a host the world does not have
		}
	}
	return out
}

// queueRun queues one run job of the world's template on hosts, enrolling
// on list (nil: no list).
func (w *enrolWorld) queueRun(t *testing.T, hostNames []string, list []uuid.UUID, mod func(*Job)) Job {
	t.Helper()
	j := Job{ID: uuid.Must(uuid.NewV7()), Trigger: TriggerManual, OrgID: w.org, ZoneID: w.zone, Kind: KindRun, TemplateID: &w.tmpl,
		HostIDs: w.ids(hostNames...), Status: StatusQueued, RunGroupID: uuid.Must(uuid.NewV7()), CreatedAt: time.Now(), EnrollHostIDs: list}
	if mod != nil {
		mod(&j)
	}
	if err := NewSQLStore(w.d).CreateJob(db.WithOrgScope(context.Background(), w.org), j, nil); err != nil {
		t.Fatal(err)
	}
	return j
}

func (w *enrolWorld) claim(t *testing.T, want Job) *workerpb.Job {
	t.Helper()
	c, err := w.q.Claim(context.Background(), w.org, []uuid.UUID{w.zone}, "v1")
	if err != nil || c == nil {
		t.Fatalf("Claim = %+v, %v; want job %s", c, err, want.ID)
	}
	if c.Job.GetId() != want.ID.String() {
		t.Fatalf("claimed %s, want %s", c.Job.GetId(), want.ID)
	}
	return &c.Job
}

// handedOut reads each host's enrollment variables from a claim.
func handedOut(t *testing.T, job *workerpb.Job) (tokens map[string]string, dicts map[string]any) {
	t.Helper()
	var doc struct {
		Meta struct {
			Hostvars map[string]map[string]any `json:"hostvars"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal([]byte(job.GetInventoryDoc()), &doc); err != nil {
		t.Fatal(err)
	}
	tokens, dicts = map[string]string{}, map[string]any{}
	for host, vars := range doc.Meta.Hostvars {
		if v, ok := vars["pulsix_agent_enroll_token"]; ok {
			tokens[host], _ = v.(string)
		}
		if v, ok := vars["pulsix_agent_enroll_tokens"]; ok {
			dicts[host] = v
		}
	}
	return tokens, dicts
}

// pendingOf is the job's unused tokens' ids.
func (w *enrolWorld) pendingOf(t *testing.T, jobID uuid.UUID) []uuid.UUID {
	t.Helper()
	rows, err := w.d.Pool().Query(context.Background(), `SELECT id FROM agent_enrollments WHERE job_id = $1 AND used_at IS NULL ORDER BY id`, jobID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

// activity is the organization's activity texts (summaries and meta).
func (w *enrolWorld) activity(t *testing.T) []string {
	t.Helper()
	rows, err := w.d.Pool().Query(context.Background(), `SELECT action || ' ' || summary || ' ' || meta::text FROM audit_events WHERE org_id = $1 ORDER BY at, id`, w.org)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// jobRecord is the job's whole stored row as text.
func (w *enrolWorld) jobRecord(t *testing.T, id uuid.UUID) string {
	t.Helper()
	var s string
	if err := w.d.Pool().QueryRow(context.Background(), `SELECT row_to_json(j)::text FROM jobs AS j WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (w *enrolWorld) minted(t *testing.T, id uuid.UUID) int {
	t.Helper()
	j, err := NewSQLStore(w.d).GetJob(db.WithOrgScope(context.Background(), w.org), w.org, id)
	if err != nil {
		t.Fatal(err)
	}
	return j.EnrollTokensMinted
}

func assertNoToken(t *testing.T, where, text string, tokens map[string]string) {
	t.Helper()
	for host, tok := range tokens {
		parsed, err := enrolltoken.ParseAgent(tok)
		if err != nil {
			t.Fatalf("%s's token is no agent token: %v", host, err)
		}
		// The secret part alone, as the token spells it.
		secret := base64.RawURLEncoding.EncodeToString(parsed.Secret)
		if strings.Contains(text, tok) || strings.Contains(text, secret) || len(parsed.Secret) == 0 {
			t.Fatalf("%s carries %s's token: %s", where, host, text)
		}
	}
}

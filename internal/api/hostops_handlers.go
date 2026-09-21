// The connection-test and host-key routes (spec §5, backlog 2.7c): an
// operator triggers a connection test for a managed host (the manager
// enqueues a connection_test job; the outcome flows back through the
// queue's finish path into the host's test history), reads the latest
// result, pins or clears the host key — only ever pinning a fingerprint a
// test actually observed — and bootstraps the host's ansible account with
// an operator-provided one-time login (spec §5, backlog 2.7c part 2): the
// login is sealed into the job's payload, used once by the worker and
// never stored anywhere — not in the audit trail, not in a log line.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/sshconf"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// connectionTestStartOutput is the 202 body of POST .../connection-test:
// the job the test runs as, so the SPA can navigate to the run view.
type connectionTestStartOutput struct {
	Body struct {
		JobID string `json:"job_id"`
	}
}

type hostRouteInput struct {
	Org string `path:"org"`
	ID  string `path:"id"`
}

// hostTestDTO is the wire shape of one connection-test outcome.
type hostTestDTO struct {
	At           time.Time  `json:"at"`
	OK           bool       `json:"ok"`
	HostKey      string     `json:"host_key"`
	HostKeyState string     `json:"host_key_state"`
	Checks       []checkDTO `json:"checks"`
}

type checkDTO struct {
	Name   string `json:"name"`
	Detail string `json:"detail"`
	OK     bool   `json:"ok"`
}

func toHostTestDTO(t hosts.HostTest) hostTestDTO {
	out := hostTestDTO{At: t.At, OK: t.OK, HostKey: t.HostKey, HostKeyState: t.HostKeyState}
	if t.Checks != nil {
		out.Checks = make([]checkDTO, len(t.Checks))
		for i, c := range t.Checks {
			out.Checks[i] = checkDTO{Name: c.Name, Detail: c.Detail, OK: c.OK}
		}
	}
	return out
}

type hostTestOutput struct {
	Body struct {
		Test hostTestDTO `json:"test"`
	}
}

// hostKeyInput is the pin request: the fingerprint a test observed.
type hostKeyInput struct {
	Org  string `path:"org"`
	ID   string `path:"id"`
	Body struct {
		Fingerprint string `json:"fingerprint" minLength:"1" maxLength:"71" pattern:"^SHA256:[A-Za-z0-9+/]{43}$"`
	}
}

// bootstrapInput is the one-time login the bootstrap runs with: the login
// user plus exactly one of password and private key. The body is never
// stored — it is sealed into the job's payload and wiped from the row
// when the job reaches a terminal state.
type bootstrapInput struct {
	Org  string `path:"org"`
	ID   string `path:"id"`
	Body struct {
		User       string `json:"user" minLength:"1" maxLength:"64"`
		Password   string `json:"password,omitempty" maxLength:"1024" doc:"One-time password; exactly one of password and private_key"`
		PrivateKey string `json:"private_key,omitempty" maxLength:"16384" doc:"One-time private key; exactly one of password and private_key"`
	}
}

// bootstrapEnqueuePayload is the sealed kind payload the job row stores:
// which ssh_key credential's public key installs and what logs in this
// once. Never logged, never served.
type bootstrapEnqueuePayload struct {
	CredentialID string             `json:"credential_id"`
	LoginUser    string             `json:"login_user"`
	Login        credentials.Secret `json:"login"`
}

// registerHostopsRoutes wires the host-operation routes under
// /orgs/{org}/hosts/{id}: connection-test start (enqueue) and read, the
// host-key pin/clear, and the bootstrap start. Reads gate on
// inventory.read, writes on inventory.write — the same convention the host
// SSH settings routes use.
func registerHostopsRoutes(hapi huma.API, deps Deps) {
	huma.Post(hapi, "/api/v1/orgs/{org}/hosts/{id}/connection-test", func(ctx context.Context, in *hostRouteInput) (*connectionTestStartOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermInventoryWrite)
		if err != nil {
			return nil, err
		}
		hostID, err := parseUUID(in.ID, "host id")
		if err != nil {
			return nil, err
		}
		actor := scope.UserID
		j, err := deps.Jobs.EnqueueHostJob(ctx, scope.OrgID, jobs.KindConnectionTest, []uuid.UUID{hostID}, nil, jobs.TriggerManual, &actor)
		if err != nil {
			return nil, mapHostopsErr(ctx, err)
		}
		setAuditObject(ctx, j.ID.String(), "started connection test for host "+hostID.String())
		out := &connectionTestStartOutput{}
		out.Body.JobID = j.ID.String()
		return out, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusAccepted }, withAudit("startConnectionTest", "host_connection_test"))

	huma.Post(hapi, "/api/v1/orgs/{org}/hosts/{id}/bootstrap", func(ctx context.Context, in *bootstrapInput) (*connectionTestStartOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermInventoryWrite)
		if err != nil {
			return nil, err
		}
		hostID, err := parseUUID(in.ID, "host id")
		if err != nil {
			return nil, err
		}
		// Exactly one one-time credential; huma bounds the sizes, the
		// exclusive-or is ours.
		if (in.Body.Password == "") == (in.Body.PrivateKey == "") {
			return nil, huma.Error400BadRequest("exactly one of password and private_key is required")
		}
		// The host must exist, be managed, and carry an ssh_key credential
		// of its own: the bootstrap installs THAT credential's public key.
		host, err := deps.Hosts.GetHost(ctx, scope.OrgID, hostID)
		if err != nil {
			return nil, mapHostopsErr(ctx, err)
		}
		if !host.Managed {
			return nil, huma.Error400BadRequest("host is not managed")
		}
		sshSettings, err := deps.SSH.HostSSH(ctx, scope.OrgID, hostID)
		if err != nil {
			if errors.Is(err, sshconf.ErrNotFound) {
				return nil, bootstrapNoCredentialError()
			}
			return nil, mapHostopsErr(ctx, err)
		}
		if sshSettings.CredentialID == nil {
			return nil, bootstrapNoCredentialError()
		}
		cred, err := deps.Credentials.Get(ctx, scope.OrgID, *sshSettings.CredentialID)
		if err != nil {
			return nil, mapCredentialsErr(ctx, err)
		}
		if cred.Kind != credentials.KindSSHKey {
			return nil, huma.Error422UnprocessableEntity("bootstrap installs the public key of an ssh_key credential; create one first")
		}
		secret := credentials.Secret{Password: in.Body.Password}
		if in.Body.PrivateKey != "" {
			secret = credentials.Secret{PrivateKey: in.Body.PrivateKey}
		}
		payload, err := json.Marshal(bootstrapEnqueuePayload{
			CredentialID: cred.ID.String(), LoginUser: in.Body.User, Login: secret,
		})
		if err != nil {
			return nil, err
		}
		actor := scope.UserID
		j, err := deps.Jobs.EnqueueHostJob(ctx, scope.OrgID, jobs.KindBootstrap, []uuid.UUID{hostID}, payload, jobs.TriggerManual, &actor)
		if err != nil {
			return nil, mapHostopsErr(ctx, err)
		}
		// The audit summary names the host — never the body's one-time
		// login.
		setAuditObject(ctx, j.ID.String(), "started bootstrap for host "+hostID.String())
		out := &connectionTestStartOutput{}
		out.Body.JobID = j.ID.String()
		return out, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusAccepted }, withAudit("bootstrapHost", "host"))

	huma.Get(hapi, "/api/v1/orgs/{org}/hosts/{id}/connection-test", func(ctx context.Context, in *hostRouteInput) (*hostTestOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermInventoryRead)
		if err != nil {
			return nil, err
		}
		hostID, err := parseUUID(in.ID, "host id")
		if err != nil {
			return nil, err
		}
		test, err := deps.Hosts.LatestTest(ctx, scope.OrgID, hostID)
		if err != nil {
			return nil, mapHostopsErr(ctx, err)
		}
		out := &hostTestOutput{}
		out.Body.Test = toHostTestDTO(test)
		return out, nil
	})

	huma.Put(hapi, "/api/v1/orgs/{org}/hosts/{id}/host-key", func(ctx context.Context, in *hostKeyInput) (*okOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermInventoryWrite)
		if err != nil {
			return nil, err
		}
		hostID, err := parseUUID(in.ID, "host id")
		if err != nil {
			return nil, err
		}
		// The host is resolved first: a host of another organization is as
		// invisible as one that does not exist (404), before anything about
		// its test history is asked.
		if _, err := deps.Hosts.GetHost(ctx, scope.OrgID, hostID); err != nil {
			return nil, mapHostopsErr(ctx, err)
		}
		// Only pin what you saw: the pin must equal the fingerprint the
		// latest test observed. A pin made from a value the manager never
		// saw would blind trust-on-first-use — the mismatch case this
		// feature exists to catch.
		test, err := deps.Hosts.LatestTest(ctx, scope.OrgID, hostID)
		if err != nil {
			if errors.Is(err, hosts.ErrNotFound) {
				return nil, huma.Error422UnprocessableEntity("the host has no connection test yet; pin the fingerprint a test observed")
			}
			return nil, mapHostopsErr(ctx, err)
		}
		if test.HostKey == "" || test.HostKey != in.Body.Fingerprint {
			return nil, huma.Error422UnprocessableEntity("the fingerprint does not match the latest connection test result; only the observed key can be pinned")
		}
		// The pin stores the observed key itself (the worker's known_hosts
		// enforces it on every connection). A test recorded before full
		// keys were kept cannot provide it: a fresh test must observe it.
		if test.HostPublicKey == "" {
			return nil, huma.Error422UnprocessableEntity("the latest connection test did not record the full host key; run a new connection test and pin its result")
		}
		if err := deps.Hosts.SetHostKey(ctx, scope.OrgID, hostID, in.Body.Fingerprint, test.HostPublicKey); err != nil {
			return nil, mapHostsErr(ctx, err)
		}
		setAuditObject(ctx, hostID.String(), "pinned host key")
		return &okOutput{}, nil
	}, withAudit("setHostKey", "host"))

	huma.Delete(hapi, "/api/v1/orgs/{org}/hosts/{id}/host-key", func(ctx context.Context, in *hostRouteInput) (*okOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermInventoryWrite)
		if err != nil {
			return nil, err
		}
		hostID, err := parseUUID(in.ID, "host id")
		if err != nil {
			return nil, err
		}
		if err := deps.Hosts.ClearHostKey(ctx, scope.OrgID, hostID); err != nil {
			return nil, mapHostsErr(ctx, err)
		}
		setAuditObject(ctx, hostID.String(), "cleared host key pin")
		return &okOutput{}, nil
	}, withAudit("clearHostKey", "host"))
}

// mapHostopsErr converts the jobs and hosts errors the two write routes
// can produce. A host that does not exist — or exists in another
// organization — is 404, exactly like every other org-scoped resource; a
// malformed input is 400; anything else is logged server-side and 500.
func mapHostopsErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, jobs.ErrNotFound) || errors.Is(err, hosts.ErrNotFound):
		return huma.Error404NotFound("not found")
	case errors.Is(err, jobs.ErrInvalidInput) || errors.Is(err, hosts.ErrInvalidInput):
		return huma.Error400BadRequest(err.Error())
	case errors.Is(err, jobs.ErrHostKeyChanged):
		return huma.Error409Conflict(err.Error())
	default:
		slog.ErrorContext(ctx, "unmapped hostops error", "err", err)
		return huma.Error500InternalServerError("internal error")
	}
}

// bootstrapNoCredentialError is the 422 a host without an ssh_key
// credential of its own gets: the bootstrap's whole purpose is installing
// that credential's public key, so there is nothing to bootstrap with.
func bootstrapNoCredentialError() error {
	return huma.Error422UnprocessableEntity("bootstrap installs the public key of an ssh_key credential; create one first")
}

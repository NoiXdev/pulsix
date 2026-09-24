/**
 * Shared constants for the Playwright suite: the ports the test stack
 * (deploy/compose/docker-compose.test.yml) publishes, the manager's own
 * configuration, and the fixture data the journeys build on top of each
 * other with. Every spec, playwright.config.ts and global-setup.ts import
 * from here instead of repeating literals, so the port/URL story only
 * needs to agree in one place.
 */
import path from 'node:path';

// --- Test stack ports (must match deploy/compose/docker-compose.test.yml) ---
export const DB_PORT = 55432;
export const MAILPIT_SMTP_PORT = 45025;
export const MAILPIT_HTTP_PORT = 48025;

// The manager's gRPC surface (PULSIX_GRPC_LISTEN), which workers and
// agents dial. Off by default in production; the e2e manager enables it
// so agent.spec can enrol a real agent against this stack.
export const AGENT_GRPC_PORT = 48444;

// --- The manager under test ---
export const MANAGER_PORT = 48090;
// "localhost", not "127.0.0.1": go-webauthn (internal/auth's passkey
// service) rejects an RP ID that is an IP address, and the RP ID is
// derived from PULSIX_PUBLIC_URL's hostname (see internal/config.RPID) —
// passkey.spec's whole premise breaks on an IP-literal origin.
export const BASE_URL = `http://localhost:${MANAGER_PORT}`;
export const DATABASE_URL = `postgres://pulsix:pulsix@localhost:${DB_PORT}/pulsix?sslmode=disable`;
// 64 hex characters, matching the shape internal/config.Load requires and
// the same "ab" x32 convention internal/app's own tests use for a fixture
// key that is obviously not a real secret.
export const MASTER_KEY = 'ab'.repeat(32);

export const MAILPIT_HTTP_URL = `http://localhost:${MAILPIT_HTTP_PORT}`;

// The agent binary global-setup builds for agent.spec (see e2e/agent.ts).
export const AGENT_BINARY_PATH = path.resolve(import.meta.dirname, '../.tmp/pulsix-agent-e2e');

export const COMPOSE_FILE = path.resolve(import.meta.dirname, '../../deploy/compose/docker-compose.test.yml');

// --- Fixture data setup.spec creates and every later spec reuses ---
export const ADMIN = {
  name: 'Ada Admin',
  email: 'admin@pulsix.test',
  // Meets passwordSchema (>= 12 chars); has no bearing on any assertion.
  password: 'Sup3rSecret-Pw!',
};
export const ORG = { slug: 'acme', name: 'Acme Hosting' };

// SMTP delivered straight to the mailpit container started above — no
// STARTTLS, no auth, matching what internal/mail.deliver does when
// Username/StartTLS are left unset.
export const SMTP = {
  host: 'localhost',
  port: MAILPIT_SMTP_PORT,
  from: 'pulsix@acme.example.test',
};

// --- Cross-spec state (written by setup.spec, read by everything after) ---
const stateDir = path.resolve(import.meta.dirname, '../.tmp');
/** The admin's authenticated + TOTP-enrolled browser storage state
 * (cookies), first captured at the end of setup.spec. Every later spec
 * that needs to already be signed in as the admin loads this instead of
 * logging in from scratch. It is a relay baton, not a frozen fixture:
 * logging out is a real server-side session revocation (see
 * login.spec.ts's own top-of-file note), so any spec that logs out —
 * currently login.spec and passkey.spec — re-saves its own freshly
 * authenticated storage state back to this same path once it has signed
 * back in, so the next spec in the dependency chain (playwright.config.ts)
 * always loads a session that is actually still alive. hosts/members/
 * activity/tenancy never log out, so they neither need nor do this. */
export const ADMIN_STORAGE_STATE = path.join(stateDir, 'admin-storage-state.json');
/** The admin's TOTP secret, captured from the enrolment screen during
 * setup.spec — login.spec needs it to compute a real 6-digit code. */
export const TOTP_STATE_PATH = path.join(stateDir, 'totp-secret.json');
/** The last TOTP time step this suite has already asked the server to
 * accept for the admin's secret — see otp.ts's nextTotpCode, which reads
 * and updates this file so two submissions computed independently, close
 * together in wall-clock time, never reserve the same step. */
export const TOTP_STEP_STATE_PATH = path.join(stateDir, 'totp-step.json');
export const MANAGER_LOG_PATH = path.join(stateDir, 'manager.log');
/** The operator members.spec invites enrols TOTP on acceptance (a second
 * factor is mandatory); its secret and step state let the spec's second
 * test sign that account back in. */
export const OPERATOR_TOTP_STATE_PATH = path.join(stateDir, 'operator-totp-secret.json');
export const OPERATOR_TOTP_STEP_STATE_PATH = path.join(stateDir, 'operator-totp-step.json');
/** tenancy.spec's second-organization user, enrolled the same way. */
export const TENANT_TOTP_STATE_PATH = path.join(stateDir, 'tenant-totp-secret.json');
export const TENANT_TOTP_STEP_STATE_PATH = path.join(stateDir, 'tenant-totp-step.json');

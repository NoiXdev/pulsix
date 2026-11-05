/**
 * Shared constants for the Playwright suite: the ports the test stack
 * (deploy/compose/docker-compose.test.yml) publishes, the manager's own
 * configuration, and the fixture data the journeys build on top of each
 * other with. Every spec, playwright.config.ts and global-setup.ts import
 * from here instead of repeating literals, so the port/URL story only
 * needs to agree in one place.
 */
import { hkdfSync } from 'node:crypto';
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

/** setupToken mirrors internal/setup.Token: HKDF-SHA256 of the master key
 * (no salt, info "pulsix setup token v1"), 10 bytes, RFC 4648 base32
 * without padding, grouped XXXX-XXXX-XXXX-XXXX. internal/setup's
 * TestTokenKnownValue pins the value for this very key. */
function setupToken(masterKeyHex: string): string {
  const okm = new Uint8Array(hkdfSync('sha256', Buffer.from(masterKeyHex, 'hex'), Buffer.alloc(0), 'pulsix setup token v1', 10));
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
  let bits = 0;
  let value = 0;
  let out = '';
  for (const byte of okm) {
    value = (value << 8) | byte;
    bits += 8;
    while (bits >= 5) {
      out += alphabet[(value >>> (bits - 5)) & 31];
      bits -= 5;
    }
  }
  return out.match(/.{4}/g)!.join('-');
}
/** The token the setup wizard asks for on this stack (the manager also
 * logs it at start, to MANAGER_LOG_PATH). */
export const SETUP_TOKEN = setupToken(MASTER_KEY);

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
/** The operator members.spec invites into ORG. accounts.spec later sets a
 * temporary password for this account, and the person replaces it with
 * `changedPassword`. */
export const OPERATOR = {
  email: 'ops@example.test',
  name: 'Otto Operator',
  password: 'operator-password',
  changedPassword: 'operator-own-password',
  // accounts.spec's reset-link journey sets this one through a mailed link.
  resetPassword: 'operator-reset-password',
};

/** The person accounts.spec's factor-reset journey invites into ORG with a
 * fresh account of their own, so the operator's password and factor stay as
 * the specs after it expect them. They enrol a first authenticator on
 * accepting, an administrator resets it, and they enrol a second one
 * (`FACTOR_PERSON_TOTP_STATE_PATH` holds whichever is current). */
export const FACTOR_PERSON = {
  email: 'reset-person@example.test',
  name: 'Rita Reset',
  password: 'reset-person-password',
  changedPassword: 'reset-person-own-password',
};

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
export const FACTOR_PERSON_TOTP_STATE_PATH = path.join(stateDir, 'factor-person-totp-secret.json');
export const FACTOR_PERSON_TOTP_STEP_STATE_PATH = path.join(stateDir, 'factor-person-totp-step.json');

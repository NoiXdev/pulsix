/**
 * generateTotpCode computes a real 6-digit TOTP code from a secret the app
 * displays on screen (see AccountSecurityPage's enrolment step) — the
 * suite never types a hardcoded code, since the code that would validate
 * is only known at the moment the secret exists.
 *
 * otplib's `generateSync` defaults (SHA1, 6 digits, 30s step — RFC 6238 /
 * Google Authenticator's own defaults) match internal/auth/totp.go's
 * server-side generation exactly (see its own totp.Generate/
 * GenerateCodeCustom calls, using otp.AlgorithmSHA1/otp.DigitsSix and a
 * 30s totpStep), so no option needs overriding here.
 */
import { generateSync } from 'otplib';

export function generateTotpCode(secret: string): string {
  return generateSync({ secret });
}

/**
 * waitForFreshTotpWindow blocks until a brand new 30-second TOTP window has
 * just started, so the *next* generateTotpCode call is guaranteed to
 * produce a code for a step later than any step already consumed so far.
 *
 * internal/auth/totp.go's verifyAndConsume rejects a code whose time step
 * is not strictly newer than the last step it already accepted for that
 * account (anti-replay — see its own comment) — not "older than 30s ago",
 * but "the same or an earlier 30s window". Two TOTP submissions for the
 * same account close together in wall-clock time (e.g. setup.spec's own
 * enrolment confirm, moments before login.spec's first real sign-in) can
 * easily land in the very same window, and a correct, honestly-computed
 * code is then genuinely rejected as a replay — not a bug in the app, but
 * a real hazard for a test suite that reuses one TOTP secret across
 * specs. Waiting for a fresh window before the next submission removes the
 * hazard without weakening what the assertion proves.
 */
export async function waitForFreshTotpWindow(periodMs = 30_000, bufferMs = 1_000): Promise<void> {
  const msIntoWindow = Date.now() % periodMs;
  const msUntilNextWindow = periodMs - msIntoWindow;
  await new Promise((resolve) => setTimeout(resolve, msUntilNextWindow + bufferMs));
}

/**
 * nextTotpCode computes a real 6-digit TOTP code from a secret the app
 * displays on screen (see AccountSecurityPage's enrolment step) — the
 * suite never types a hardcoded code, since the code that would validate
 * is only known at the moment the secret exists.
 *
 * otplib's `generateSync` defaults (SHA1, 6 digits, 30s step — RFC 6238 /
 * Google Authenticator's own defaults) match internal/auth/totp.go's
 * server-side generation exactly (see its own totp.Generate/
 * GenerateCodeCustom calls, using otp.AlgorithmSHA1/otp.DigitsSix and a
 * 30s totpStep), so no option needs overriding besides `epoch` below.
 */
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { generateSync } from 'otplib';

const TOTP_PERIOD_SECONDS = 30;

function currentTotpStep(): number {
  return Math.floor(Date.now() / 1000 / TOTP_PERIOD_SECONDS);
}

/**
 * nextTotpCode replaces the old waitForFreshTotpWindow() + generateTotpCode()
 * pair (which slept into a fresh 30-second window — roughly sixty of the
 * suite's ninety seconds) with a code computed for a time step that is
 * valid *right now*, no waiting required.
 *
 * internal/auth/totp.go's verifyAndConsume accepts the current time step
 * and one step either side (totpSkewSteps = 1) — so a code computed for
 * the *next* step (`epoch` one period ahead of now, via otplib's own
 * option for it) validates immediately, while still recording a step
 * newer than the one a previous submission for this account consumed,
 * because the server always resolves "next" as "one more than the
 * previous window", regardless of which spec or test made that previous
 * submission.
 *
 * The one thing a single fixed "+1" offset cannot guarantee on its own:
 * two submissions for the same secret, computed independently, moments
 * apart, within the very same real 30-second window would both land on
 * that window's step + 1 — an actual replay, not a false one. `statePath`
 * is a small JSON file (one per TOTP secret, alongside the secret itself
 * in web/.tmp) recording the last step this suite has already asked the
 * server to accept, so every call here reserves a step strictly after the
 * last one *this suite* used — real time only needs to have moved forward
 * at all since the previous reservation (any two real Playwright actions
 * apart is enough), not a whole 30-second window. That is exactly the
 * same "genuinely newer, but never further ahead than the skew allows"
 * property waitForFreshTotpWindow used to buy by sleeping for it.
 *
 * A reserved step can never be handed to the server more than one step
 * ahead of its own real clock (totpSkewSteps = 1) — if reservations ever
 * ran far enough ahead of wall-clock time to break that (they don't, in
 * this suite: at most three submissions total, each separated by several
 * real Playwright actions), this waits for real time to catch up rather
 * than submit a code the server would reject as not-yet-valid. That
 * fallback is defensive, not the common path.
 */
export async function nextTotpCode(secret: string, statePath: string): Promise<string> {
  let last: number | undefined;
  if (existsSync(statePath)) {
    last = (JSON.parse(readFileSync(statePath, 'utf8')) as { lastStep: number }).lastStep;
  }
  const step = last === undefined ? currentTotpStep() : Math.max(currentTotpStep(), last + 1);
  while (step > currentTotpStep() + 1) {
    await new Promise((resolve) => setTimeout(resolve, 250));
  }
  mkdirSync(path.dirname(statePath), { recursive: true });
  writeFileSync(statePath, JSON.stringify({ lastStep: step }));
  return generateSync({ secret, epoch: step * TOTP_PERIOD_SECONDS });
}

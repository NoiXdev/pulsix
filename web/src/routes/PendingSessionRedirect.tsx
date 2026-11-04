import { Navigate } from '@tanstack/react-router';
import type { SessionState } from '../lib/sessionState.ts';

/**
 * PendingSessionRedirect sends a session that may not use the app yet to
 * the one place it can make progress: second-factor enrolment when its
 * account has none yet, the TOTP step of /login when it has one to verify,
 * and the change-password screen when its account must replace a password
 * an administrator set. Every route-connected layout renders it instead of
 * its own content for such a session — the API would refuse that content
 * anyway.
 */
export function PendingSessionRedirect({ state }: { state: Exclude<SessionState, 'complete'> }) {
  if (state === 'enrol') return <Navigate to="/account/enrol" />;
  if (state === 'password') return <Navigate to="/account/password" />;
  return <Navigate to="/login" search={{ step: 'totp' }} />;
}

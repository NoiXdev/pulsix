/**
 * MIN_PASSWORD_LENGTH is the product's one password floor, in characters
 * (backlog 7.74): the setup wizard, invitation acceptance, a reset by link
 * and a change of one's own password all hold it. The server's
 * auth.MinPasswordLength is the authority; this mirror lets a form say so
 * before the round trip.
 */
export const MIN_PASSWORD_LENGTH = 12;

/** PASSWORD_TOO_SHORT is the detail of the server's 422 for a new password
 * under the floor (POST /auth/password and the reset by link). Only it
 * means the length rule: any other 422 carries its own message. */
export const PASSWORD_TOO_SHORT = 'password_too_short';

/** NewPasswordProblem names what is wrong with a new password typed twice. */
export type NewPasswordProblem = 'tooShort' | 'mismatch';

/** passwordLength counts characters the way the server does (code points),
 * not UTF-16 units: an emoji is one character, not two. */
export function passwordLength(password: string): number {
  return [...password].length;
}

/** newPasswordProblem checks a new password and its repetition, the floor
 * first: a mismatch of two passwords that are too short anyway is not the
 * news. Null when both are fine. */
export function newPasswordProblem(next: string, repeat: string): NewPasswordProblem | null {
  if (passwordLength(next) < MIN_PASSWORD_LENGTH) return 'tooShort';
  if (next !== repeat) return 'mismatch';
  return null;
}

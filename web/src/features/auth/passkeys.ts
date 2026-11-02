import {
  beginPasskeyLogin,
  beginPasskeyRegistration,
  finishPasskeyLogin,
  finishPasskeyRegistration,
} from '../../api/hooks/usePasskeys.ts';
import { en } from '../../i18n/en/index.ts';
import type { Messages } from '../../i18n/types.ts';
import {
  decodeCreationOptions,
  decodeRequestOptions,
  encodeAssertionCredential,
  encodeAttestationCredential,
  type CreationOptionsJSON,
  type RequestOptionsJSON,
} from './webauthnCodec.ts';

/**
 * PasskeyCeremonyBegin is the shape POST …/register/begin and
 * …/login/begin actually respond with for their `options` field: go-
 * webauthn's protocol.CredentialCreation/CredentialAssertion — what
 * internal/auth/passkey.go's BeginRegistration/BeginLogin json.Marshal —
 * wrap the real options in a top-level `publicKey` key (matching the
 * WebAuthn spec's own `navigator.credentials.create({publicKey: ...})`
 * call shape), not the flat object CreationOptionsJSON/RequestOptionsJSON
 * describe on their own. Found by passkey.spec.ts's CDP-virtual-
 * authenticator run: without unwrapping `.publicKey` here,
 * `decodeCreationOptions`/`decodeRequestOptions` received `{challenge:
 * undefined, ...}` and base64urlToBuffer threw "Cannot read properties of
 * undefined (reading 'replace')" on every real ceremony — invisible to
 * webauthnCodec.ts's own unit tests, which fixture the *already-unwrapped*
 * flat shape, and to this file, deliberately left untested per
 * task-15a-report.md pending exactly this kind of end-to-end coverage.
 */
interface PasskeyCeremonyBegin<T> {
  publicKey: T;
}

/**
 * registerPasskey drives the full "add a passkey" ceremony end to end:
 * fetch options from the API, hand them to the browser's own
 * navigator.credentials.create, and post the signed result back. The
 * base64url <-> ArrayBuffer conversion lives entirely in webauthnCodec.ts
 * (unit-tested there); this function is the imperative glue around it and
 * around the browser API, which cannot be meaningfully unit-tested without
 * a real authenticator — that coverage comes from the Playwright
 * passkey.spec.ts in the second half of this task, using a CDP virtual
 * authenticator.
 */
export async function registerPasskey(name: string, t: Messages = en): Promise<void> {
  const begin = await beginPasskeyRegistration();
  const wrapped = begin.options as unknown as PasskeyCeremonyBegin<CreationOptionsJSON>;
  const options = decodeCreationOptions(wrapped.publicKey);

  const credential = await navigator.credentials.create({ publicKey: options });
  if (!credential) throw new Error(t.auth.passkey.registrationCancelled);

  await finishPasskeyRegistration({
    ceremony_id: begin.ceremony_id,
    name,
    response: encodeAttestationCredential(credential as PublicKeyCredential),
  });
}

/**
 * loginWithPasskey drives discoverable passkey login: no email or password
 * up front, matching the "Use a passkey" button on /login — the
 * authenticator itself picks which credential to present. On success, the
 * response is a signed cookie the manager sets directly, so this function
 * returns void; the caller re-reads /auth/me to pick up the fresh session.
 */
export async function loginWithPasskey(t: Messages = en): Promise<void> {
  const begin = await beginPasskeyLogin();
  const wrapped = begin.options as unknown as PasskeyCeremonyBegin<RequestOptionsJSON>;
  const options = decodeRequestOptions(wrapped.publicKey);

  const credential = await navigator.credentials.get({ publicKey: options });
  if (!credential) throw new Error(t.auth.passkey.loginCancelled);

  await finishPasskeyLogin({
    ceremony_id: begin.ceremony_id,
    response: encodeAssertionCredential(credential as PublicKeyCredential),
  });
}

/** passkeysSupported reports whether the current browser can even attempt
 * a passkey ceremony, so the "Use a passkey" button can hide itself
 * instead of failing on click. */
export function passkeysSupported(): boolean {
  return typeof window !== 'undefined' && !!window.PublicKeyCredential && !!navigator.credentials;
}

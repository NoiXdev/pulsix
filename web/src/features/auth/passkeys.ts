import {
  beginPasskeyLogin,
  beginPasskeyRegistration,
  finishPasskeyLogin,
  finishPasskeyRegistration,
} from '../../api/hooks/usePasskeys.ts';
import {
  decodeCreationOptions,
  decodeRequestOptions,
  encodeAssertionCredential,
  encodeAttestationCredential,
  type CreationOptionsJSON,
  type RequestOptionsJSON,
} from './webauthnCodec.ts';

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
export async function registerPasskey(name: string): Promise<void> {
  const begin = await beginPasskeyRegistration();
  const options = decodeCreationOptions(begin.options as unknown as CreationOptionsJSON);

  const credential = await navigator.credentials.create({ publicKey: options });
  if (!credential) throw new Error('passkey registration was cancelled');

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
export async function loginWithPasskey(): Promise<void> {
  const begin = await beginPasskeyLogin();
  const options = decodeRequestOptions(begin.options as unknown as RequestOptionsJSON);

  const credential = await navigator.credentials.get({ publicKey: options });
  if (!credential) throw new Error('passkey login was cancelled');

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

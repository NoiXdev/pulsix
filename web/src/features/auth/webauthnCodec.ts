/**
 * webauthnCodec converts between the base64url strings the passkey API's
 * ceremony options and responses carry over JSON and the ArrayBuffers the
 * browser's `navigator.credentials` API actually deals in. WebAuthn's
 * options and results carry binary values (a challenge, credential ids,
 * signatures, ...); JSON has no binary type, so the manager encodes every
 * one of those fields as base64url text on the wire instead
 * (…/register/begin, …/login/begin responses; …/register/finish,
 * …/login/finish request bodies) — this module is the one place that
 * crosses that boundary, kept separate from the components that drive the
 * ceremony so it can be unit-tested without a real authenticator or a
 * jsdom WebAuthn polyfill.
 */

/** base64urlToBuffer decodes a base64url string (no padding, `-`/`_`
 * instead of `+`/`/`) into the ArrayBuffer navigator.credentials expects
 * for fields like `challenge` and a credential's `id`. */
export function base64urlToBuffer(value: string): ArrayBuffer {
  const base64 = value.replace(/-/g, '+').replace(/_/g, '/');
  const padded = base64 + '='.repeat((4 - (base64.length % 4)) % 4);
  const binary = atob(padded);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes.buffer;
}

/** bufferToBase64url is base64urlToBuffer's inverse: it encodes a
 * credential response's binary fields back to the base64url text the
 * …/finish endpoints expect in their JSON request body. */
export function bufferToBase64url(buffer: ArrayBuffer | Uint8Array): string {
  const bytes = buffer instanceof Uint8Array ? buffer : new Uint8Array(buffer);
  let binary = '';
  for (let i = 0; i < bytes.length; i++) {
    binary += String.fromCharCode(bytes[i]);
  }
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

/** CredentialDescriptorJSON is one entry of excludeCredentials/
 * allowCredentials as the API's ceremony options carry it: the credential
 * id as base64url text rather than a BufferSource. */
export interface CredentialDescriptorJSON {
  id: string;
  type: 'public-key';
  transports?: AuthenticatorTransport[];
}

export interface CreationOptionsJSON {
  challenge: string;
  rp: PublicKeyCredentialRpEntity;
  user: { id: string; name: string; displayName: string };
  pubKeyCredParams: PublicKeyCredentialParameters[];
  timeout?: number;
  excludeCredentials?: CredentialDescriptorJSON[];
  authenticatorSelection?: AuthenticatorSelectionCriteria;
  attestation?: AttestationConveyancePreference;
}

/** decodeCreationOptions turns the JSON options from POST
 * …/passkeys/register/begin into the PublicKeyCredentialCreationOptions
 * navigator.credentials.create expects, decoding every base64url field
 * (challenge, user.id, each excludeCredentials[].id) to an ArrayBuffer. */
export function decodeCreationOptions(json: CreationOptionsJSON): PublicKeyCredentialCreationOptions {
  return {
    ...json,
    challenge: base64urlToBuffer(json.challenge),
    user: { ...json.user, id: base64urlToBuffer(json.user.id) },
    excludeCredentials: json.excludeCredentials?.map((c) => ({ ...c, id: base64urlToBuffer(c.id) })),
  };
}

export interface RequestOptionsJSON {
  challenge: string;
  rpId?: string;
  timeout?: number;
  userVerification?: UserVerificationRequirement;
  allowCredentials?: CredentialDescriptorJSON[];
}

/** decodeRequestOptions is decodeCreationOptions' counterpart for login:
 * it turns the JSON options from POST …/passkeys/login/begin into
 * PublicKeyCredentialRequestOptions for navigator.credentials.get. */
export function decodeRequestOptions(json: RequestOptionsJSON): PublicKeyCredentialRequestOptions {
  return {
    ...json,
    challenge: base64urlToBuffer(json.challenge),
    allowCredentials: json.allowCredentials?.map((c) => ({ ...c, id: base64urlToBuffer(c.id) })),
  };
}

/** encodeAttestationCredential turns the PublicKeyCredential
 * navigator.credentials.create resolved with into the JSON body POST
 * …/passkeys/register/finish expects (the `response` field's
 * ceremony_id/name are added by the caller, not this codec — see
 * features/auth/passkeys.ts). */
export function encodeAttestationCredential(cred: PublicKeyCredential): unknown {
  const response = cred.response as AuthenticatorAttestationResponse;
  return {
    id: cred.id,
    rawId: bufferToBase64url(cred.rawId),
    type: cred.type,
    response: {
      clientDataJSON: bufferToBase64url(response.clientDataJSON),
      attestationObject: bufferToBase64url(response.attestationObject),
    },
    clientExtensionResults: cred.getClientExtensionResults(),
  };
}

/** encodeAssertionCredential is encodeAttestationCredential's counterpart
 * for login: the JSON body POST …/passkeys/login/finish expects. */
export function encodeAssertionCredential(cred: PublicKeyCredential): unknown {
  const response = cred.response as AuthenticatorAssertionResponse;
  return {
    id: cred.id,
    rawId: bufferToBase64url(cred.rawId),
    type: cred.type,
    response: {
      clientDataJSON: bufferToBase64url(response.clientDataJSON),
      authenticatorData: bufferToBase64url(response.authenticatorData),
      signature: bufferToBase64url(response.signature),
      userHandle: response.userHandle ? bufferToBase64url(response.userHandle) : null,
    },
    clientExtensionResults: cred.getClientExtensionResults(),
  };
}

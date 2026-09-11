import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { bufferToBase64url } from './webauthnCodec.ts';
import type { CreationOptionsJSON, RequestOptionsJSON } from './webauthnCodec.ts';

/**
 * The previous round's fix (task-15b-report.md, "Product bug found #1")
 * taught registerPasskey/loginWithPasskey to unwrap the `publicKey`
 * envelope go-webauthn's protocol.CredentialCreation/CredentialAssertion
 * wrap every ceremony's options in (internal/auth/passkey.go's
 * BeginRegistration/BeginLogin json.Marshal them that way). Before the
 * fix, `begin.options` was handed to decodeCreationOptions/
 * decodeRequestOptions unwrapped, and every real ceremony threw before
 * ever reaching the browser — invisible to webauthnCodec.ts's own unit
 * tests (which fixture the already-flat shape) and, until now, to this
 * file, which had no test of its own at all: only the slow,
 * DevTools-dependent passkey.spec.ts (a CDP virtual authenticator) caught
 * it.
 *
 * This test feeds the *actual* enveloped wire shape — `{ options:
 * { publicKey: {...} } }` — through both begin functions and asserts the
 * codec receives the unwrapped, flat options, not the envelope. Removing
 * the `.publicKey` unwrap in passkeys.ts (handing `wrapped` itself to the
 * codec instead of `wrapped.publicKey`) makes both tests fail, since the
 * codec would then be called with `{ publicKey: {...} }` rather than the
 * `flatOptions` fixture below — confirmed by reverting the fix locally
 * before writing this file.
 */
vi.mock('../../api/hooks/usePasskeys.ts', () => ({
  beginPasskeyRegistration: vi.fn(),
  finishPasskeyRegistration: vi.fn(),
  beginPasskeyLogin: vi.fn(),
  finishPasskeyLogin: vi.fn(),
}));

vi.mock('./webauthnCodec.ts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./webauthnCodec.ts')>();
  return {
    ...actual,
    decodeCreationOptions: vi.fn(actual.decodeCreationOptions),
    decodeRequestOptions: vi.fn(actual.decodeRequestOptions),
  };
});

const {
  beginPasskeyRegistration,
  finishPasskeyRegistration,
  beginPasskeyLogin,
  finishPasskeyLogin,
} = await import('../../api/hooks/usePasskeys.ts');
const { decodeCreationOptions, decodeRequestOptions } = await import('./webauthnCodec.ts');
const { registerPasskey, loginWithPasskey } = await import('./passkeys.ts');

function bytesOf(str: string): Uint8Array {
  return new Uint8Array([...str].map((c) => c.charCodeAt(0)));
}

function fakeCredential(): PublicKeyCredential {
  return {
    id: 'credential-id',
    rawId: bytesOf('raw-id').buffer,
    type: 'public-key',
    response: {
      clientDataJSON: bytesOf('client-data').buffer,
      attestationObject: bytesOf('attestation').buffer,
      authenticatorData: bytesOf('auth-data').buffer,
      signature: bytesOf('sig').buffer,
      userHandle: bytesOf('user-handle').buffer,
    },
    getClientExtensionResults: vi.fn(() => ({})),
  } as unknown as PublicKeyCredential;
}

describe('registerPasskey / loginWithPasskey — the go-webauthn publicKey envelope', () => {
  beforeEach(() => {
    Object.defineProperty(navigator, 'credentials', {
      configurable: true,
      value: {
        create: vi.fn().mockResolvedValue(fakeCredential()),
        get: vi.fn().mockResolvedValue(fakeCredential()),
      },
    });
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it('unwraps the publicKey envelope before handing registration options to the codec', async () => {
    const flatOptions: CreationOptionsJSON = {
      challenge: bufferToBase64url(bytesOf('challenge')),
      rp: { id: 'pulsix.example', name: 'Pulsix' },
      user: { id: bufferToBase64url(bytesOf('user-id')), name: 'tim@acme.io', displayName: 'Tim R.' },
      pubKeyCredParams: [{ type: 'public-key', alg: -7 }],
    };
    // The real wire shape: internal/auth/passkey.go's BeginRegistration
    // json.Marshals go-webauthn's protocol.CredentialCreation, which wraps
    // the options one level deeper under `publicKey` — not the flat
    // object CreationOptionsJSON describes on its own.
    vi.mocked(beginPasskeyRegistration).mockResolvedValue({
      ceremony_id: 'cer_1',
      options: { publicKey: flatOptions },
    } as never);

    await registerPasskey('My key');

    expect(decodeCreationOptions).toHaveBeenCalledTimes(1);
    expect(decodeCreationOptions).toHaveBeenCalledWith(flatOptions);
    expect(finishPasskeyRegistration).toHaveBeenCalledWith(
      expect.objectContaining({ ceremony_id: 'cer_1', name: 'My key' }),
    );
  });

  it('unwraps the publicKey envelope before handing login options to the codec', async () => {
    const flatOptions: RequestOptionsJSON = {
      challenge: bufferToBase64url(bytesOf('login-challenge')),
    };
    // BeginLogin wraps the same way, around protocol.CredentialAssertion.
    vi.mocked(beginPasskeyLogin).mockResolvedValue({
      ceremony_id: 'cer_2',
      options: { publicKey: flatOptions },
    } as never);

    await loginWithPasskey();

    expect(decodeRequestOptions).toHaveBeenCalledTimes(1);
    expect(decodeRequestOptions).toHaveBeenCalledWith(flatOptions);
    expect(finishPasskeyLogin).toHaveBeenCalledWith(expect.objectContaining({ ceremony_id: 'cer_2' }));
  });
});

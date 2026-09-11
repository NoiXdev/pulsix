import { describe, expect, it, vi } from 'vitest';
import {
  base64urlToBuffer,
  bufferToBase64url,
  decodeCreationOptions,
  decodeRequestOptions,
  encodeAssertionCredential,
  encodeAttestationCredential,
} from './webauthnCodec.ts';

function bytesOf(str: string): Uint8Array {
  return new Uint8Array([...str].map((c) => c.charCodeAt(0)));
}

describe('base64urlToBuffer', () => {
  it('decodes a known base64url value to the expected bytes', () => {
    // "Hello" in standard base64 is "SGVsbG8=" — base64url drops the
    // padding and would swap +/ for -_ if any were present here.
    const buf = base64urlToBuffer('SGVsbG8');
    expect(new Uint8Array(buf)).toEqual(bytesOf('Hello'));
  });

  it('decodes the URL-safe characters - and _ that base64 would encode as + and /', () => {
    // Byte sequence [0xfb, 0xff, 0xbf] is "+/+/" in standard base64
    // ("+/+/" would appear as base64 "+/+/" only with different bytes —
    // pick bytes that are unambiguous): 0xfb 0xef 0xbe standard-encodes
    // with a '+' and jsdom's own base64url would use '-' for it.
    const bytes = new Uint8Array([0xfb, 0xef, 0xbe]);
    const url = bufferToBase64url(bytes);
    expect(url).not.toMatch(/[+/=]/);
    expect(new Uint8Array(base64urlToBuffer(url))).toEqual(bytes);
  });
});

describe('bufferToBase64url', () => {
  it('encodes bytes to base64url with no padding', () => {
    expect(bufferToBase64url(bytesOf('Hello'))).toBe('SGVsbG8');
  });

  it('round-trips arbitrary bytes through encode and decode', () => {
    const original = new Uint8Array(32);
    for (let i = 0; i < original.length; i++) original[i] = (i * 37 + 5) % 256;

    const encoded = bufferToBase64url(original);
    expect(encoded).not.toMatch(/[+/=]/); // must be genuinely URL-safe
    const decoded = new Uint8Array(base64urlToBuffer(encoded));
    expect(decoded).toEqual(original);
  });
});

describe('decodeCreationOptions', () => {
  it('decodes challenge, user.id and excludeCredentials ids to ArrayBuffers, leaving everything else untouched', () => {
    const json = {
      challenge: bufferToBase64url(bytesOf('challenge')),
      rp: { id: 'pulsix.example', name: 'Pulsix' },
      user: { id: bufferToBase64url(bytesOf('user-id')), name: 'tim@acme.io', displayName: 'Tim R.' },
      pubKeyCredParams: [{ type: 'public-key' as const, alg: -7 }],
      excludeCredentials: [{ id: bufferToBase64url(bytesOf('cred-1')), type: 'public-key' as const }],
      attestation: 'none' as const,
    };

    const decoded = decodeCreationOptions(json);

    expect(new Uint8Array(decoded.challenge as ArrayBuffer)).toEqual(bytesOf('challenge'));
    expect(new Uint8Array(decoded.user.id as ArrayBuffer)).toEqual(bytesOf('user-id'));
    expect(new Uint8Array(decoded.excludeCredentials![0].id as ArrayBuffer)).toEqual(bytesOf('cred-1'));
    expect(decoded.rp).toEqual(json.rp);
    expect(decoded.user.name).toBe('tim@acme.io');
    expect(decoded.attestation).toBe('none');
  });

  it('leaves excludeCredentials undefined when the options carry none (first-time enrolment)', () => {
    const decoded = decodeCreationOptions({
      challenge: bufferToBase64url(bytesOf('c')),
      rp: { name: 'Pulsix' },
      user: { id: bufferToBase64url(bytesOf('u')), name: 'a@b.c', displayName: 'A' },
      pubKeyCredParams: [],
    });
    expect(decoded.excludeCredentials).toBeUndefined();
  });
});

describe('decodeRequestOptions', () => {
  it('decodes challenge and allowCredentials ids to ArrayBuffers', () => {
    const json = {
      challenge: bufferToBase64url(bytesOf('login-challenge')),
      allowCredentials: [{ id: bufferToBase64url(bytesOf('cred-a')), type: 'public-key' as const }],
      userVerification: 'preferred' as const,
    };

    const decoded = decodeRequestOptions(json);

    expect(new Uint8Array(decoded.challenge as ArrayBuffer)).toEqual(bytesOf('login-challenge'));
    expect(new Uint8Array(decoded.allowCredentials![0].id as ArrayBuffer)).toEqual(bytesOf('cred-a'));
    expect(decoded.userVerification).toBe('preferred');
  });

  it('supports discoverable login with no allowCredentials at all', () => {
    const decoded = decodeRequestOptions({ challenge: bufferToBase64url(bytesOf('c')) });
    expect(decoded.allowCredentials).toBeUndefined();
  });
});

function fakeCredential(overrides: {
  response: Record<string, unknown>;
}): PublicKeyCredential {
  return {
    id: 'credential-id',
    rawId: bytesOf('raw-id').buffer,
    type: 'public-key',
    response: overrides.response,
    getClientExtensionResults: vi.fn(() => ({})),
  } as unknown as PublicKeyCredential;
}

describe('encodeAttestationCredential', () => {
  it('encodes id/rawId/type and the attestation response fields to base64url', () => {
    const cred = fakeCredential({
      response: {
        clientDataJSON: bytesOf('client-data').buffer,
        attestationObject: bytesOf('attestation').buffer,
      },
    });

    const encoded = encodeAttestationCredential(cred) as {
      id: string;
      rawId: string;
      type: string;
      response: { clientDataJSON: string; attestationObject: string };
    };

    expect(encoded.id).toBe('credential-id');
    expect(encoded.type).toBe('public-key');
    expect(encoded.rawId).toBe(bufferToBase64url(bytesOf('raw-id')));
    expect(encoded.response.clientDataJSON).toBe(bufferToBase64url(bytesOf('client-data')));
    expect(encoded.response.attestationObject).toBe(bufferToBase64url(bytesOf('attestation')));
  });
});

describe('encodeAssertionCredential', () => {
  it('encodes id/rawId/type and the assertion response fields, including a present userHandle', () => {
    const cred = fakeCredential({
      response: {
        clientDataJSON: bytesOf('client-data').buffer,
        authenticatorData: bytesOf('auth-data').buffer,
        signature: bytesOf('sig').buffer,
        userHandle: bytesOf('user-handle').buffer,
      },
    });

    const encoded = encodeAssertionCredential(cred) as {
      response: { authenticatorData: string; signature: string; userHandle: string | null };
    };

    expect(encoded.response.authenticatorData).toBe(bufferToBase64url(bytesOf('auth-data')));
    expect(encoded.response.signature).toBe(bufferToBase64url(bytesOf('sig')));
    expect(encoded.response.userHandle).toBe(bufferToBase64url(bytesOf('user-handle')));
  });

  it('encodes a null userHandle as null, not a base64url empty string', () => {
    const cred = fakeCredential({
      response: {
        clientDataJSON: bytesOf('client-data').buffer,
        authenticatorData: bytesOf('auth-data').buffer,
        signature: bytesOf('sig').buffer,
        userHandle: null,
      },
    });

    const encoded = encodeAssertionCredential(cred) as { response: { userHandle: string | null } };
    expect(encoded.response.userHandle).toBeNull();
  });
});

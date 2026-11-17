import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';

export function usePasskeys() {
  return useQuery({
    queryKey: ['passkeys'],
    queryFn: () => unwrap(api.GET('/api/v1/auth/passkeys')),
  });
}

export function useDeletePasskey() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE('/api/v1/auth/passkeys/{id}', { params: { path: { id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['passkeys'] }),
  });
}

/** The four raw ceremony calls features/auth/passkeys.ts orchestrates
 * around navigator.credentials — plain functions, not hooks, since a
 * WebAuthn ceremony is an imperative sequence (begin, call the browser
 * API, finish) rather than declarative data fetching. */
export async function beginPasskeyRegistration() {
  return unwrap(api.POST('/api/v1/auth/passkeys/register/begin', {}));
}

export async function finishPasskeyRegistration(body: { ceremony_id: string; name: string; response: unknown }) {
  return unwrap(api.POST('/api/v1/auth/passkeys/register/finish', { body: body as never }));
}

export async function beginPasskeyLogin() {
  return unwrap(api.POST('/api/v1/auth/passkeys/login/begin', {}));
}

export async function finishPasskeyLogin(body: { ceremony_id: string; response: unknown }) {
  return unwrap(api.POST('/api/v1/auth/passkeys/login/finish', { body: body as never }));
}

/** Passkey re-authentication of the signed-in account (backlog 3.93): the
 * same ceremony as sign-in, but begun against the current session. The
 * finish route says what the proof is for: a step-up of this session, or
 * the renewal of its lifetime. */
export async function beginPasskeyReauth() {
  return unwrap(api.POST('/api/v1/auth/passkeys/reauth/begin', {}));
}

export async function finishPasskeyReauth(body: { ceremony_id: string; response: unknown }) {
  return unwrap(api.POST('/api/v1/auth/passkeys/reauth/finish', { body: body as never }));
}

export async function finishPasskeyRenew(body: { ceremony_id: string; response: unknown }) {
  return unwrap(api.POST('/api/v1/auth/session/renew/passkey', { body: body as never }));
}

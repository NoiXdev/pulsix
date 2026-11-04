import { useMutation, useQuery } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';

/** useResetAvailable reads the public flag that says whether a password can
 * be reset by e-mail: outbound mail is configured. */
export function useResetAvailable() {
  return useQuery({
    queryKey: ['password-reset-available'],
    queryFn: () => unwrap(api.GET('/api/v1/auth/password/reset-available')),
  });
}

/**
 * The three mutations below carry a password, a reset token or both in
 * their variables. gcTime: 0 drops a mutation from the cache the moment
 * nothing observes it any more (the same reason useTotp.ts gives), and the
 * pages call `reset()` once a request has settled, so a secret does not
 * stay readable in the mutation cache while the page is still open.
 */

/** useRequestReset asks for a reset link. The server answers 202 to every
 * address alike (or 429 when the client or address is rate limited). */
export function useRequestReset() {
  return useMutation({
    mutationFn: (email: string) => unwrap(api.POST('/api/v1/auth/password/forgot', { body: { email } })),
    gcTime: 0,
  });
}

/** useResetPassword sets a new password with the token of a reset link.
 * The token travels in the body only, never in a URL. It signs nobody in. */
export function useResetPassword() {
  return useMutation({
    mutationFn: (body: { token: string; new_password: string }) =>
      unwrap(api.POST('/api/v1/auth/password/reset', { body })),
    gcTime: 0,
  });
}

/** useChangePassword changes the signed-in account's own password. It needs
 * a fresh second factor (step_up_required otherwise) and ends every other
 * session of the account; this one stays. */
export function useChangePassword() {
  return useMutation({
    mutationFn: (body: { current_password: string; new_password: string }) =>
      unwrap(api.POST('/api/v1/auth/password', { body })),
    gcTime: 0,
  });
}

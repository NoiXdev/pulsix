import { useMutation, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';

/**
 * gcTime: 0 on both mutations below is the Critical fix (task-15a-report.md
 * fix round 1): their `data` carries a TOTP seed/otpauth_url or freshly
 * generated recovery codes. TanStack Query keeps a mutation's result in
 * its mutation cache for `gcTime` after the component that fired it
 * unmounts — the default is five minutes — reachable the whole time via
 * `queryClient.getMutationCache().getAll()` from any script on the origin.
 * gcTime: 0 drops the result the moment nothing observes the mutation any
 * more; AccountSecurityPage.tsx additionally calls `.reset()` on it as
 * soon as the secret has been consumed or explicitly dismissed, which
 * clears it even sooner (gcTime only bounds the *unobserved* lifetime).
 */
export function useEnrolTotp() {
  return useMutation({
    // A first-time enrolment sends no code at all — internal/api's
    // codeInput.Body.Code carries `pattern:"^[0-9]{6}$"`, which huma
    // enforces on any *present* value, empty string included. Sending
    // `{ code: "" }` (as this used to) therefore 422s outright, breaking
    // enrolment for every user with no existing factor — i.e. everyone
    // the setup wizard's own "enrol a second factor now" step sends here.
    // Only re-enrolling over an already-confirmed secret needs a code
    // (auth.TOTP.BeginEnrol), so the key is omitted entirely when there
    // is none, not sent empty.
    mutationFn: (code?: string) => unwrap(api.POST('/api/v1/auth/totp/enrol', { body: code ? { code } : {} })),
    gcTime: 0,
  });
}

export function useConfirmTotp() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (code: string) => unwrap(api.POST('/api/v1/auth/totp/confirm', { body: { code } })),
    gcTime: 0,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['me'] }),
  });
}

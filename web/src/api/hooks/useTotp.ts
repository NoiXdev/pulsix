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
    mutationFn: (code?: string) => unwrap(api.POST('/api/v1/auth/totp/enrol', { body: { code: code ?? '' } })),
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

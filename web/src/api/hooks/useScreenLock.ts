import { useMutation, useQueryClient } from '@tanstack/react-query';
import { api, ApiError, unwrap } from '../client.ts';
import { meQueryKey } from './useMe.ts';

/** attemptsLeft reads the extra `attempts_left` number a wrong-PIN answer
 * (422) carries; it is not in the generated schema, so it comes off the
 * error body untyped. Undefined when the answer has none. */
export function attemptsLeft(err: unknown): number | undefined {
  if (!(err instanceof ApiError) || !err.body || typeof err.body !== 'object') return undefined;
  const n = (err.body as Record<string, unknown>).attempts_left;
  return typeof n === 'number' ? n : undefined;
}

/*
 * gcTime: 0 on the PIN mutations below follows useTotp.ts and useTokens.ts:
 * TanStack Query keeps a mutation, `variables` included, in its mutation
 * cache for gcTime (five minutes by default) after nothing observes it,
 * reachable through `queryClient.getMutationCache().getAll()`. The PIN
 * goes in as `variables`, so it must not outlive the form. RootLayout also
 * clears the mutation cache when the session locks, and LockScreen resets
 * the unlock mutation after every failed attempt.
 */

/** useLockSession locks the session (409 `lock_pin_missing` when no PIN is
 * set). The caller switches its own tab and tells the others
 * (features/lock/lockChannel.ts). */
export function useLockSession() {
  return useMutation({
    mutationFn: () => unwrap(api.POST('/api/v1/auth/lock', {})),
  });
}

/** useUnlockSession checks the PIN. 422 is a wrong PIN (see attemptsLeft);
 * 401 "too many wrong pins" means the fifth wrong PIN ended the session,
 * any other 401 that the session had already ended. */
export function useUnlockSession() {
  return useMutation({
    mutationFn: (pin: string) => unwrap(api.POST('/api/v1/auth/lock/unlock', { body: { pin } })),
    gcTime: 0,
  });
}

/** useSetLockPin sets or replaces the PIN (step-up required to replace). */
export function useSetLockPin() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (pin: string) => unwrap(api.PUT('/api/v1/auth/lock/pin', { body: { pin } })),
    gcTime: 0,
    onSuccess: () => qc.invalidateQueries({ queryKey: meQueryKey }),
  });
}

/** useRemoveLockPin removes the PIN (step-up required). */
export function useRemoveLockPin() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => unwrap(api.DELETE('/api/v1/auth/lock/pin', {})),
    gcTime: 0,
    onSuccess: () => qc.invalidateQueries({ queryKey: meQueryKey }),
  });
}

/** useSkipLockPinPrompt records that the person skipped the sign-in
 * "set a lock PIN" step. */
export function useSkipLockPinPrompt() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => unwrap(api.POST('/api/v1/auth/lock/prompt-skipped', {})),
    onSuccess: () => qc.invalidateQueries({ queryKey: meQueryKey }),
  });
}

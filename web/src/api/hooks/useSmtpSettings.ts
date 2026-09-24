import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { SmtpSaveValues } from '../../lib/schemas.ts';

const smtpQueryKey = ['admin', 'smtp'] as const;

/** The instance's outgoing mail settings (instance administrators only;
 * the password is never returned). `configured: false` on an instance
 * whose operator skipped mail in the setup wizard. */
export function useSmtpSettings() {
  return useQuery({ queryKey: smtpQueryKey, queryFn: () => unwrap(api.GET('/api/v1/admin/settings/smtp')) });
}

/** Replaces the stored settings — every field, the password included. */
export function useSaveSmtpSettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: SmtpSaveValues) => unwrap(api.PUT('/api/v1/admin/settings/smtp', { body })),
    // gcTime: 0 — the variables carry the SMTP password; drop them from
    // the mutation cache as soon as nothing observes this mutation.
    gcTime: 0,
    onSuccess: () => qc.invalidateQueries({ queryKey: smtpQueryKey }),
  });
}

/** Sends a test message through the stored settings; a failure carries the
 * SMTP server's own answer in its message. */
export function useTestSmtpSettings() {
  return useMutation({
    mutationFn: (to: string) => unwrap(api.POST('/api/v1/admin/settings/smtp/test', { body: { to } })),
  });
}

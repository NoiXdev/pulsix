import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { BackupList, BackupSettingsInput } from '../types.ts';

const settingsKey = ['admin', 'backups', 'settings'] as const;
const listKey = ['admin', 'backups', 'list'] as const;

/** How often the list is fetched again while a backup is being written. */
const RUNNING_POLL_MS = 5000;

/**
 * The list's refetch interval: every 5 s while any entry is running, so the
 * entry turns into its result without the page being reloaded, and never
 * otherwise.
 */
export function backupsRefetchInterval(data: BackupList | undefined): number | false {
  return data?.items?.some((i) => i.state === 'running') ? RUNNING_POLL_MS : false;
}

/** The instance's backup settings (instance administrators only; the S3
 * secret key is never returned, only `s3.secret_key_set`). */
export function useBackupSettings() {
  return useQuery({ queryKey: settingsKey, queryFn: () => unwrap(api.GET('/api/v1/admin/backups/settings')) });
}

/** Replaces the settings. An S3 `secret_key` that is absent keeps the stored
 * one, `""` removes it. */
export function useSaveBackupSettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: BackupSettingsInput) => unwrap(api.PUT('/api/v1/admin/backups/settings', { body })),
    // gcTime: 0 — the variables may carry the S3 secret key; drop them from
    // the mutation cache as soon as nothing observes this mutation.
    gcTime: 0,
    // The destination and schedule feed the list's status strip too.
    onSuccess: () =>
      Promise.all([qc.invalidateQueries({ queryKey: settingsKey }), qc.invalidateQueries({ queryKey: listKey })]),
  });
}

/** Probes the destination described by the body without saving it; a failure
 * carries the storage error text in its message. */
export function useTestBackupSettings() {
  return useMutation({
    mutationFn: (body: BackupSettingsInput) => unwrap(api.POST('/api/v1/admin/backups/settings/test', { body })),
    gcTime: 0,
  });
}

/** Backups at the destination merged with recent attempts, newest first;
 * polls while one is running. */
export function useBackups() {
  return useQuery({
    queryKey: listKey,
    queryFn: () => unwrap(api.GET('/api/v1/admin/backups')),
    refetchInterval: (query) => backupsRefetchInterval(query.state.data),
  });
}

/** Starts a manual backup (202); a 409 means another one is running. */
export function useCreateBackup() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => unwrap(api.POST('/api/v1/admin/backups')),
    onSuccess: () => qc.invalidateQueries({ queryKey: listKey }),
  });
}

/** Deletes a stored backup by name. */
export function useDeleteBackup() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) => unwrap(api.DELETE('/api/v1/admin/backups/{name}', { params: { path: { name } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: listKey }),
  });
}

/** The raw download route is not in the OpenAPI document (it streams); the
 * browser fetches it as a plain link with the session cookie. */
export function backupDownloadUrl(name: string): string {
  return `/api/v1/admin/backups/${encodeURIComponent(name)}/download`;
}

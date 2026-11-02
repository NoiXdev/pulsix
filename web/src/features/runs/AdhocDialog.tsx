import { useState, type FormEvent } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Label } from '../../ui/label.tsx';
import { LoadMoreButton } from '../../ui/LoadMoreButton.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useHosts } from '../../api/hooks/useHosts.ts';
import { useRunAdhocCommand } from '../../api/hooks/useJobs.ts';
import { useT } from '../../i18n/useT.ts';

// MAX_ARGV mirrors the server's bound (internal/jobs/adhoc.go's
// maxAdhocArgv, the API's huma maxLength): a client-side cap only, the
// server is the authority.
const MAX_ARGV = 8 << 10;

/**
 * AdhocDialog runs one shell command on explicit managed hosts (spec §7,
 * backlog 2.14): a paginated, searchable multi-select of managed hosts
 * (every managed host stays reachable, the same "search past the loaded
 * page" contract ManagedHostPicker uses for the single-select case) plus
 * the command itself. Module is fixed to shell in v0.2b — the field exists
 * server-side so v0.3 can extend it, nothing to choose here yet. On
 * success the dialog navigates to the run view of the first job of the
 * group, the same landing every other run action uses.
 */
export function AdhocDialog({ org, onClose }: { org: string; onClose: () => void }) {
  const t = useT();
  const toast = useToast();
  const navigate = useNavigate();
  const runAdhoc = useRunAdhocCommand(org);
  const [q, setQ] = useState('');
  const [selected, setSelected] = useState<readonly string[]>([]);
  const [argv, setArgv] = useState('');
  const [error, setError] = useState<string | null>(null);

  const hosts = useHosts(org, { managed: true, q });
  const page = hosts.data?.pages.flatMap((p) => p.hosts ?? []) ?? [];
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = hosts;

  function toggle(id: string) {
    setSelected((prev) => (prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]));
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    if (selected.length === 0) {
      setError(t.runs.adhoc.chooseHost);
      return;
    }
    if (argv.trim() === '') {
      setError(t.runs.adhoc.commandRequired);
      return;
    }
    try {
      const { jobs } = await runAdhoc.mutateAsync({ host_ids: [...selected], module: 'shell', argv });
      toast.show(t.runs.adhoc.started(selected.length));
      if (jobs && jobs.length > 0) {
        await navigate({ to: '/o/$org/runs/$id', params: { org, id: jobs[0].id } });
      }
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : t.runs.adhoc.failed);
    }
  }

  return (
    <Dialog open onClose={onClose} size="wide" title={t.runs.adhoc.title}>
      <form onSubmit={submit} className="flex max-h-[70vh] flex-col gap-3 overflow-y-auto">
        <p className="text-xs text-muted-foreground">
          {t.runs.adhoc.intro}
        </p>
        <div className="flex flex-col gap-1">
          <Label htmlFor="adhoc-search">{t.runs.adhoc.hosts}</Label>
          <input
            id="adhoc-search"
            type="search"
            aria-label={t.runs.adhoc.searchLabel}
            placeholder={t.runs.adhoc.searchPlaceholder}
            className="h-8 w-full rounded-md border border-border bg-background px-2 text-xs text-foreground"
            value={q}
            onChange={(e) => setQ(e.target.value)}
          />
          <div className="flex max-h-48 flex-col gap-1 overflow-y-auto rounded-md border border-border p-2">
            {page.map((h) => (
              <label key={h.id} className="flex items-center gap-2 text-xs">
                <input type="checkbox" checked={selected.includes(h.id)} onChange={() => toggle(h.id)} aria-label={h.name} />
                {h.name}
              </label>
            ))}
            {hosts.data && page.length === 0 && (
              <span className="text-[11px] text-muted-foreground">
                {q ? t.runs.adhoc.noMatch(q) : t.runs.adhoc.noHosts}
              </span>
            )}
          </div>
          {hasNextPage && (
            <div className="flex items-center justify-between gap-2 text-[11px] text-muted-foreground">
              <span>{t.runs.adhoc.more}</span>
              <LoadMoreButton onClick={() => void fetchNextPage()} loading={isFetchingNextPage} />
            </div>
          )}
          <span className="text-[11px] text-muted-foreground">{t.runs.adhoc.selected(selected.length)}</span>
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor="adhoc-argv">{t.runs.adhoc.command}</Label>
          <textarea
            id="adhoc-argv"
            aria-label={t.runs.adhoc.command}
            rows={4}
            spellCheck={false}
            maxLength={MAX_ARGV}
            className="rounded-md border border-border bg-background p-2 font-mono text-[11px] text-foreground"
            value={argv}
            onChange={(e) => setArgv(e.target.value)}
            placeholder="uptime"
          />
        </div>
        {error && (
          <p role="alert" className="text-xs text-destructive">
            {error}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={onClose}>
            {t.common.cancel}
          </Button>
          <Button type="submit" variant="primary" disabled={runAdhoc.isPending}>
            {t.runs.adhoc.submit}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

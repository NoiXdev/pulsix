import { useState } from 'react';
import { useNavigate, useParams, useSearch } from '@tanstack/react-router';
import { Link } from '@tanstack/react-router';
import { Terminal } from 'lucide-react';
import { Card, GridHeader, GridRow } from '../../ui/table.tsx';
import { Pill } from '../../ui/badge.tsx';
import { Button } from '../../ui/button.tsx';
import { Select } from '../../ui/Select.tsx';
import { LoadMoreButton } from '../../ui/LoadMoreButton.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useJobs } from '../../api/hooks/useJobs.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useSchedules } from '../../api/hooks/useSchedules.ts';
import { may } from '../../lib/roles.ts';
import { AdhocDialog } from './AdhocDialog.tsx';
import { HostFilterChip } from './HostFilterChip.tsx';
import { kindLabel, shortId, statusLabel, statusTone } from './status.ts';
import { TRIGGERS, triggerLabel } from './trigger.ts';
import { useT } from '../../i18n/useT.ts';

const COLUMNS = '90px 120px 110px 90px minmax(0, 0.7fr) 90px 110px';

const STATUSES = ['queued', 'running', 'succeeded', 'failed', 'cancelled', 'error'];
const KINDS = ['run', 'connection_test', 'bootstrap', 'rotate_key', 'revoke_key', 'sync_repo'];

/**
 * RunsPage is the org's run list (spec §7, backlog 2.12): keyset-paginated,
 * filterable by status, kind, trigger and schedule (spec 2026-09-18 §8),
 * each row a link into the run view. The filters live in the URL
 * (?status=&kind=&trigger=&schedule=&host=), validated by the route; the
 * host filter (the runs that targeted one host, linked from the host view)
 * shows as a removable chip.
 */
export function RunsPage() {
  const t = useT();
  const { org } = useParams({ strict: false }) as { org: string };
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as { status?: string; kind?: string; trigger?: string; schedule?: string; host?: string };
  const jobs = useJobs(org, { status: search.status, kind: search.kind, trigger: search.trigger, triggerRef: search.schedule, host: search.host });
  const schedules = useSchedules(search.trigger === 'schedule' ? org : '');
  const orgInfo = useOrg(org);
  const canRun = may(orgInfo.data?.org.role, 'jobs.run');
  const [adhocOpen, setAdhocOpen] = useState(false);

  const guard = useQueryGuard(jobs);
  if (guard) return guard;
  if (!jobs.data) return <QueryLoading />;
  const rows = jobs.data.pages.flatMap((p) => p.jobs ?? []);

  function filter(next: { status?: string; kind?: string; trigger?: string; schedule?: string; host?: string }) {
    const trigger = next.trigger ?? search.trigger;
    void navigate({
      to: '/o/$org/runs',
      params: { org },
      search: {
        status: next.status ?? search.status,
        kind: next.kind ?? search.kind,
        trigger,
        // A schedule filter only makes sense for scheduled runs.
        schedule: trigger === 'schedule' ? (next.schedule ?? search.schedule) : undefined,
        host: search.host,
      },
    });
  }

  function clearHost() {
    void navigate({
      to: '/o/$org/runs',
      params: { org },
      search: { status: search.status, kind: search.kind, trigger: search.trigger, schedule: search.schedule, host: undefined },
    });
  }

  return (
    <>
      <div className="flex items-center justify-between">
        <div className="flex flex-col gap-2">
          <h1 className="text-xl font-semibold">{t.runs.list.title}</h1>
          <span className="text-[13px] text-muted-foreground">{t.runs.list.summary(rows.length)}</span>
        </div>
        <div className="flex gap-2">
          <Select
            aria-label={t.runs.list.filterStatus}
            options={STATUSES.map((s) => ({ value: s, label: statusLabel(s, t) }))}
            value={search.status ?? ''}
            onChange={(value) => filter({ status: value })}
            placeholder={t.runs.list.allStatuses}
          />
          <Select
            aria-label={t.runs.list.filterKind}
            options={KINDS.map((k) => ({ value: k, label: kindLabel(k, t) }))}
            value={search.kind ?? ''}
            onChange={(value) => filter({ kind: value })}
            placeholder={t.runs.list.allKinds}
          />
          <Select
            aria-label={t.runs.list.filterTrigger}
            options={TRIGGERS.map((k) => ({ value: k, label: triggerLabel(k, t) }))}
            value={search.trigger ?? ''}
            onChange={(value) => filter({ trigger: value })}
            placeholder={t.runs.list.allTriggers}
          />
          {search.trigger === 'schedule' && (
            <Select
              aria-label={t.runs.list.filterSchedule}
              options={(schedules.data?.schedules ?? []).map((sc) => ({ value: sc.id, label: sc.name }))}
              value={search.schedule ?? ''}
              onChange={(value) => filter({ schedule: value })}
              placeholder={t.runs.list.allSchedules}
            />
          )}
          {canRun && (
            <Button variant="primary" onClick={() => setAdhocOpen(true)}>
              <Terminal size={15} /> {t.runs.list.runCommand}
            </Button>
          )}
        </div>
      </div>
      {search.host && <HostFilterChip org={org} hostId={search.host} onRemove={clearHost} />}
      <Card>
        <GridHeader columns={COLUMNS}>
          <span>{t.runs.list.columns.run}</span>
          <span>{t.runs.list.columns.kind}</span>
          <span>{t.runs.list.columns.status}</span>
          <span>{t.runs.list.columns.trigger}</span>
          <span>{t.runs.list.columns.template}</span>
          <span>{t.runs.list.columns.zone}</span>
          <span>{t.runs.list.columns.started}</span>
        </GridHeader>
        {rows.map((j) => (
          <GridRow key={j.id} columns={COLUMNS}>
            <Link to="/o/$org/runs/$id" params={{ org, id: j.id }} className="font-mono text-[13px] hover:text-primary">
              {shortId(j.id)}
            </Link>
            <span className="text-xs">{kindLabel(j.kind, t)}</span>
            <span><Pill tone={statusTone(j.status)}>{statusLabel(j.status, t)}</Pill></span>
            <span className="text-xs">{triggerLabel(j.trigger, t)}</span>
            <span className="truncate font-mono text-xs text-muted-foreground">{j.template_id ? shortId(j.template_id) : '—'}</span>
            <span className="font-mono text-xs text-muted-foreground">{shortId(j.zone_id)}</span>
            <span className="text-xs text-muted-foreground">{j.created_at.slice(0, 16).replace('T', ' ')}</span>
          </GridRow>
        ))}
        {rows.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">{t.runs.list.empty}</p>}
      </Card>
      {jobs.hasNextPage && <LoadMoreButton onClick={() => void jobs.fetchNextPage()} loading={jobs.isFetchingNextPage} />}
      {adhocOpen && <AdhocDialog org={org} onClose={() => setAdhocOpen(false)} />}
    </>
  );
}

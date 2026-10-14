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
import { kindLabel, shortId, statusTone } from './status.ts';
import { TRIGGER_LABEL, triggerLabel } from './trigger.ts';

const COLUMNS = '90px 120px 110px 90px minmax(0, 0.7fr) 90px 110px';

const STATUSES = ['queued', 'running', 'succeeded', 'failed', 'cancelled', 'error'];
const KINDS = ['run', 'connection_test', 'bootstrap', 'rotate_key', 'revoke_key', 'sync_repo'];

/**
 * RunsPage is the org's run list (spec §7, backlog 2.12): keyset-paginated,
 * filterable by status, kind, trigger and schedule (spec 2026-09-18 §8),
 * each row a link into the run view. The filters live in the URL
 * (?status=&kind=&trigger=&schedule=), validated by the route.
 */
export function RunsPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as { status?: string; kind?: string; trigger?: string; schedule?: string };
  const jobs = useJobs(org, { status: search.status, kind: search.kind, trigger: search.trigger, triggerRef: search.schedule });
  const schedules = useSchedules(search.trigger === 'schedule' ? org : '');
  const orgInfo = useOrg(org);
  const canRun = may(orgInfo.data?.org.role, 'jobs.run');
  const [adhocOpen, setAdhocOpen] = useState(false);

  const guard = useQueryGuard(jobs);
  if (guard) return guard;
  if (!jobs.data) return <QueryLoading />;
  const rows = jobs.data.pages.flatMap((p) => p.jobs ?? []);

  function filter(next: { status?: string; kind?: string; trigger?: string; schedule?: string }) {
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
      },
    });
  }

  return (
    <>
      <div className="flex items-center justify-between">
        <div className="flex flex-col gap-2">
          <h1 className="text-xl font-semibold">Runs</h1>
          <span className="text-[13px] text-muted-foreground">{rows.length} runs · every job the workers executed for this organization</span>
        </div>
        <div className="flex gap-2">
          <Select
            aria-label="Filter by status"
            options={STATUSES.map((s) => ({ value: s, label: s }))}
            value={search.status ?? ''}
            onChange={(value) => filter({ status: value })}
            placeholder="All statuses"
          />
          <Select
            aria-label="Filter by kind"
            options={KINDS.map((k) => ({ value: k, label: kindLabel(k) }))}
            value={search.kind ?? ''}
            onChange={(value) => filter({ kind: value })}
            placeholder="All kinds"
          />
          <Select
            aria-label="Filter by trigger"
            options={Object.entries(TRIGGER_LABEL).map(([k, label]) => ({ value: k, label }))}
            value={search.trigger ?? ''}
            onChange={(value) => filter({ trigger: value })}
            placeholder="All triggers"
          />
          {search.trigger === 'schedule' && (
            <Select
              aria-label="Filter by schedule"
              options={(schedules.data?.schedules ?? []).map((sc) => ({ value: sc.id, label: sc.name }))}
              value={search.schedule ?? ''}
              onChange={(value) => filter({ schedule: value })}
              placeholder="All schedules"
            />
          )}
          {canRun && (
            <Button variant="primary" onClick={() => setAdhocOpen(true)}>
              <Terminal size={15} /> Run command
            </Button>
          )}
        </div>
      </div>
      <Card>
        <GridHeader columns={COLUMNS}>
          <span>Run</span>
          <span>Kind</span>
          <span>Status</span>
          <span>Trigger</span>
          <span>Template</span>
          <span>Zone</span>
          <span>Started</span>
        </GridHeader>
        {rows.map((j) => (
          <GridRow key={j.id} columns={COLUMNS}>
            <Link to="/o/$org/runs/$id" params={{ org, id: j.id }} className="font-mono text-[13px] hover:text-primary">
              {shortId(j.id)}
            </Link>
            <span className="text-xs">{kindLabel(j.kind)}</span>
            <span><Pill tone={statusTone(j.status)}>{j.status}</Pill></span>
            <span className="text-xs">{triggerLabel(j.trigger)}</span>
            <span className="truncate font-mono text-xs text-muted-foreground">{j.template_id ? shortId(j.template_id) : '—'}</span>
            <span className="font-mono text-xs text-muted-foreground">{shortId(j.zone_id)}</span>
            <span className="text-xs text-muted-foreground">{j.created_at.slice(0, 16).replace('T', ' ')}</span>
          </GridRow>
        ))}
        {rows.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">No runs match.</p>}
      </Card>
      {jobs.hasNextPage && <LoadMoreButton onClick={() => void jobs.fetchNextPage()} loading={jobs.isFetchingNextPage} />}
      {adhocOpen && <AdhocDialog org={org} onClose={() => setAdhocOpen(false)} />}
    </>
  );
}

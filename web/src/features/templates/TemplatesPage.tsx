import { useState } from 'react';
import { useNavigate, useParams } from '@tanstack/react-router';
import { CalendarClock, ListPlus, Play, Webhook } from 'lucide-react';
import { Card, GridHeader, GridRow } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Pill } from '../../ui/badge.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useGroups } from '../../api/hooks/useInventory.ts';
import { usePlaybooks } from '../../api/hooks/usePlaybooks.ts';
import { useCredentials } from '../../api/hooks/useCredentials.ts';
import { useRunTemplateById, useTemplates } from '../../api/hooks/useTemplates.ts';
import { may } from '../../lib/roles.ts';
import { TemplateDialog } from './TemplateDialog.tsx';
import { TriggersDialog } from '../triggers/TriggersDialog.tsx';
import type { Template } from '../../api/types.ts';

const COLUMNS = 'minmax(0, 0.8fr) minmax(0, 1fr) minmax(0, 0.9fr) minmax(0, 0.8fr) 110px';

/** TemplatesPage lists the job templates (spec §7, backlog 2.10): a
 * playbook plus scope, limit, extra vars and the default SSH credential —
 * the thing an operator runs, and the run view's own entry point. */
export function TemplatesPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const navigate = useNavigate();
  const toast = useToast();
  const orgInfo = useOrg(org);
  const templates = useTemplates(org);
  const playbooks = usePlaybooks(org);
  const groups = useGroups(org);
  const canWrite = may(orgInfo.data?.org.role, 'templates.write');
  const canRun = may(orgInfo.data?.org.role, 'jobs.run');
  const canReadCredentials = may(orgInfo.data?.org.role, 'credentials.read');
  // Gates the Default SSH credential picker's own create row:
  // credentials.write is admin, stricter than templates.write (v0.4g
  // Task 7 fix round).
  const canCreateCredential = may(orgInfo.data?.org.role, 'credentials.write');
  const credentials = useCredentials(org, { enabled: canReadCredentials });
  const runTemplate = useRunTemplateById(org);

  const [dialog, setDialog] = useState<'new' | Template | null>(null);
  const [triggers, setTriggers] = useState<Template | null>(null);

  const guard = useQueryGuard(templates);
  if (guard) return guard;
  if (!templates.data) return <QueryLoading />;
  const list = templates.data.templates ?? [];
  const credentialName = (id?: string) => (credentials.data?.credentials ?? []).find((c) => c.id === id)?.name;
  const groupName = (id: string) => (groups.data?.groups ?? []).find((g) => g.id === id)?.name ?? id.slice(0, 8);

  const playbookLabel = (id: string) => {
    const p = (playbooks.data?.playbooks ?? []).find((x) => x.id === id);
    if (!p) return id.slice(0, 8);
    return p.source === 'repo' ? `repo: ${p.path ?? ''}` : p.summary || 'editor playbook';
  };

  async function run(t: Template) {
    try {
      const { jobs } = await runTemplate.mutateAsync(t.id);
      toast.show(`Run of ${t.name} started`);
      if (jobs && jobs.length > 0) await navigate({ to: '/o/$org/runs/$id', params: { org, id: jobs[0].id } });
    } catch (err) {
      toast.show(err instanceof Error ? err.message : 'Starting the run failed', 'destructive');
    }
  }

  return (
    <>
      <div className="flex items-center justify-between">
        <div className="flex flex-col gap-2">
          <h1 className="text-xl font-semibold">Job templates</h1>
          <span className="text-[13px] text-muted-foreground">
            {list.length} templates · a playbook, its scope, its extra vars and one button to run it
          </span>
        </div>
        {canWrite && (
          <Button variant="primary" onClick={() => setDialog('new')}>
            <ListPlus size={15} /> Add template
          </Button>
        )}
      </div>
      <Card>
        <GridHeader columns={COLUMNS}>
          <span>Name</span>
          <span>Playbook</span>
          <span>Scope</span>
          <span>Default credential</span>
          <span>Actions</span>
        </GridHeader>
        {list.map((t) => (
          <GridRow key={t.id} columns={COLUMNS}>
            <span className="truncate">
              {canWrite ? (
                <button type="button" className="truncate text-left font-mono text-[13px] hover:text-primary" onClick={() => setDialog(t)}>
                  {t.name}
                </button>
              ) : (
                <span className="font-mono text-[13px]">{t.name}</span>
              )}
            </span>
            <span className="truncate font-mono text-xs text-muted-foreground">{playbookLabel(t.playbook_id)}</span>
            <span>{scopeChip(t, groupName)}</span>
            <span className="truncate text-xs text-muted-foreground">{t.credential_id ? (credentialName(t.credential_id) ?? t.credential_id.slice(0, 8)) : '—'}</span>
            <span className="flex gap-1">
              <Button type="button" variant="ghost" aria-label={`Triggers of ${t.name}`} onClick={() => setTriggers(t)}>
                <Webhook size={13} />
              </Button>
              <Button
                type="button"
                variant="ghost"
                aria-label={`Schedules of ${t.name}`}
                onClick={() => void navigate({ to: '/o/$org/schedules', params: { org }, search: { id: undefined, template: t.id } })}
              >
                <CalendarClock size={13} />
              </Button>
              {canRun && (
                <Button type="button" aria-label={`Run ${t.name}`} disabled={runTemplate.isPending} onClick={() => void run(t)}>
                  <Play size={13} /> Run
                </Button>
              )}
            </span>
          </GridRow>
        ))}
        {list.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">No job templates yet.</p>}
      </Card>
      {triggers && (
        <TriggersDialog
          org={org}
          target={{ kind: 'template_run', id: triggers.id, name: triggers.name }}
          canWrite={canWrite}
          canRun={canRun}
          onClose={() => setTriggers(null)}
        />
      )}
      {dialog === 'new' ? (
        <TemplateDialog
          org={org}
          canReadCredentials={canReadCredentials}
          canCreateCredential={canCreateCredential}
          onClose={() => setDialog(null)}
        />
      ) : dialog ? (
        <TemplateDialog
          org={org}
          template={{ id: dialog.id, name: dialog.name }}
          canReadCredentials={canReadCredentials}
          canCreateCredential={canCreateCredential}
          onClose={() => setDialog(null)}
        />
      ) : null}
    </>
  );
}

/** scopeChip names the template's targets: the whole organization, a
 * group, an explicit host selection, or both halves at once. */
function scopeChip(t: Template, groupName: (id: string) => string) {
  const count = (t.scope.host_ids ?? []).length;
  if (!t.scope.group_id && count === 0) return <Pill tone="unknown">whole organization</Pill>;
  const group = t.scope.group_id ? groupName(t.scope.group_id) : null;
  if (!group) return <Pill tone="accent">{count} {count === 1 ? 'host' : 'hosts'}</Pill>;
  if (count === 0) return <Pill tone="accent">{group}</Pill>;
  return <Pill tone="accent">{group} + {count} {count === 1 ? 'host' : 'hosts'}</Pill>;
}

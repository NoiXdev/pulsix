import { useState } from 'react';
import { Link, useNavigate, useParams } from '@tanstack/react-router';
import { Braces, CalendarClock, ListPlus, Play, Webhook } from 'lucide-react';
import { Card, GridHeader, GridRow } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { buttonClass } from '../../ui/buttonStyles.ts';
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
import { ReviewBadges } from './variables/ReviewBadges.tsx';
import type { Template } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';
import { useT } from '../../i18n/useT.ts';

const COLUMNS = 'minmax(0, 0.8fr) minmax(0, 1fr) minmax(0, 0.9fr) minmax(0, 0.8fr) 150px';

/** TemplatesPage lists the job templates (spec §7, backlog 2.10): a
 * playbook plus scope, limit, extra vars and the default SSH credential —
 * the thing an operator runs, and the run view's own entry point. */
export function TemplatesPage() {
  const t = useT();
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
    // "" is the unresolved-projection case (toTemplateDTO's own comment,
    // internal/api/template_handlers.go): this organization has not synced
    // a matching playbook from the shared repository yet. Distinguished
    // from "a real id this page's own playbook list does not know about"
    // (id.slice(0, 8)) rather than falling through to the same fallback,
    // which would otherwise render as an empty string.
    if (id === '') return t.templates.playbook.notSynced;
    const p = (playbooks.data?.playbooks ?? []).find((x) => x.id === id);
    if (!p) return id.slice(0, 8);
    return p.source === 'repo' ? t.templates.playbook.repo(p.path ?? '') : p.summary || t.templates.playbook.editor;
  };

  async function run(tpl: Template) {
    try {
      const { jobs } = await runTemplate.mutateAsync({ id: tpl.id });
      toast.show(t.templates.list.started(tpl.name));
      if (jobs && jobs.length > 0) await navigate({ to: '/o/$org/runs/$id', params: { org, id: jobs[0].id } });
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.templates.list.startFailed, 'destructive');
    }
  }

  return (
    <>
      <div className="flex items-center justify-between">
        <div className="flex flex-col gap-2">
          <h1 className="text-xl font-semibold">{t.templates.list.title}</h1>
          <span className="text-[13px] text-muted-foreground">
            {t.templates.list.summary(list.length)}
          </span>
        </div>
        {canWrite && (
          <Button variant="primary" onClick={() => setDialog('new')}>
            <ListPlus size={15} /> {t.templates.list.add}
          </Button>
        )}
      </div>
      <Card>
        <GridHeader columns={COLUMNS}>
          <span>{t.templates.list.columns.name}</span>
          <span>{t.templates.list.columns.playbook}</span>
          <span>{t.templates.list.columns.scope}</span>
          <span>{t.templates.list.columns.credential}</span>
          <span>{t.templates.list.columns.actions}</span>
        </GridHeader>
        {list.map((tpl) => (
          <GridRow key={tpl.id} columns={COLUMNS}>
            <span className="flex min-w-0 flex-wrap items-center gap-1.5">
              {canWrite ? (
                <button type="button" className="truncate text-left font-mono text-[13px] hover:text-primary" onClick={() => setDialog(tpl)}>
                  {tpl.name}
                </button>
              ) : (
                <span className="truncate font-mono text-[13px]">{tpl.name}</span>
              )}
              {/* tpl.shared_template_id marks a projection of an installation-
                  owned definition (spec v0.2f decision 1) — TemplateDialog
                  disables its name/playbook/limit/extra-vars fields, so the
                  row needs to say why before the dialog ever opens. */}
              {tpl.shared_template_id && <Pill tone="accent">{t.templates.list.managed}</Pill>}
              {/* What a linked template's variables leave for a person
                  (backlog 7.75); the variables page resolves it. */}
              <ReviewBadges review={tpl.variables_review} />
            </span>
            <span className="truncate font-mono text-xs text-muted-foreground">{playbookLabel(tpl.playbook_id)}</span>
            <span>{scopeChip(tpl, groupName, t)}</span>
            <span className="truncate text-xs text-muted-foreground">{tpl.credential_id ? (credentialName(tpl.credential_id) ?? tpl.credential_id.slice(0, 8)) : '—'}</span>
            <span className="flex gap-1">
              <Link
                to="/o/$org/templates/$id/variables"
                params={{ org, id: tpl.id }}
                aria-label={t.templates.list.variablesLabel(tpl.name)}
                title={t.templates.list.variables}
                className={buttonClass('ghost', 'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary')}
              >
                <Braces size={13} />
              </Link>
              <Button type="button" variant="ghost" aria-label={t.templates.list.triggersLabel(tpl.name)} onClick={() => setTriggers(tpl)}>
                <Webhook size={13} />
              </Button>
              <Button
                type="button"
                variant="ghost"
                aria-label={t.templates.list.schedulesLabel(tpl.name)}
                onClick={() => void navigate({ to: '/o/$org/schedules', params: { org }, search: { id: undefined, template: tpl.id } })}
              >
                <CalendarClock size={13} />
              </Button>
              {canRun && (
                <Button type="button" aria-label={t.templates.list.runLabel(tpl.name)} disabled={runTemplate.isPending} onClick={() => void run(tpl)}>
                  <Play size={13} /> {t.templates.list.run}
                </Button>
              )}
            </span>
          </GridRow>
        ))}
        {list.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">{t.templates.list.empty}</p>}
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
function scopeChip(tpl: Template, groupName: (id: string) => string, t: Messages) {
  const count = (tpl.scope.host_ids ?? []).length;
  if (!tpl.scope.group_id && count === 0) return <Pill tone="unknown">{t.templates.scope.whole}</Pill>;
  const group = tpl.scope.group_id ? groupName(tpl.scope.group_id) : null;
  if (!group) return <Pill tone="accent">{t.templates.scope.hosts(count)}</Pill>;
  if (count === 0) return <Pill tone="accent">{group}</Pill>;
  return <Pill tone="accent">{t.templates.scope.groupAndHosts(group, count)}</Pill>;
}

import { useState, type ReactNode } from 'react';
import { Copy, Webhook } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { Pill } from '../../ui/badge.tsx';
import { Section } from '../../ui/detail-layout.tsx';
import { ReviewNotice } from '../../ui/review-notice.tsx';
import { reviewHref } from '../../ui/reviewHref.ts';
import { useCopy } from '../../ui/useCopy.ts';
import { useCreateTrigger, type TriggerTarget } from '../../api/hooks/useTriggers.ts';
import type { Template, Trigger, TriggerInput } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { may } from '../../lib/roles.ts';
import { Deliveries } from '../triggers/Deliveries.tsx';
import { presetLabel } from '../triggers/presets.ts';
import { TriggerForm } from '../triggers/TriggerForm.tsx';
import { TriggerSecretDialog } from '../triggers/TriggerSecretDialog.tsx';
import { useTriggerCommands, type FreshSecret } from '../triggers/useTriggerCommands.ts';
import { PendingRun } from '../triggers/PendingRun.tsx';
import { useSettleSummary } from '../triggers/useSettleSummary.ts';

export interface TemplateTriggersTabProps {
  org: string;
  template: Template;
  role: string;
  /** The template's webhook triggers (the page reads them for the tab count
   * too); undefined while read. */
  triggers: readonly Trigger[] | undefined;
  /** Why the triggers could not be read, when they could not. */
  error: unknown;
}

/**
 * TemplateTriggersTab is the template page's Triggers tab: one card per
 * webhook trigger that starts this template (the triggers list's own
 * `template_id` filter) with its URL, sender, timestamp header, allowed
 * sources, what it does while a run is active, its settle delay and
 * whether it is enabled (or disabled for review after an assistant's
 * change), and a collected run while one waits (Run now with jobs.run,
 * Discard with templates.write). With
 * `templates.write` a card is edited in place, gets a new secret (shown
 * once, in the trigger secret dialog) or is deleted; anyone sees its
 * deliveries. "New trigger" creates one for this template and shows its
 * secret once.
 */
export function TemplateTriggersTab({ org, template, role, triggers, error }: TemplateTriggersTabProps) {
  const t = useT();
  const p = t.templates.page.triggers;
  const canWrite = may(role, 'templates.write');
  const canRun = canWrite && may(role, 'jobs.run');
  const target: TriggerTarget = { kind: 'template_run', id: template.id, name: template.name };
  const create = useCreateTrigger(org);
  const [creating, setCreating] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);
  const [secret, setSecret] = useState<FreshSecret | null>(null);

  async function onCreate(body: TriggerInput) {
    setCreateError(null);
    try {
      const res = await create.mutateAsync(body);
      setCreating(false);
      setSecret({ secret: res.secret, url: res.url || res.trigger.url });
    } catch (err) {
      setCreateError(err instanceof Error ? err.message : t.triggers.createFailed);
    }
  }

  return (
    <Section
      id="template-triggers"
      title={p.title}
      actions={
        canWrite &&
        !creating && (
          <Button variant="primary" onClick={() => setCreating(true)}>
            <Webhook size={14} /> {p.newTrigger}
          </Button>
        )
      }
    >
      <p className="text-[13px] text-muted-foreground">{p.hint}</p>
      {creating && (
        <div className="max-w-xl rounded-[10px] border border-border bg-card p-4">
          <TriggerForm
            target={target}
            submitLabel={t.triggers.createTrigger}
            pending={create.isPending}
            error={createError}
            onSubmit={(b) => void onCreate(b)}
            onCancel={() => {
              setCreating(false);
              setCreateError(null);
            }}
          />
        </div>
      )}
      {error ? (
        <p role="alert" className="text-xs text-destructive">
          {error instanceof Error ? error.message : p.readFailed}
        </p>
      ) : !triggers ? (
        <p className="text-[13px] text-muted-foreground">{t.common.loading}</p>
      ) : triggers.length === 0 ? (
        !creating && <p className="text-[13px] text-muted-foreground">{p.empty}</p>
      ) : (
        <div className="grid items-start gap-4 xl:grid-cols-2">
          {triggers.map((tr) => (
            <TriggerCard key={tr.id} org={org} target={target} trigger={tr} canWrite={canWrite} canRun={canRun} onSecret={setSecret} />
          ))}
        </div>
      )}
      {secret && <TriggerSecretDialog secret={secret.secret} url={secret.url} onClose={() => setSecret(null)} />}
    </Section>
  );
}

function TriggerCard({ org, target, trigger: tr, canWrite, canRun, onSecret }: {
  org: string;
  target: TriggerTarget;
  trigger: Trigger;
  canWrite: boolean;
  canRun: boolean;
  onSecret: (fresh: FreshSecret) => void;
}) {
  const t = useT();
  const p = t.templates.page.triggers;
  const copy = useCopy();
  const commands = useTriggerCommands(org, tr);
  const [editing, setEditing] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [deliveries, setDeliveries] = useState(false);
  const sources = tr.allow_cidrs ?? [];
  const settle = useSettleSummary(tr);

  async function onSave(body: TriggerInput) {
    setSaveError(null);
    const failed = await commands.save(body);
    if (failed) setSaveError(failed);
    else setEditing(false);
  }

  async function onRotate() {
    const fresh = await commands.rotate();
    if (fresh) onSecret(fresh);
  }

  return (
    <article aria-label={tr.name} className="flex min-w-0 flex-col gap-3 rounded-[10px] border border-border bg-card p-4">
      <div className="flex items-center justify-between gap-3">
        <h3 className="min-w-0 truncate text-sm font-semibold">{tr.name}</h3>
        {tr.disabled_reason ? (
          <Pill tone="warn">{t.schedules.needsReview}</Pill>
        ) : tr.enabled ? (
          <Pill tone="ok">{t.triggers.enabled}</Pill>
        ) : (
          <Pill tone="unknown">{t.triggers.disabled}</Pill>
        )}
      </div>
      {tr.disabled_reason && (
        <ReviewNotice title={t.triggers.pausedForReview} hint={t.triggers.pausedForReviewHint} linkLabel={t.triggers.openReview} href={reviewHref(org, tr.review_request_id)} />
      )}
      {editing ? (
        <TriggerForm target={target} initial={tr} submitLabel={t.common.save} pending={commands.saving} error={saveError} onSubmit={(b) => void onSave(b)} onCancel={() => setEditing(false)} />
      ) : (
        <>
          <dl className="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-4 gap-y-1.5 text-[13px]">
            <Fact label={p.url}>
              <span className="flex min-w-0 items-center gap-1">
                <code className="min-w-0 truncate font-mono text-xs">{tr.url}</code>
                <Button type="button" variant="ghost" aria-label={t.triggers.copyUrl} onClick={() => void copy(tr.url)}>
                  <Copy size={13} />
                </Button>
              </span>
            </Fact>
            <Fact label={p.sender}>{presetLabel(tr.preset, t.triggers.preset)}</Fact>
            <Fact label={p.timestamp}>{tr.timestamp_header ? <code className="font-mono text-xs">{tr.timestamp_header}</code> : <span className="text-muted-foreground">{p.noTimestamp}</span>}</Fact>
            <Fact label={p.sources}>
              <span title={sources.join(', ') || undefined}>{sources.length === 0 ? p.anySource : p.sourceCount(sources.length)}</span>
            </Fact>
            <Fact label={p.onActive}>
              {(tr.settle_seconds ?? 0) > 0 ? t.triggers.settle.onActive : t.triggers.form.onActive[tr.on_active === 'skip' ? 'skip' : 'queue']}
            </Fact>
            <Fact label={t.triggers.settle.label}>{settle}</Fact>
          </dl>
          <PendingRun org={org} trigger={tr} canRun={canRun} canDiscard={canWrite} />
          <div className="flex flex-wrap gap-2">
            {canWrite && (
              <>
                <Button type="button" onClick={() => setEditing(true)}>
                  {p.edit}
                </Button>
                <Button type="button" onClick={() => void onRotate()} disabled={commands.rotating}>
                  {p.newSecret}
                </Button>
              </>
            )}
            <Button type="button" variant="ghost" aria-expanded={deliveries} onClick={() => setDeliveries((d) => !d)}>
              {deliveries ? p.hideDeliveries : p.deliveries}
            </Button>
            {canWrite && (
              <Button type="button" variant="destructive" className="ml-auto" disabled={commands.removing} onClick={() => void commands.remove()}>
                {t.common.delete}
              </Button>
            )}
          </div>
          {deliveries && (
            <section aria-label={p.deliveriesOf(tr.name)}>
              <Deliveries org={org} id={tr.id} />
            </section>
          )}
        </>
      )}
    </article>
  );
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0">{children}</dd>
    </>
  );
}

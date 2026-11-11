import { useState } from 'react';
import { Copy } from 'lucide-react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { useCopy } from '../../ui/useCopy.ts';
import { useToast } from '../../ui/toastContext.ts';
import type { Trigger, TriggerInput } from '../../api/types.ts';
import {
  type TriggerTarget,
  useCreateTrigger,
  useDeleteTrigger,
  useRotateTriggerSecret,
  useTestTrigger,
  useTriggers,
  useUpdateTrigger,
} from '../../api/hooks/useTriggers.ts';
import { Deliveries } from './Deliveries.tsx';
import { TriggerForm } from './TriggerForm.tsx';
import { presetLabel } from './presets.ts';
import { outcomeLabel } from './outcome.ts';
import { useT } from '../../i18n/useT.ts';
import { snippets } from './snippets.ts';
import { useConfirm } from '../../ui/confirmContext.ts';

/** TriggersDialog is the mockup's Triggers card for one job template or
 * git repository: every incoming webhook with URL, sender, allowlist,
 * mapping, rate limit and its delivery log, plus create, edit, rotate,
 * test call and the sender setup snippets. */
export function TriggersDialog({ org, target, canWrite, canRun, onClose }: {
  org: string;
  target: TriggerTarget;
  canWrite: boolean;
  canRun: boolean;
  onClose: () => void;
}) {
  const t = useT();
  const m = t.triggers;
  const triggers = useTriggers(org, target);
  const create = useCreateTrigger(org);
  const [creating, setCreating] = useState(false);
  const [secret, setSecret] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const list = triggers.data?.triggers ?? [];

  async function onCreate(body: TriggerInput) {
    setError(null);
    try {
      const res = await create.mutateAsync(body);
      setSecret(res.secret);
      setCreating(false);
    } catch (err) {
      setError(err instanceof Error ? err.message : m.createFailed);
    }
  }

  return (
    <Dialog open onClose={onClose} title={m.dialogTitle(target.name)} size="wide">
      <div className="flex flex-col gap-4">
        <p className="text-xs text-muted-foreground">
          {target.kind === 'template_run' ? m.hintRun : m.hintRepo}
        </p>
        {secret && <SecretBox secret={secret} onDone={() => setSecret(null)} />}
        {list.map((t) => (
          <TriggerCard key={t.id} org={org} target={target} trigger={t} canWrite={canWrite} canRun={canRun} onSecret={setSecret} />
        ))}
        {list.length === 0 && !creating && <p className="text-xs text-muted-foreground">{m.none}</p>}
        {creating ? (
          <TriggerForm target={target} submitLabel={m.createTrigger} pending={create.isPending} error={error} onSubmit={(b) => void onCreate(b)} onCancel={() => setCreating(false)} />
        ) : (
          canWrite && (
            <Button type="button" variant="primary" onClick={() => setCreating(true)}>
              {m.newTrigger}
            </Button>
          )
        )}
      </div>
    </Dialog>
  );
}

/** SecretBox shows a trigger secret exactly once. */
function SecretBox({ secret, onDone }: { secret: string; onDone: () => void }) {
  const m = useT().triggers;
  const copy = useCopy();
  return (
    <div className="flex flex-col gap-2 rounded-md border border-[var(--px-warn)] p-3">
      <p className="text-xs font-semibold">{m.secretOnce}</p>
      <div className="flex items-center gap-2">
        <code className="flex-1 truncate font-mono text-xs">{secret}</code>
        <Button type="button" variant="ghost" aria-label={m.copySecret} onClick={() => void copy(secret)}>
          <Copy size={13} />
        </Button>
      </div>
      <Button type="button" onClick={onDone}>
        {m.stored}
      </Button>
    </div>
  );
}

function TriggerCard({ org, target, trigger: t, canWrite, canRun, onSecret }: {
  org: string;
  target: TriggerTarget;
  trigger: Trigger;
  canWrite: boolean;
  canRun: boolean;
  onSecret: (s: string) => void;
}) {
  const i18n = useT();
  const m = i18n.triggers;
  const toast = useToast();
  const ask = useConfirm();
  const copy = useCopy();
  const update = useUpdateTrigger(org, t.id);
  const remove = useDeleteTrigger(org);
  const rotate = useRotateTriggerSecret(org);
  const test = useTestTrigger(org);
  const [editing, setEditing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [tab, setTab] = useState<'curl' | 'github' | 'gitlab' | 'gitea'>('curl');
  const sn = snippets(t, i18n);
  const mapping = Object.entries(t.mapping.extra_vars ?? {});

  async function onRotate() {
    if (!(await ask({ message: m.rotateConfirm(t.name), confirmLabel: m.rotate }))) return;
    try {
      const res = await rotate.mutateAsync(t.id);
      onSecret(res.secret);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : m.rotateFailed, 'destructive');
    }
  }

  async function onDelete() {
    if (!(await ask({ message: m.deleteConfirm(t.name), confirmLabel: i18n.common.delete, destructive: true }))) return;
    try {
      await remove.mutateAsync(t.id);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : m.deleteFailed, 'destructive');
    }
  }

  async function onTest() {
    if (!(await ask({ message: m.testConfirm[t.kind === 'repo_sync' ? 'repo_sync' : 'template_run'](t.name) }))) return;
    try {
      const res = await test.mutateAsync({ id: t.id });
      toast.show(m.testResult(outcomeLabel(res.outcome, i18n)));
    } catch (err) {
      toast.show(err instanceof Error ? err.message : m.testFailed, 'destructive');
    }
  }

  async function onSave(body: TriggerInput) {
    setError(null);
    try {
      await update.mutateAsync({ ...body, revision: t.revision });
      setEditing(false);
    } catch (err) {
      setError(err instanceof Error ? err.message : m.savingFailed);
    }
  }

  if (editing) {
    return <TriggerForm target={target} initial={t} submitLabel={i18n.common.save} pending={update.isPending} error={error} onSubmit={(b) => void onSave(b)} onCancel={() => setEditing(false)} />;
  }

  return (
    <div className="flex flex-col gap-2 rounded-md border border-border p-3">
      <div className="flex items-center justify-between">
        <span className="text-sm font-semibold">{t.name}</span>
        <span className="text-xs text-muted-foreground">{t.enabled ? m.enabled : m.disabled}</span>
      </div>
      <div className="flex items-center gap-2">
        <span className="text-xs font-semibold">POST</span>
        <code className="flex-1 truncate font-mono text-xs">{t.url}</code>
        <Button type="button" variant="ghost" aria-label={m.copyUrl} onClick={() => void copy(t.url)}>
          <Copy size={13} />
        </Button>
      </div>
      <p className="text-xs">{m.auth(presetLabel(t.preset, m.preset), t.signature_header)}</p>
      {t.timestamp_header && <p className="text-xs">{m.replay(t.timestamp_header)}</p>}
      <div className="flex flex-wrap gap-1 text-xs">
        <span>{m.allowedSources}</span>
        {(t.allow_cidrs ?? []).length === 0 ? (
          <span className="text-muted-foreground">{m.anySource}</span>
        ) : (
          (t.allow_cidrs ?? []).map((c) => (
            <span key={c} className="font-mono">
              {c}
            </span>
          ))
        )}
      </div>
      {(mapping.length > 0 || t.mapping.limit) && (
        <ul className="font-mono text-xs">
          {mapping.map(([name, path]) => (
            <li key={name}>{`${path} → ${name}`}</li>
          ))}
          {t.mapping.limit && <li>{`${t.mapping.limit} → limit`}</li>}
        </ul>
      )}
      <p className="text-xs">{m.rate(t.rate_limit.count, t.rate_limit.per, t.on_active === 'skip' ? m.rateAction.skip : m.rateAction.queue)}</p>
      {canWrite && (
        <div className="flex flex-wrap gap-2">
          <Button type="button" onClick={() => void onRotate()}>
            {m.rotate}
          </Button>
          {(canRun || t.kind === 'repo_sync') && (
            <Button
              type="button"
              title={m.testTitle[t.kind === 'repo_sync' ? 'repo_sync' : 'template_run']}
              onClick={() => void onTest()}
            >
              {m.testCall}
            </Button>
          )}
          <Button type="button" onClick={() => setEditing(true)}>
            {m.edit}
          </Button>
          <Button type="button" variant="destructive" disabled={remove.isPending} onClick={() => void onDelete()}>
            {i18n.common.delete}
          </Button>
        </div>
      )}
      <Deliveries org={org} id={t.id} />
      <details className="text-xs">
        <summary className="cursor-pointer">{m.senderSetup}</summary>
        <div role="tablist" className="mt-2 flex gap-3">
          {(['curl', 'github', 'gitlab', 'gitea'] as const).map((k) => (
            <button key={k} type="button" role="tab" aria-selected={tab === k} className={tab === k ? 'font-semibold text-primary' : 'text-muted-foreground'} onClick={() => setTab(k)}>
              {k}
            </button>
          ))}
        </div>
        <pre className="mt-2 overflow-x-auto rounded-md border border-border bg-background p-2 font-mono text-[11px]">{sn[tab]}</pre>
      </details>
    </div>
  );
}

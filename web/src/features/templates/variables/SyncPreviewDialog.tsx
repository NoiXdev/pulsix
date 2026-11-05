import { useEffect, useState, type ReactNode } from 'react';
import { Dialog } from '../../../ui/dialog.tsx';
import { Button } from '../../../ui/button.tsx';
import { Pill, Tag } from '../../../ui/badge.tsx';
import { QueryLoading } from '../../../ui/QueryState.tsx';
import { errorStatus } from '../../../lib/apiError.ts';
import { useApplyTemplateVariables, usePreviewTemplateVariables } from '../../../api/hooks/useTemplateVariables.ts';
import type { VariablesAdded, VariablesChange, VariablesNote, VariablesPreview } from '../../../api/types.ts';
import { SchemaForm } from '../../variables/SchemaForm.tsx';
import { formatAttribute } from '../../variables/fieldEditor.ts';
import { isActive } from '../../variables/origin.ts';
import { problemsFromError } from '../../variables/paths.ts';
import type { Problem, SchemaField, Vars } from '../../variables/types.ts';
import type { Messages } from '../../../i18n/types.ts';
import { useT } from '../../../i18n/useT.ts';
import { unavailableReason } from './sourceNotices.ts';

export interface SyncPreviewDialogProps {
  org: string;
  templateId: string;
  /** The revision the page's view is based on (If-Match of the apply). */
  revision: number;
  /** The mode the variables are in after the apply. */
  mode: 'manual' | 'automatic';
  onClose: () => void;
  onApplied: () => void;
  /** Reads the page again after a 412 (and closes this dialog). */
  onReload: () => void;
}

/** A held change asks for its value again when its type, its secret flag
 * or the stored value's fit changed (not for a limit's cut). */
const REENTER = new Set(['type', 'value', 'secret']);

/** wantsValue: an added field the apply checks as required with no
 * default to fall back to, so the person enters its value here. */
function wantsValue(f: SchemaField): boolean {
  return isActive(f) && Boolean(f.required) && f.default === undefined;
}

/** asksAdded: an added field this dialog asks a value for — a new
 * required one (wantsValue), or an active one held because the value the
 * template kept under its name no longer fits or was a secret. */
function asksAdded(a: VariablesAdded): boolean {
  const f = a.field as SchemaField;
  return wantsValue(f) || (isActive(f) && Boolean(a.reason && REENTER.has(a.reason)));
}

/** Held is the "needs review" mark of a held change or addition, with why. */
function Held({ needsReview, reason, t }: { needsReview: boolean; reason?: string; t: Messages['templates']['preview'] }) {
  return (
    <>
      {needsReview && <Pill tone="warn">{t.needsReview}</Pill>}
      {reason && reason in t.reasons && <span className="text-[11px] text-muted-foreground">{t.reasons[reason as keyof typeof t.reasons]}</span>}
    </>
  );
}

/** The top-level name a problem path (port, vhosts[0].domain) is under. */
const rootOf = (path: string) => path.split(/[.[]/)[0];

type Failure = null | 'changed' | 'stale' | string;

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section aria-label={title} className="flex flex-col gap-2">
      <h3 className="text-xs font-semibold">{title}</h3>
      {children}
    </section>
  );
}

function ChangeList({ changes }: { changes: readonly VariablesChange[] | null }) {
  return (
    <ul className="flex flex-col gap-0.5 pl-3 text-[11px] text-muted-foreground">
      {(changes ?? []).map((c) => (
        <li key={c.attr} className="flex gap-2">
          <span className="font-mono">{c.attr}</span>
          <span>{`${formatAttribute(c.attr, c.old)} → ${formatAttribute(c.attr, c.new)}`}</span>
        </li>
      ))}
    </ul>
  );
}

/** noteText is a note in the page's language where its code has words of
 * its own (the left-out count), else the server's message. */
function noteText(note: VariablesNote, p: VariablesPreview, t: Messages['templates']['preview']): string {
  if (note.code === 'fields_left_out') return t.leftOut(p.left_out);
  return note.message;
}

/** entered is what the person typed for the held changes: empty inputs are
 * left out, so the server keeps (or refuses) the stored value. */
function entered(values: Vars): Vars {
  return Object.fromEntries(Object.entries(values).filter(([, v]) => v !== undefined && v !== ''));
}

/**
 * SyncPreviewDialog is "Sync now" and the first link of a template's
 * variables (spec 7.75 goal 5): it asks the server what applying the
 * playbook's variable sources would change — added fields with the
 * activation they get (one held for review over a value the template kept
 * says why, and asks for the value again), changed fields attribute by attribute (a change held
 * for review says why, and asks again for a value that may no longer
 * fit; a new required field without a default asks for its value),
 * adjusted fields with the source's new version, gone fields (removed only
 * when ticked) and the parser's warnings — and applies it.
 *
 * The apply names the preview's source revision and sync time exactly as
 * the server sent them (the time is passed on as the string it arrived as:
 * the server compares it to the microsecond, which a JS Date would cut), so
 * a source that moved since answers 409 and the person previews again.
 */
export function SyncPreviewDialog({ org, templateId, revision, mode, onClose, onApplied, onReload }: SyncPreviewDialogProps) {
  const t = useT();
  const p = t.templates.preview;
  const preview = usePreviewTemplateVariables(org, templateId);
  const apply = useApplyTemplateVariables(org, templateId);
  const [removeGone, setRemoveGone] = useState(false);
  const [values, setValues] = useState<Vars>({});
  const [problems, setProblems] = useState<Problem[]>([]);
  const [failure, setFailure] = useState<Failure>(null);
  const { mutate: runPreview } = preview;
  useEffect(() => runPreview(), [runPreview]);

  function previewAgain() {
    setFailure(null);
    setProblems([]);
    setValues({});
    runPreview();
  }

  async function onApply() {
    const data = preview.data;
    if (!data) return;
    setFailure(null);
    setProblems([]);
    try {
      await apply.mutateAsync({
        mode,
        sourceRev: data.preview.source_rev,
        sourceSyncedAt: data.source.synced_at,
        removeGone,
        values: entered(values),
        revision,
      });
      onApplied();
    } catch (err) {
      const status = errorStatus(err);
      if (status === 409) return setFailure('changed');
      if (status === 412) return setFailure('stale');
      const ps = problemsFromError(err, 'body.values');
      if (ps.length > 0) return setProblems(ps);
      setFailure(err instanceof Error ? err.message : p.applyFailed);
    }
  }

  const data = preview.data;
  const pv = data?.preview;
  const added = pv?.added ?? [];
  const changed = pv?.changed ?? [];
  const adjusted = pv?.adjusted ?? [];
  const gone = pv?.gone ?? [];
  // Why nothing can be applied, in the page's language; it stands in for
  // the server's source_unavailable note (English text).
  const unavailable = data ? unavailableReason(data.source, t.templates.source) : null;
  const notes = (pv?.notes ?? []).filter((n) => !(unavailable && n.code === 'source_unavailable'));
  const nothing = added.length + changed.length + adjusted.length + gone.length === 0;
  const valueFor = (f: SchemaField) => (
    <div className="pl-3">
      <SchemaForm fields={[{ ...f, active: true }]} value={values} onChange={setValues} problems={problems} idPrefix="sync-value" />
    </div>
  );
  // Problems at a field with no input here (the server refused a value
  // this dialog did not ask for) are listed, never dropped.
  const asked = new Set([
    ...added.filter(asksAdded).map(({ field }) => field.name),
    ...changed.filter((c) => c.reason && REENTER.has(c.reason)).map((c) => c.name),
  ]);
  const unplaced = problems.filter((x) => !asked.has(rootOf(x.path)));

  return (
    <Dialog open onClose={onClose} size="wide" title={p.title}>
      <div className="flex max-h-[70vh] flex-col gap-4 overflow-y-auto">
        {preview.isPending && <QueryLoading />}
        {preview.isError && (
          <p role="alert" className="text-xs text-destructive">
            {preview.error instanceof Error ? preview.error.message : p.failed}
          </p>
        )}
        {unavailable && (
          <p role="status" className="rounded-md border border-border px-3 py-2 text-xs">
            {unavailable}
          </p>
        )}
        {pv && data && (
          <>
            {added.length > 0 && (
              <Section title={p.added}>
                <ul className="flex flex-col gap-1 text-xs">
                  {added.map((a) => (
                    <li key={a.field.name} className="flex flex-col gap-1">
                      <span className="flex flex-wrap items-center gap-2">
                        <span className="font-mono">{a.field.name}</span>
                        <span className="text-muted-foreground">{a.field.type}</span>
                        <Tag>{isActive(a.field as SchemaField) ? p.active : p.inactive}</Tag>
                        <Held needsReview={a.needs_review} reason={a.reason} t={p} />
                      </span>
                      {asksAdded(a) && valueFor(a.field as SchemaField)}
                    </li>
                  ))}
                </ul>
              </Section>
            )}
            {changed.length > 0 && (
              <Section title={p.changed}>
                <ul className="flex flex-col gap-2 text-xs">
                  {changed.map((c) => (
                    <li key={c.name} className="flex flex-col gap-1">
                      <span className="flex flex-wrap items-center gap-2">
                        <span className="font-mono">{c.name}</span>
                        <Held needsReview={c.needs_review} reason={c.reason} t={p} />
                      </span>
                      <ChangeList changes={c.changes} />
                      {c.reason && REENTER.has(c.reason) && valueFor(c.field as SchemaField)}
                    </li>
                  ))}
                </ul>
              </Section>
            )}
            {adjusted.length > 0 && (
              <Section title={p.adjusted}>
                <p className="text-[11px] text-muted-foreground">{p.adjustedHint}</p>
                <ul className="flex flex-col gap-2 text-xs">
                  {adjusted.map((a) => (
                    <li key={a.name} className="flex flex-col gap-1">
                      <span className="font-mono">{a.name}</span>
                      <ChangeList changes={a.changes} />
                    </li>
                  ))}
                </ul>
              </Section>
            )}
            {gone.length > 0 && (
              <Section title={p.gone}>
                <ul className="flex flex-wrap gap-2 text-xs">
                  {gone.map((g) => (
                    <li key={g.name} className="font-mono">
                      {g.name}
                    </li>
                  ))}
                </ul>
                <label className="flex items-center gap-2 text-xs">
                  <input type="checkbox" checked={removeGone} onChange={(e) => setRemoveGone(e.target.checked)} />
                  {p.removeGone}
                </label>
                <span className="text-[11px] text-muted-foreground">{p.removeGoneHint}</span>
              </Section>
            )}
            {nothing && !unavailable && <p className="text-xs text-muted-foreground">{p.nothing}</p>}
            <p className="text-xs text-muted-foreground">{p.unchanged(pv.unchanged)}</p>
            {pv.left_out > 0 && !notes.some((n) => n.code === 'fields_left_out') && <p className="text-xs">{p.leftOut(pv.left_out)}</p>}
            {notes.length > 0 && (
              <Section title={p.notes}>
                <ul className="flex flex-col gap-1 text-[11px]">
                  {notes.map((n, i) => (
                    <li key={i} className="flex flex-wrap gap-2">
                      <Tag>{n.level === 'hint' ? p.levels.hint : p.levels.warning}</Tag>
                      {n.file && <span className="font-mono text-muted-foreground">{n.line ? `${n.file}:${n.line}` : n.file}</span>}
                      <span>{noteText(n, pv, p)}</span>
                    </li>
                  ))}
                </ul>
              </Section>
            )}
          </>
        )}
        {unplaced.length > 0 && (
          <ul role="alert" className="flex flex-col gap-0.5 text-xs text-destructive">
            {unplaced.map((x) => (
              <li key={x.path}>{x.path ? `${x.path}: ${x.message}` : x.message}</li>
            ))}
          </ul>
        )}
        {failure === 'changed' && (
          <div role="alert" className="flex items-center justify-between gap-3 rounded-md border border-border px-3 py-2 text-xs">
            <span>{p.sourceChanged}</span>
            <Button type="button" onClick={previewAgain}>
              {p.previewAgain}
            </Button>
          </div>
        )}
        {failure === 'stale' && (
          <div role="alert" className="flex items-center justify-between gap-3 rounded-md border border-border px-3 py-2 text-xs">
            <span>{p.stale}</span>
            <Button type="button" onClick={onReload}>
              {p.reload}
            </Button>
          </div>
        )}
        {failure && failure !== 'changed' && failure !== 'stale' && (
          <p role="alert" className="text-xs text-destructive">
            {failure}
          </p>
        )}
        <div className="flex justify-end gap-2 border-t border-border pt-3">
          <Button type="button" onClick={onClose}>
            {t.common.cancel}
          </Button>
          <Button
            type="button"
            variant="primary"
            disabled={!data?.source.available || preview.isPending || apply.isPending || failure === 'changed' || failure === 'stale'}
            onClick={() => void onApply()}
          >
            {p.apply}
          </Button>
        </div>
      </div>
    </Dialog>
  );
}

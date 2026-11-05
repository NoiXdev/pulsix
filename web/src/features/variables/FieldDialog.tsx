import { useState, type FormEvent } from 'react';
import { createPortal } from 'react-dom';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Tag } from '../../ui/badge.tsx';
import { FieldAttributes } from './FieldAttributes.tsx';
import { OriginPill } from './OriginPill.tsx';
import { editLocked, keepAsManual, originOf, resetToSource, sourceDifferences } from './origin.ts';
import { formatAttribute } from './fieldEditor.ts';
import type { EditorVariant, FieldSource, Problem, SchemaField } from './types.ts';
import type { Messages } from '../../i18n/types.ts';
import { useT } from '../../i18n/useT.ts';

export interface FieldDialogProps {
  /** The field as it is now; a new field arrives already named. */
  field: SchemaField;
  /** Server path of the field, for its problems. */
  path: string;
  variant: EditorVariant;
  /** A sub-field: no provenance of its own. */
  nested?: boolean;
  /** No new sub-fields (the top-level field is locked to its source). */
  subFieldsLocked?: boolean;
  problems?: readonly Problem[];
  readOnly?: boolean;
  onSave: (field: SchemaField) => void;
  /** Absent for a field that is not in the schema yet. */
  onRemove?: () => void;
  onClose: () => void;
}

function sourceLine(src: FieldSource, t: Messages): string {
  let kind = src.kind === 'argument_spec' && src.role ? t.variables.field.roleSpec(src.role) : t.variables.field.kinds[src.kind];
  if (src.entry_point && src.entry_point !== 'main') kind = `${kind} (${src.entry_point})`;
  return t.variables.field.fromSource(kind, src.line ? `${src.file}:${src.line}` : src.file);
}

/**
 * FieldDialog edits one field on a draft: nothing reaches the schema until
 * Done. A top-level field shows where it came from; an edit of a field
 * locked to its source shows it as adjusted right away, with what the
 * source says beside each attribute that now differs and a way back.
 *
 * It renders into document.body: the editor sits inside other forms (the
 * job template dialog), and a form nested in a form would hand its submit
 * to the outer one. React still passes the submit up the component tree,
 * so the handler stops it here.
 */
export function FieldDialog({ field, path, variant, nested = false, subFieldsLocked = false, problems, readOnly, onSave, onRemove, onClose }: FieldDialogProps) {
  const t = useT();
  const [draft, setDraft] = useState(field);
  const shown = nested ? draft : editLocked(field, draft);
  const origin = originOf(shown);
  const says: Record<string, string> = {};
  if (!nested && origin === 'adjusted') {
    for (const [k, v] of sourceDifferences(shown)) says[k] = t.variables.field.sourceSays(formatAttribute(k, v));
  }

  function submit(e: FormEvent) {
    e.preventDefault();
    e.stopPropagation();
    if (!readOnly) onSave(shown);
  }

  return createPortal(
    <Dialog open onClose={onClose} size="wide" title={t.variables.builder.fieldGroup(draft.name)}>
      <form onSubmit={submit} className="flex max-h-[70vh] flex-col gap-4 overflow-y-auto">
        {!nested && (variant === 'template' || shown.source) && (
          <div className="flex flex-wrap items-center gap-2">
            {variant === 'template' && <OriginPill origin={origin} />}
            {shown.set_in_playbook && <Tag>{t.variables.field.setInPlaybook}</Tag>}
            {shown.source && <span className="font-mono text-[11px] text-muted-foreground">{sourceLine(shown.source, t)}</span>}
          </div>
        )}
        {!nested && origin === 'source' && <p className="text-[11px] text-muted-foreground">{t.variables.field.lockedNote}</p>}
        {!nested && origin === 'gone' && <p className="text-[11px] text-muted-foreground">{t.variables.field.goneNote}</p>}
        <fieldset disabled={readOnly} className="min-w-0">
          <FieldAttributes
            field={draft}
            path={path}
            idBase={`fd-${path.replace(/[^a-zA-Z0-9]/g, '-')}`}
            nested={nested}
            variant={variant}
            problems={problems}
            nameLocked={!nested && origin !== 'manual'}
            subFieldsLocked={subFieldsLocked}
            says={says}
            onChange={setDraft}
          />
        </fieldset>
        <div className="flex flex-wrap items-center gap-2 border-t border-border pt-3">
          {!readOnly && onRemove && origin === 'manual' && (
            <Button type="button" variant="destructive" onClick={onRemove}>
              {t.variables.builder.deleteField}
            </Button>
          )}
          {!readOnly && !nested && origin === 'gone' && (
            <>
              {onRemove && (
                <Button type="button" variant="destructive" onClick={onRemove}>
                  {t.variables.field.remove}
                </Button>
              )}
              <Button type="button" onClick={() => setDraft(keepAsManual(shown))}>
                {t.variables.field.keepAsManual}
              </Button>
            </>
          )}
          {!readOnly && !nested && origin === 'adjusted' && shown.source_field && (
            <Button type="button" onClick={() => setDraft(resetToSource(shown))}>
              {t.variables.field.resetToSource}
            </Button>
          )}
          <div className="ml-auto flex gap-2">
            <Button type="button" onClick={onClose}>
              {t.common.cancel}
            </Button>
            {!readOnly && (
              <Button type="submit" variant="primary">
                {t.variables.field.done}
              </Button>
            )}
          </div>
        </div>
      </form>
    </Dialog>,
    document.body,
  );
}

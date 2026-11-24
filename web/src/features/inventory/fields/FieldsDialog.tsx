import { useRef } from 'react';
import { Button } from '../../../ui/button.tsx';
import { Dialog } from '../../../ui/dialog.tsx';
import { useConfirm } from '../../../ui/confirmContext.ts';
import type { InventoryGroup } from '../../../api/types.ts';
import { StaleNotice } from '../sections/StaleNotice.tsx';
import { FieldsEditor } from './FieldsEditor.tsx';
import { useSchemaDraft } from './useSchemaDraft.ts';
import { useT } from '../../../i18n/useT.ts';
import { useAskOnEscape } from '../../shared/useAskOnEscape.ts';

export interface FieldsDialogProps {
  org: string;
  group: InventoryGroup;
  canWrite: boolean;
  open: boolean;
  onClose: () => void;
}

/**
 * FieldsDialog is the group's schema editor (spec 2026-11-18, "Manage
 * fields dialog"): the field list and the selected field's panel
 * (FieldsEditor) above a footer with the unsaved hint, Cancel and Save.
 * Save is off until the draft is edited and closes the dialog once what it
 * stored is the draft; closing an edited draft — Cancel or Escape — asks
 * first and discards it. A reader gets the same view read-only, titled
 * "Fields", with only Close.
 *
 * It stays mounted while closed (the page renders it always), so the
 * draft's revision keeps following the group while nobody edits it.
 */
export function FieldsDialog({ org, group, canWrite, open, onClose }: FieldsDialogProps) {
  const t = useT();
  const f = t.inventory.fields;
  const ask = useConfirm();
  const s = useSchemaDraft(org, group);
  const dirty = canWrite && s.touched;

  async function requestClose() {
    if (dirty) {
      if (!(await ask({ title: f.discardTitle, message: f.discardBody, confirmLabel: f.discard, destructive: true }))) return;
      s.discard();
    }
    onClose();
  }

  async function save() {
    if (await s.save()) onClose();
  }

  // Escape asks while there are changes, wherever the focus is — in the
  // content or on <body> after a click on the title bar or text (see
  // useAskOnEscape). A picker or menu that closes itself on Escape keeps
  // that press; so does a sub-field's own dialog, which gets its own.
  const content = useRef<HTMLDivElement>(null);
  useAskOnEscape(content, open, dirty, () => void requestClose());

  // The fallback for a close request that comes without that keydown (the
  // browser's own close gesture): refused and the question asked instead.
  function onCancel(): boolean {
    if (!dirty) return true;
    void requestClose();
    return false;
  }

  return (
    <Dialog open={open} onClose={onClose} onCancel={onCancel} title={canWrite ? f.manageTitle : f.viewTitle} size="xl">
      <div ref={content} className="flex flex-col gap-4">
        {s.stale && <StaleNotice onReload={s.reload} />}
        {s.problems.length > 0 && (
          <p role="alert" className="text-xs text-destructive">
            {t.inventory.schemaTab.problems(s.problems.length)}
          </p>
        )}
        <FieldsEditor key={s.generation} schema={s.draft} onChange={s.edit} problems={s.problems} readOnly={!canWrite} />
        <div className="flex flex-wrap items-center gap-2 border-t border-border pt-3">
          {dirty && (
            <span className="flex items-center gap-1.5 text-xs text-[var(--px-warn)]">
              <span aria-hidden="true" className="h-1.5 w-1.5 rounded-full bg-[var(--px-warn)]" />
              {f.unsaved}
            </span>
          )}
          <div className="ml-auto flex gap-2">
            {canWrite ? (
              <>
                <Button type="button" onClick={() => void requestClose()}>
                  {f.cancel}
                </Button>
                <Button type="button" variant="primary" disabled={!dirty || s.saving} onClick={() => void save()}>
                  {f.save}
                </Button>
              </>
            ) : (
              <Button type="button" onClick={onClose}>
                {f.close}
              </Button>
            )}
          </div>
        </div>
      </div>
    </Dialog>
  );
}

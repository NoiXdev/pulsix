import type { KeyboardEvent } from 'react';
import { Button } from '../../../ui/button.tsx';
import { Dialog } from '../../../ui/dialog.tsx';
import { useConfirm } from '../../../ui/confirmContext.ts';
import type { InventoryGroup } from '../../../api/types.ts';
import { StaleNotice } from '../sections/StaleNotice.tsx';
import { FieldsEditor } from './FieldsEditor.tsx';
import { useSchemaDraft } from './useSchemaDraft.ts';
import { useT } from '../../../i18n/useT.ts';

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

  // Escape asks too while there are changes: preventing the keydown's
  // default is what stops the native <dialog>'s close request. A picker
  // or menu that closes itself on Escape has already prevented it, and
  // that press is left to it; so is one inside a sub-field's own dialog
  // (React bubbles it through this tree).
  function onKeyDown(e: KeyboardEvent<HTMLDivElement>) {
    if (e.key !== 'Escape' || e.defaultPrevented || !dirty) return;
    if ((e.target as Element).closest('dialog') !== e.currentTarget.closest('dialog')) return;
    e.preventDefault();
    void requestClose();
  }

  return (
    <Dialog open={open} onClose={onClose} title={canWrite ? f.manageTitle : f.viewTitle} size="xl">
      <div className="flex flex-col gap-4" onKeyDown={onKeyDown}>
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

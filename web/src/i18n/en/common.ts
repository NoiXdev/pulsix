/**
 * Words shared by many features. A string belongs here only when it means
 * the same thing everywhere it is used; anything a single feature says
 * goes into that feature's own module.
 */
export const enCommon = {
  save: 'Save',
  copied: 'Copied',
  copyUnavailable: 'Copying is not available here. Select the text and press Ctrl+C (Cmd+C on a Mac).',
  cancel: 'Cancel',
  rowActions: (name: string) => `Actions for ${name}`,
  sections: 'Sections',
  open: 'Open',
  close: 'Close',
  back: 'Back',
  continue: 'Continue',
  remove: 'Remove',
  delete: 'Delete',
  edit: 'Edit',
  loading: 'Loading…',
  /** The dash that joins two pieces of copy in a component ("Field 1 name — required"):
   * spaced em dash in English, spaced en dash in German (backlog 1.42). */
  dash: ' — ',
  never: 'never',
};

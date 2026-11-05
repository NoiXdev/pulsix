/**
 * Words shared by many features. A string belongs here only when it means
 * the same thing everywhere it is used; anything a single feature says
 * goes into that feature's own module.
 */
export const enCommon = {
  save: 'Save',
  cancel: 'Cancel',
  rowActions: (name: string) => `Actions for ${name}`,
  sections: 'Sections',
  open: 'Open',
  close: 'Close',
  back: 'Back',
  continue: 'Continue',
  remove: 'Remove',
  delete: 'Delete',
  loading: 'Loading…',
  never: 'never',
};

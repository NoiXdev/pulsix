/**
 * Form validation messages (lib/schemas.ts and the forms that build their
 * own zod schemas). They read as the continuation of a field label, hence
 * the lower case: "Name — required".
 */
export const enValidation = {
  required: 'required',
  maxChars: (max: number) => `must be ${max} characters or fewer`,
  minChars: (min: number) => `at least ${min} characters`,
  slugShape: 'lowercase letters, digits and single hyphens only',
  email: 'enter a valid email address',
  port: 'enter a port between 1 and 65535',
  selectZone: 'select a zone',
  passwordsMismatch: 'passwords do not match',
};

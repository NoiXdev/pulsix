/** isMaskedSecret reports a value the server masked: a secret extra var
 * arrives as {"$secret": "set"}, never with its value. */
export function isMaskedSecret(value: unknown): boolean {
  return (
    typeof value === 'object' &&
    value !== null &&
    !Array.isArray(value) &&
    Object.keys(value).length === 1 &&
    (value as Record<string, unknown>).$secret === 'set'
  );
}

/** varText renders an extra var's value: a string as it is, anything else
 * as JSON, with every masked secret inside it replaced by `secretLabel`. */
export function varText(value: unknown, secretLabel: string): string {
  if (isMaskedSecret(value)) return secretLabel;
  if (typeof value === 'string') return value;
  return JSON.stringify(value, (_key, v: unknown) => (isMaskedSecret(v) ? secretLabel : v));
}

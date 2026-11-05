import { ApiError } from '../../api/client.ts';
import type { FieldScope, Problem, SchemaField, VarSchema } from './types.ts';

export function childPath(prefix: string, name: string): string {
  return prefix ? `${prefix}.${name}` : name;
}

export function indexPath(prefix: string, index: number): string {
  return `${prefix}[${index}]`;
}

/** inputId turns a document path into a DOM id safe for `<label htmlFor>`.
 * Lives here (not SchemaForm.tsx) so that file exports only components,
 * per react-refresh/only-export-components — see web/src/ui/toast.tsx and
 * web/src/lib/apiError.ts for the same split. */
export function inputId(idPrefix: string, path: string): string {
  return `${idPrefix}-${path.replace(/[^a-zA-Z0-9_-]/g, '-')}`;
}

export function problemAt(problems: readonly Problem[] | undefined, path: string): string | undefined {
  return problems?.find((p) => p.path === path)?.message;
}

export function scopeOf(field: SchemaField): FieldScope {
  return field.scope ?? 'group';
}

export function groupFields(schema: VarSchema): SchemaField[] {
  return schema.fields.filter((f) => scopeOf(f) === 'group');
}

export function hostFields(schema: VarSchema): SchemaField[] {
  return schema.fields.filter((f) => scopeOf(f) === 'host');
}

/** What a membership may carry: host fields plus optional overrides of
 * overridable group fields (no schema default: an absent override means
 * "use the group value"), in schema order, exactly like the server. */
export function membershipFields(schema: VarSchema): SchemaField[] {
  return schema.fields.flatMap((f) => {
    if (scopeOf(f) === 'host') return [f];
    if (f.overridable) return [{ ...f, required: false, default: undefined }];
    return [];
  });
}

interface ErrorDetail {
  location?: string;
  message?: string;
}

/** problemsFromError turns a 422's details under location (for example
 * "body.vars") into problems with paths relative to that location. */
export function problemsFromError(error: unknown, location: string): Problem[] {
  if (!(error instanceof ApiError) || error.status !== 422) return [];
  const body = error.body as { errors?: ErrorDetail[] } | undefined;
  const prefix = `${location}.`;
  return (body?.errors ?? []).flatMap((e) => {
    if (!e.location || !e.message) return [];
    if (e.location === location) return [{ path: '', message: e.message }];
    if (e.location.startsWith(prefix)) return [{ path: e.location.slice(prefix.length), message: e.message }];
    return [];
  });
}

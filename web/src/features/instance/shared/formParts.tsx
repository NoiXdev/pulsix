import type { ReactNode } from 'react';

/** FormSection is one card of a shared-resource form page: the fields that
 * belong together, in a grid that uses the page's width. A titled section is
 * also a labelled region, so a screen reader can jump to it. */
export function FormSection({
  title,
  titleId,
  hint,
  children,
}: {
  title?: string;
  titleId?: string;
  hint?: string;
  children: ReactNode;
}) {
  return (
    <section
      aria-labelledby={title ? titleId : undefined}
      className="flex flex-col gap-4 rounded-[10px] border border-border bg-card p-5"
    >
      {title && (
        <h2 id={titleId} className="text-sm font-semibold">
          {title}
        </h2>
      )}
      {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
      {children}
    </section>
  );
}

/** FieldError is the message under a field the validation refused. */
export function FieldError({ children }: { children?: string }) {
  if (!children) return null;
  return (
    <span role="alert" className="text-[11px] text-destructive">
      {children}
    </span>
  );
}

/** FormError is the server's own answer to a refused save, shown whole: the
 * collision message names every colliding organization and the in-use
 * message names one plus what still references it — neither is about a
 * single field. */
export function FormError({ children }: { children: string | null }) {
  if (!children) return null;
  return (
    <p role="alert" className="text-xs text-destructive">
      {children}
    </p>
  );
}

/** The two-column grid a form section lays its fields on. */
export const FIELD_GRID = 'grid grid-cols-1 gap-4 md:grid-cols-2';

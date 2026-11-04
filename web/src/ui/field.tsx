import type { ReactNode } from 'react';
import { Label } from './label.tsx';

/**
 * Field is a labelled form control with an optional hint and error below
 * it. The error reads as the continuation of the label ("New password —
 * at least 12 characters"), as the validation messages are written.
 */
export function Field({
  id,
  label,
  hint,
  error,
  children,
}: {
  id: string;
  label: string;
  hint?: string;
  error?: string | null;
  children: ReactNode;
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      {children}
      {error ? (
        <span className="text-[11px] text-destructive">{error}</span>
      ) : (
        hint && <span className="text-[11px] text-muted-foreground">{hint}</span>
      )}
    </div>
  );
}

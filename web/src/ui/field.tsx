import { Children, cloneElement, isValidElement, type ReactElement, type ReactNode } from 'react';
import { Label } from './label.tsx';

/** The props Field sets on the control it labels. */
interface ControlProps {
  id?: string;
  'aria-describedby'?: string;
  'aria-invalid'?: boolean;
}

/**
 * Field is a labelled form control with an optional hint and error below
 * it. The error reads as the continuation of the label ("New password —
 * at least 12 characters"), as the validation messages are written.
 *
 * The control is the child whose `id` is the field's: it gets
 * `aria-describedby` (the error while there is one, otherwise the hint,
 * after whatever the control already points at) and, with an error,
 * `aria-invalid`. A child that forwards those to its input, as SlugField
 * does, works the same way. The error is announced as it appears
 * (`role="alert"`).
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
  const hintId = `${id}-hint`;
  const errorId = `${id}-error`;
  const describedBy = error ? errorId : hint ? hintId : undefined;

  const controls = Children.map(children, (child) => {
    if (!isValidElement<ControlProps>(child) || child.props.id !== id) return child;
    const own = child.props['aria-describedby'];
    const props: ControlProps = {};
    if (describedBy) props['aria-describedby'] = own ? `${own} ${describedBy}` : describedBy;
    if (error) props['aria-invalid'] = true;
    return cloneElement(child as ReactElement<ControlProps>, props);
  });

  return (
    <div className="flex flex-col gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      {controls}
      {error ? (
        <span id={errorId} role="alert" className="text-[11px] text-destructive">
          {error}
        </span>
      ) : (
        hint && (
          <span id={hintId} className="text-[11px] text-muted-foreground">
            {hint}
          </span>
        )
      )}
    </div>
  );
}

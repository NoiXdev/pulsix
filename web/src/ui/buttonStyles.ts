import { cn } from '../lib/utils.ts';

export type ButtonVariant = 'primary' | 'default' | 'ghost' | 'destructive';

const VARIANT: Record<ButtonVariant, string> = {
  primary: 'bg-primary text-primary-foreground hover:opacity-90',
  default: 'border border-border bg-card text-foreground hover:bg-accent',
  ghost: 'text-foreground hover:bg-accent',
  destructive: 'border border-destructive/40 text-destructive hover:bg-destructive/10',
};

/** The classes of a Button, for an element that must stay something else —
 * a router Link that looks like a button, since a button inside an anchor
 * is invalid HTML. */
export function buttonClass(variant: ButtonVariant = 'default', className?: string): string {
  return cn(
    'inline-flex h-[34px] items-center justify-center gap-2 rounded-md px-3.5 text-[13px] font-semibold transition-colors disabled:pointer-events-none disabled:opacity-50',
    VARIANT[variant],
    className,
  );
}

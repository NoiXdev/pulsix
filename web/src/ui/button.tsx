import type { ButtonHTMLAttributes } from 'react';
import { cn } from '../lib/utils.ts';

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: 'primary' | 'default' | 'ghost' | 'destructive';
}

const VARIANT: Record<NonNullable<ButtonProps['variant']>, string> = {
  primary: 'bg-primary text-primary-foreground hover:opacity-90',
  default: 'border border-border bg-card text-foreground hover:bg-accent',
  ghost: 'text-foreground hover:bg-accent',
  destructive: 'border border-destructive/40 text-destructive hover:bg-destructive/10',
};

/** Button — see design/mockups/build.mjs's `btn()` generator for the exact
 * shape (34px tall, 6px radius, primary = accent fill with ink text). */
export function Button({ variant = 'default', className, ...props }: ButtonProps) {
  return (
    <button
      className={cn(
        'inline-flex h-[34px] items-center justify-center gap-2 rounded-md px-3.5 text-[13px] font-semibold transition-colors disabled:pointer-events-none disabled:opacity-50',
        VARIANT[variant],
        className,
      )}
      {...props}
    />
  );
}

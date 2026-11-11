import type { ComponentProps } from 'react';
import { cn } from '../lib/utils.ts';

/** The text input every form uses. `ref` passes through (React 19 hands it
 *  over as an ordinary prop). */
export function Input({ className, ...props }: ComponentProps<'input'>) {
  return (
    <input
      className={cn(
        'flex h-8 w-full rounded-md border border-border bg-background px-2.5 text-xs text-foreground placeholder:text-muted-foreground focus-visible:border-primary focus-visible:outline-none disabled:opacity-50',
        className,
      )}
      {...props}
    />
  );
}

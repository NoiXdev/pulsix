import type { LabelHTMLAttributes } from 'react';
import { cn } from '../lib/utils.ts';

export function Label({ className, ...props }: LabelHTMLAttributes<HTMLLabelElement>) {
  return <label className={cn('text-[11px] font-medium text-muted-foreground', className)} {...props} />;
}

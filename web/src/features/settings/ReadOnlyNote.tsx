import type { ReactNode } from 'react';

/** ReadOnlyNote is the card footer a member without the write right sees in
 * place of the save bar. */
export function ReadOnlyNote({ children }: { children: ReactNode }) {
  return <p className="border-t border-border bg-background/40 px-5 py-3 text-[12.5px] text-muted-foreground">{children}</p>;
}

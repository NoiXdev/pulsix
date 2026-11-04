import type { ReactNode } from 'react';
import { PulsixMark } from './PulsixMark.tsx';

/**
 * AuthShell is the frame of the pages a person sees before they are
 * signed in — sign-in, accepting an invitation, a forgotten password: the
 * brand above one narrow centered column, and no app shell.
 */
export function AuthShell({ children }: { children: ReactNode }) {
  return (
    <div className="flex min-h-screen items-center justify-center bg-background px-4 text-foreground">
      <div className="flex w-full max-w-[380px] flex-col gap-7">
        <div className="flex items-center gap-3">
          <PulsixMark size={32} />
          <span className="font-mono text-2xl font-extrabold tracking-tight">
            pulsi<span className="text-brand">x</span>
          </span>
        </div>
        {children}
      </div>
    </div>
  );
}

/** AuthCard is the card inside AuthShell that holds a page's one form or
 * message. */
export function AuthCard({ children }: { children: ReactNode }) {
  return <div className="flex flex-col gap-4 rounded-xl border border-border bg-card p-5">{children}</div>;
}

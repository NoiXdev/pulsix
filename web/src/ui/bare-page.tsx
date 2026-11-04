import type { ReactNode } from 'react';
import { PulsixMark } from './PulsixMark.tsx';

/**
 * BarePage is the frame of a signed-in page that a session sees before it
 * may use the app — second-factor enrolment, replacing a temporary
 * password: the brand and one action (signing out) in a header, and the
 * page below, without the app shell, whose organization switcher such a
 * session could not load.
 */
export function BarePage({ action, children }: { action: ReactNode; children: ReactNode }) {
  return (
    <div className="min-h-screen bg-background text-foreground text-[13px]">
      <header className="flex items-center justify-between border-b border-border px-8 py-4">
        <div className="flex items-center gap-3">
          <PulsixMark size={28} />
          <span className="font-mono text-xl font-extrabold tracking-tight">
            pulsi<span className="text-brand">x</span>
          </span>
        </div>
        {action}
      </header>
      <main className="mx-auto flex max-w-[980px] flex-col gap-5 px-8 py-8">{children}</main>
    </div>
  );
}

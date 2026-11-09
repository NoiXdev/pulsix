import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { cn } from '../lib/utils.ts';
import { ToastContext } from './toastContext.ts';

interface ToastMessage {
  id: number;
  text: string;
  tone: 'default' | 'warn' | 'destructive';
}

let nextId = 1;

/** ToastProvider renders transient, auto-dismissing notifications (a
 * mutation's success/failure) — a hand-rolled stand-in for shadcn's
 * toast, avoiding a Radix dependency for something this small. Mount it
 * once near the app root (see src/main.tsx). Use the useToast hook from
 * ./toastContext.ts to trigger one — split into its own module so this
 * file exports only the component (react-refresh/only-export-components). */
export function ToastProvider({ children }: { children: ReactNode }) {
  const [messages, setMessages] = useState<ToastMessage[]>([]);
  // The pending dismissal timers, cleared when the provider unmounts: one
  // that fired afterwards would update state of an unmounted component (and,
  // in a test run, fire after the environment was torn down).
  const timers = useRef(new Set<ReturnType<typeof setTimeout>>());

  useEffect(() => {
    const pending = timers.current;
    return () => {
      pending.forEach(clearTimeout);
      pending.clear();
    };
  }, []);

  const show = useCallback((text: string, tone: ToastMessage['tone'] = 'default') => {
    const id = nextId++;
    setMessages((prev) => [...prev, { id, text, tone }]);
    const timer = setTimeout(() => {
      timers.current.delete(timer);
      setMessages((prev) => prev.filter((m) => m.id !== id));
    }, 4000);
    timers.current.add(timer);
  }, []);

  return (
    <ToastContext.Provider value={{ show }}>
      {children}
      <div className="fixed right-4 bottom-4 z-50 flex flex-col gap-2">
        {messages.map((m) => (
          <div
            key={m.id}
            role="status"
            data-tone={m.tone}
            className={cn(
              'rounded-md border px-4 py-2.5 text-[13px] font-medium shadow-lg',
              m.tone === 'destructive' && 'border-destructive/40 bg-card text-destructive',
              m.tone === 'warn' && 'border-[var(--px-warn)] bg-card text-[var(--px-warn)]',
              m.tone === 'default' && 'border-border bg-card text-foreground',
            )}
          >
            {m.text}
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  );
}

import type { ReactNode } from 'react';
import { Bot } from 'lucide-react';
import { Pill } from '../../ui/badge.tsx';
import { useT } from '../../i18n/useT.ts';

/** AssistantMark says, beside a value, that an assistant wrote it and that
 * a run receives it as plain text until a person changes or accepts it
 * (decision D6: the server renders such values inert). The caller picks
 * the text and may add an action (Accept as live) and a note beside it
 * (why the action waits). */
export function AssistantMark({ text, testId, action, note }: { text: string; testId?: string; action?: ReactNode; note?: string }) {
  return (
    <div data-testid={testId} className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[12px] text-warn">
      <span className="flex items-start gap-1.5">
        <Bot size={13} aria-hidden="true" className="mt-px shrink-0" />
        <span>{text}</span>
      </span>
      {action}
      {note && <span className="text-muted-foreground">{note}</span>}
    </div>
  );
}

/** AssistantPill flags a membership whose values an assistant wrote
 * (decision D6); its title names them. Nothing when there are none. */
export function AssistantPill({ keys }: { keys: readonly string[] | null | undefined }) {
  const t = useT();
  if (!keys || keys.length === 0) return null;
  return (
    <span title={t.inventory.assistant.keys(keys.join(', '))}>
      <Pill tone="warn">{t.inventory.assistant.badge}</Pill>
    </span>
  );
}

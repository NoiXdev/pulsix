import { Bot } from 'lucide-react';
import { Pill } from '../../ui/badge.tsx';
import { useT } from '../../i18n/useT.ts';

/** AssistantMark says, beside a value, that an assistant wrote it and that
 * a run receives it as plain text until a person saves it (decision D6:
 * the server renders such values inert). The caller picks the text. */
export function AssistantMark({ text, testId }: { text: string; testId?: string }) {
  return (
    <p data-testid={testId} className="flex items-start gap-1.5 text-[12px] text-warn">
      <Bot size={13} aria-hidden="true" className="mt-px shrink-0" />
      <span>{text}</span>
    </p>
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

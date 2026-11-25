import { Copy } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { useCopy } from '../../ui/useCopy.ts';
import { useT } from '../../i18n/useT.ts';

/** SecretBox shows a trigger secret exactly once: with copy, and a button
 * the person confirms storing it with, after which the caller forgets it. */
export function SecretBox({ secret, onDone }: { secret: string; onDone: () => void }) {
  const m = useT().triggers;
  const copy = useCopy();
  return (
    <div className="flex flex-col gap-2 rounded-md border border-[var(--px-warn)] p-3">
      <p className="text-xs font-semibold">{m.secretOnce}</p>
      <div className="flex items-center gap-2">
        <code className="flex-1 truncate font-mono text-xs">{secret}</code>
        <Button type="button" variant="ghost" aria-label={m.copySecret} onClick={() => void copy(secret)}>
          <Copy size={13} />
        </Button>
      </div>
      <Button type="button" onClick={onDone}>
        {m.stored}
      </Button>
    </div>
  );
}

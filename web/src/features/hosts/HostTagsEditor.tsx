import { useState, type FormEvent } from 'react';
import { Button } from '../../ui/button.tsx';
import { useHostTags, useSetHostTags } from '../../api/hooks/useTags.ts';
import { useT } from '../../i18n/useT.ts';
import { parseTagChip } from './selectorInput.ts';
import { manualTagPairs } from './tags.ts';

/**
 * HostTagsEditor is the sidebar's tag list: every tag the host carries,
 * and for a role with `hosts.write` a field to add one and a remove button
 * on each manual one (an automatic tag comes from the facts and is not the
 * operator's to remove). A malformed chip is refused inline, without a
 * request. A write sends the manual tags only: the server stores whatever
 * it is sent as manual, so an automatic tag follows the facts only while it
 * stays out of the body.
 */
export function HostTagsEditor({ org, hostId, canWrite }: { org: string; hostId: string; canWrite: boolean }) {
  const t = useT();
  const tags = useHostTags(org, hostId);
  const setTags = useSetHostTags(org, hostId);
  const [input, setInput] = useState('');
  const [error, setError] = useState<string | null>(null);
  const current = () => manualTagPairs(tags.data?.tags);

  async function add(e: FormEvent) {
    e.preventDefault();
    const result = parseTagChip(input, t);
    if (!result.ok) {
      setError(result.error);
      return;
    }
    setError(null);
    await setTags.mutateAsync([...current().filter((tag) => tag.key !== result.tag.key), result.tag]);
    setInput('');
  }

  async function remove(key: string) {
    await setTags.mutateAsync(current().filter((tag) => tag.key !== key));
  }

  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap gap-1.5">
        {(tags.data?.tags ?? []).map((tag) => (
          <span
            key={tag.key}
            className="inline-flex items-center gap-1.5 rounded border border-border bg-background px-2 py-0.5 font-mono text-[11px]"
          >
            {tag.key}={tag.value}
            {canWrite && tag.source === 'manual' && (
              <button
                type="button"
                className="text-muted-foreground hover:text-foreground"
                onClick={() => void remove(tag.key)}
                aria-label={t.hosts.detail.removeTag(tag.key)}
              >
                ×
              </button>
            )}
          </span>
        ))}
      </div>
      {canWrite && (
        <form onSubmit={(e) => void add(e)} className="flex items-center gap-2">
          <input
            value={input}
            onChange={(e) => setInput(e.target.value)}
            placeholder={t.hosts.detail.tagPlaceholder}
            className="h-8 min-w-0 flex-1 rounded-md border border-border bg-background px-2.5 font-mono text-xs outline-none focus-visible:ring-2 focus-visible:ring-primary"
          />
          <Button type="submit" disabled={setTags.isPending}>
            {t.hosts.detail.addTag}
          </Button>
        </form>
      )}
      {error && <span className="text-[11px] text-destructive">{error}</span>}
    </div>
  );
}

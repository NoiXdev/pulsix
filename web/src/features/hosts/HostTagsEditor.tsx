import { useState, type FormEvent } from 'react';
import { ApiError } from '../../api/client.ts';
import { useHostTags, useSetHostTags } from '../../api/hooks/useTags.ts';
import { useReloadHost } from '../../api/hooks/useHosts.ts';
import { useT } from '../../i18n/useT.ts';
import { parseTagChip } from './selectorInput.ts';
import { manualTagPairs } from './tags.ts';
import { StaleNotice } from './StaleNotice.tsx';

/**
 * HostTagsEditor is the host page's tag list: every tag the host carries,
 * and for a role with `hosts.write` a field to add one and a remove button
 * on each manual one (an automatic tag comes from the facts and is not the
 * operator's to remove). A malformed chip is refused inline, without a
 * request. A write sends the manual tags only: the server stores whatever
 * it is sent as manual, so an automatic tag follows the facts only while it
 * stays out of the body. Each write carries the host's `revision` as
 * If-Match: a 412 means the host changed since this page loaded it, so the
 * edit is not applied and the notice offers a reload. The chips and the
 * field share one wrapping line: the host header's third.
 */
export function HostTagsEditor({ org, hostId, revision, canWrite }: { org: string; hostId: string; revision: number; canWrite: boolean }) {
  const t = useT();
  const tags = useHostTags(org, hostId);
  const setTags = useSetHostTags(org, hostId);
  const [input, setInput] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [stale, setStale] = useState(false);
  const reloadHost = useReloadHost(org, hostId);
  const current = () => manualTagPairs(tags.data?.tags);

  async function add(e: FormEvent) {
    e.preventDefault();
    const result = parseTagChip(input, t);
    if (!result.ok) {
      setError(result.error);
      return;
    }
    setError(null);
    if (await write([...current().filter((tag) => tag.key !== result.tag.key), result.tag])) setInput('');
  }

  async function remove(key: string) {
    await write(current().filter((tag) => tag.key !== key));
  }

  /** write resolves true when the server took the tags. A 412 is shown, not
   * thrown: the typed chip stays in the field; any other error propagates. */
  async function write(tagPairs: ReturnType<typeof manualTagPairs>): Promise<boolean> {
    try {
      await setTags.mutateAsync({ tags: tagPairs, revision });
      setStale(false);
      return true;
    } catch (err) {
      if (err instanceof ApiError && err.status === 412) {
        setStale(true);
        return false;
      }
      throw err;
    }
  }

  async function reload() {
    await reloadHost();
    setStale(false);
  }

  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex flex-wrap items-center gap-1.5">
        {(tags.data?.tags ?? []).map((tag) => (
          <span
            key={`${tag.source}:${tag.key}`}
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
        {canWrite && (
          <form onSubmit={(e) => void add(e)} className="flex items-center gap-1.5">
            <input
              value={input}
              onChange={(e) => setInput(e.target.value)}
              placeholder={t.hosts.detail.tagPlaceholder}
              aria-label={t.hosts.detail.tags}
              className="h-7 w-32 rounded-md border border-border bg-background px-2 font-mono text-[11px] outline-none focus-visible:ring-2 focus-visible:ring-primary"
            />
            <button
              type="submit"
              disabled={setTags.isPending}
              className="h-7 rounded-md px-2 text-xs font-semibold text-foreground hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary disabled:opacity-50"
            >
              {t.hosts.detail.addTag}
            </button>
          </form>
        )}
      </div>
      {stale && <StaleNotice onReload={reload} />}
      {error && <span className="text-[11px] text-destructive">{error}</span>}
    </div>
  );
}

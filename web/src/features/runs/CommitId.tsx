import { Copy } from 'lucide-react';
import { useCopy } from '../../ui/useCopy.ts';
import { useT } from '../../i18n/useT.ts';

/** shortCommit is the first 12 characters of a commit id, as git abbreviates
 * it in most listings. */
function shortCommit(sha: string): string {
  return sha.slice(0, 12);
}

/**
 * CommitId shows a pinned commit (spec 2026-11-25 A) short, with the full id
 * on hover and a button that copies it.
 */
export function CommitId({ sha }: { sha: string }) {
  const t = useT();
  const copy = useCopy();
  return (
    <span className="inline-flex items-center gap-1">
      <span className="font-mono" title={sha}>
        {shortCommit(sha)}
      </span>
      <button
        type="button"
        aria-label={t.runs.commit.copy}
        title={t.runs.commit.copy}
        onClick={() => void copy(sha, t.runs.commit.copied)}
        className="rounded p-0.5 text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
      >
        <Copy size={12} />
      </button>
    </span>
  );
}

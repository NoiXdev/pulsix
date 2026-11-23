import { useEffect, useId, useRef, useState } from 'react';
import { TriangleAlert } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { UnifiedDiff } from '../../ui/diff/UnifiedDiff.tsx';
import { useT } from '../../i18n/useT.ts';

interface PlaybookSaveConflictProps {
  /** The stored text the draft started from, and the one stored now. */
  base: string;
  current: string;
  onReload: () => void;
  onSaveAnyway: () => void;
  /** Save mine anyway is not possible now (a save or the read after it is
   * pending, or the draft does not pass the check). */
  saveDisabled: boolean;
  /** How often Save mine anyway was refused too, because they saved once
   * more in between (0: not yet). */
  savedAgain: number;
}

/**
 * PlaybookSaveConflict is the notice after the server refused a save
 * because someone saved the playbook since the draft was loaded (412,
 * backlog 3.127). The draft is kept; the notice offers their changes (the
 * text the draft started from against the current one), Reload (drop the
 * draft, take theirs) and Save mine anyway (save over theirs). No merge:
 * overwriting is an explicit choice. When Save mine anyway is refused as
 * well, a line says they saved again, and focus moves to the notice so it
 * is noticed (and announced) rather than looking unchanged.
 */
export function PlaybookSaveConflict({ base, current, onReload, onSaveAnyway, saveDisabled, savedAgain }: PlaybookSaveConflictProps) {
  const t = useT();
  const c = t.playbooks.page.conflict;
  const titleId = useId();
  const [open, setOpen] = useState(false);
  const notice = useRef<HTMLElement>(null);
  useEffect(() => {
    if (savedAgain > 0) notice.current?.focus();
  }, [savedAgain]);
  return (
    <section
      ref={notice}
      tabIndex={-1}
      aria-labelledby={titleId}
      className="flex flex-col gap-2 rounded-[10px] border px-4 py-3 text-xs"
      style={{ borderColor: 'color-mix(in srgb, var(--px-warn) 33%, transparent)', background: 'color-mix(in srgb, var(--px-warn) 10%, transparent)' }}
    >
      <p id={titleId} role="alert" className="flex items-center gap-1.5 text-[13px] font-semibold" style={{ color: 'var(--px-warn)' }}>
        <TriangleAlert size={14} aria-hidden /> {c.title}
      </p>
      {savedAgain > 0 && (
        // Keyed by the count: each further refusal is a new alert.
        <p key={savedAgain} role="alert" className="font-semibold">
          {c.savedAgain}
        </p>
      )}
      <p>{c.body}</p>
      <div className="flex flex-wrap gap-2">
        <Button type="button" variant="ghost" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
          {open ? c.hide : c.show}
        </Button>
        <Button type="button" onClick={onReload}>
          {c.reload}
        </Button>
        <Button type="button" variant="primary" disabled={saveDisabled} onClick={onSaveAnyway}>
          {c.saveAnyway}
        </Button>
      </div>
      {open && <UnifiedDiff before={base} after={current} beforeHeader={c.before} afterHeader={c.after} />}
    </section>
  );
}

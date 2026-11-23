import { useId } from 'react';
import { Tag } from '../../ui/badge.tsx';
import type { JobDetail } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';

/**
 * RunDryRunBanner tops a dry run's view (backlog 3.126): the run ran in
 * check mode and changed nothing, with the options it recorded — its tags
 * (all tasks when none), whether it checked an unsaved draft and whether
 * it reported differences. A dry run without a record (none is expected,
 * but the field is optional) shows the banner without the options. While
 * the run is live the text is in the present tense, after it in the past.
 */
export function RunDryRunBanner({ dryRun, live }: { dryRun: JobDetail['dry_run']; live: boolean }) {
  const t = useT();
  const d = t.runs.dryRun;
  const titleId = useId();
  return (
    <div
      role="note"
      aria-labelledby={titleId}
      className="flex flex-col gap-2 rounded-md border p-3 text-xs"
      style={{ borderColor: 'color-mix(in srgb, var(--px-accent) 33%, transparent)', background: 'color-mix(in srgb, var(--px-accent) 8%, transparent)' }}
    >
      <span id={titleId} className="text-[13px] font-semibold" style={{ color: 'var(--px-accent)' }}>
        {d.title}
      </span>
      <p className="text-muted-foreground">{live ? d.textLive : d.text}</p>
      {dryRun && (
        <div className="flex flex-wrap items-center gap-1.5">
          <span className="text-muted-foreground">{d.tags}:</span>
          {dryRun.tags.length > 0 ? dryRun.tags.map((tag) => <Tag key={tag}>{tag}</Tag>) : <span>{d.allTags}</span>}
          <span aria-hidden="true" className="text-muted-foreground">
            ·
          </span>
          <span>{dryRun.diff ? d.diffOn : d.diffOff}</span>
          {dryRun.draft && (
            <>
              <span aria-hidden="true" className="text-muted-foreground">
                ·
              </span>
              <span className="font-semibold" style={{ color: 'var(--px-warn)' }}>
                {d.draft}
              </span>
            </>
          )}
        </div>
      )}
    </div>
  );
}

import { Pill } from '../../ui/badge.tsx';
import { Card } from '../../ui/table.tsx';
import { Section } from '../../ui/detail-layout.tsx';
import { ListRow } from '../../ui/list-row.tsx';
import { useZones } from '../../api/hooks/useHosts.ts';
import type { JobDetail } from '../../api/types.ts';
import { shortId, statusLabel, statusTone } from './status.ts';
import { useT } from '../../i18n/useT.ts';

const COLUMNS = '110px 90px minmax(0, 1fr)';

/** RunGroupSection lists the other jobs the same trigger started (one per
 * zone involved), each a row to its run with its status and zone. */
export function RunGroupSection({ org, siblings }: { org: string; siblings: NonNullable<JobDetail['group']> }) {
  const t = useT();
  const zones = useZones(org);
  const zoneName = (id: string) => zones.data?.zones?.find((z) => z.id === id)?.slug ?? shortId(id);

  return (
    <Section id="group" title={t.runs.detail.section.group}>
      <span className="text-xs text-muted-foreground">{t.runs.detail.sameTrigger}</span>
      <Card>
        {siblings.map((s) => (
          <ListRow key={s.id} to="/o/$org/runs/$id" params={{ org, id: s.id }} columns={COLUMNS} label={shortId(s.id)}>
            <span>
              <Pill tone={statusTone(s.status)}>{statusLabel(s.status, t)}</Pill>
            </span>
            <span className="font-mono text-[13px]">{shortId(s.id)}</span>
            <span className="truncate font-mono text-xs text-muted-foreground">{zoneName(s.zone_id)}</span>
          </ListRow>
        ))}
      </Card>
    </Section>
  );
}

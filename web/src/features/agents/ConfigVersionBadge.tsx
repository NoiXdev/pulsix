import { Pill } from '../../ui/badge.tsx';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';

/** How many characters of a configuration version the detail shows: the
 * versions are hex SHA-256, and eight tell two apart at a glance. */
const SHORT = 8;

/** ConfigVersionBadge says whether the agent ran the check configuration the
 * manager expected at their last comparison (backlog 3.13c): "current" when
 * the two versions are equal, "outdated" otherwise. The tooltip names both
 * versions, shortened, and when the comparison was made; with `detail`, an
 * outdated badge also spells the versions out as text (the host card has
 * the room, the Agents list keeps them in the tooltip). Nothing while the
 * comparison is unknown — no heartbeat compared yet, or a manager too old
 * to report it. An agent that has applied no configuration reports the
 * empty string, which is known and outdated. Shared by the Agents page and
 * the host agent card. */
export function ConfigVersionBadge({
  reported, expected, checkedAt, detail = false,
}: {
  reported?: string;
  expected?: string;
  checkedAt?: string;
  detail?: boolean;
}) {
  const t = useT();
  const fmt = useFormat();
  if (reported == null || expected == null) return null;
  const current = reported === expected;
  const short = (v: string) => (v === '' ? t.agents.config.none : v.slice(0, SHORT));
  const versions = t.agents.config.detail(short(reported), short(expected));
  const title = checkedAt ? `${versions} · ${t.agents.config.checkedAt(fmt.dateTime(checkedAt))}` : versions;
  return (
    <>
      <span className="shrink-0 whitespace-nowrap" title={title}>
        <Pill tone={current ? 'ok' : 'warn'}>{current ? t.agents.config.current : t.agents.config.outdated}</Pill>
      </span>
      {detail && !current && <span className="font-mono text-[11px] text-muted-foreground">{versions}</span>}
    </>
  );
}

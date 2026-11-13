import { Pill } from '../../ui/badge.tsx';
import { useT } from '../../i18n/useT.ts';

/** How many characters of a configuration version the detail shows: the
 * versions are hex SHA-256, and eight tell two apart at a glance. */
const SHORT = 8;

/** ConfigVersionBadge says whether the agent ran the check configuration the
 * manager expected at their last comparison (backlog 3.13c): "current" when
 * the two versions are equal, "outdated" otherwise, with both versions,
 * shortened, in the tooltip. Nothing while the comparison is unknown — no
 * heartbeat compared yet, or a manager too old to report it. An agent that
 * has applied no configuration reports the empty string, which is known and
 * outdated. Shared by the Agents page and the host agent card. */
export function ConfigVersionBadge({ reported, expected }: { reported?: string; expected?: string }) {
  const t = useT();
  if (reported == null || expected == null) return null;
  const current = reported === expected;
  const short = (v: string) => (v === '' ? t.agents.config.none : v.slice(0, SHORT));
  return (
    <span className="shrink-0 whitespace-nowrap" title={t.agents.config.detail(short(reported), short(expected))}>
      <Pill tone={current ? 'ok' : 'warn'}>{current ? t.agents.config.current : t.agents.config.outdated}</Pill>
    </span>
  );
}

import type { Tone } from '../../ui/badge.tsx';
import type { HostCheck, HostCheckValue, ItemDecision } from '../../api/types.ts';

/**
 * The pure reading rules behind HostChecksCard, kept beside it rather than
 * inside it: a file that exports a component may not also export plain
 * functions without breaking fast refresh
 * (`react-refresh/only-export-components`), and each of these is a
 * contract worth a table-driven test of its own rather than one reachable
 * only through a rendered card.
 */

/** metricLabel names a value the way the host reports it: the mount or
 * unit it belongs to, then the metric. A whole-host metric has no item and
 * is just the metric name. */
export function metricLabel(v: HostCheckValue): string {
  return v.item ? `${v.item} ${v.metric}` : v.metric;
}

/** formatNumber keeps a reading readable without inventing precision the
 * agent did not report: at most two decimals, no trailing zeros, and no
 * locale-dependent grouping (these are percentages and load averages, not
 * currency). A raw load average is a float with sixteen significant
 * digits; rendered verbatim it is unreadable and implies an accuracy
 * nothing measured. */
export function formatNumber(value: number): string {
  return String(Math.round(value * 100) / 100);
}

/**
 * NUMERIC_PLUGIN says which plugins measure numbers. Only the service
 * plugin reports a label (the unit's state) — cpu, memory and disk report
 * numbers, and every one of them reports an error the same way
 * (internal/agent/checks: labelObservation is called from service.go
 * alone). It is a total map over the generated plugin union rather than a
 * list of exceptions, so a fifth plugin fails to compile here until
 * somebody decides which side of the line it is on.
 */
const NUMERIC_PLUGIN: Record<HostCheck['plugin'], boolean> = { cpu: true, memory: true, disk: true, service: false };

/**
 * isGraphable reports whether a graph may be offered for one of a check's
 * values, and it asks the CHECK, not the reading.
 *
 * A reading cannot answer it. When collection fails, check_current stores
 * an error row with neither value nor label (migration 00044's CHECK, and
 * internal/checks.Observation's own shape rule), so a rule of the form
 * "has a number" loses the graph button at exactly the moment an operator
 * wants it — the history GET /hosts/{id}/metrics would draw is still
 * there, and that route answers it during the incident too.
 *
 * So: a value that carries a `label` is discrete and has no series (the
 * metrics route refuses it with a 400, naming the label), and a check
 * whose plugin only ever produces labels has none either, however its
 * latest reading happens to have failed. Everything else is a number over
 * time.
 */
export function isGraphable(check: HostCheck, v: HostCheckValue): boolean {
  if (v.label !== undefined) return false;
  return NUMERIC_PLUGIN[check.plugin];
}

/**
 * CLOCK_SKEW_FLOOR is where a difference between the host's clock and the
 * manager's stops being explicable by anything else. Below half a minute
 * it is collection and delivery timing; above it, it is a clock, and the
 * operator has something to do about it.
 */
const CLOCK_SKEW_FLOOR = 30;

/**
 * clockSkewLabel renders the measured offset between a host's clock and
 * the manager's, or null when there is nothing worth saying.
 *
 * This exists because "stale" on its own cannot be acted on. Ingest
 * accepts a reading up to an hour old (a reconnecting agent replays a
 * bounded ring, and those samples are real), while a reading is shown as
 * stale after three of its check's own intervals — 180 s for the 60 s
 * seed checks. A host whose clock is four minutes behind therefore
 * reports punctually, has everything it sends accepted and stored, and is
 * badged amber for ever. That looks exactly like a host that stopped
 * reporting, and the two remedies have nothing in common.
 *
 * The server measures the offset at ingest and sends it with the
 * envelope (`clock_offset_seconds`, positive meaning the host's clock is
 * behind the manager's); it is absent when no manager process has a
 * measurement, and absence is not zero.
 */
export function clockSkewLabel(offsetSeconds: number | undefined): string | null {
  if (offsetSeconds === undefined) return null;
  const seconds = Math.round(Math.abs(offsetSeconds));
  if (seconds < CLOCK_SKEW_FLOOR) return null;
  const magnitude = seconds >= 90 ? `${Math.round(seconds / 60)} min` : `${seconds} s`;
  return `${magnitude} ${offsetSeconds > 0 ? 'behind' : 'ahead of'}`;
}

/**
 * stateLabel renders one tuple's rule-engine verdict (v0.3b-2) as a label
 * and a Pill tone — never a tone alone. Pill already pairs its coloured dot
 * with text (see ui/badge.tsx), but that text has to actually differ per
 * state for the pairing to do any work: two Pills that both say "unknown"
 * would still collapse to one shape in a black-and-white screenshot, which
 * is exactly the failure the spec's "distinguishable without colour alone"
 * requirement rules out.
 *
 * `null` is its own case, not folded into "unknown": HostCheckValueDTO's
 * own doc says null means this tuple has never been evaluated (or its
 * check no longer resolves an interval), which is a different fact from
 * "the rule engine evaluated this and concluded unknown" — the controller's
 * own ruling is that null must never read as "ok", and conflating it with
 * "unknown" would still be reading it as a verdict rather than as no
 * verdict at all.
 *
 * `state` is typed exactly as the DTO declares it — no `| undefined` —
 * because the field is never omitted on the wire (no `omitempty` on
 * HostCheckValueDTO.State; it is always present, explicitly `null` rather
 * than silently missing). A caller passing an actually-missing value is a
 * fixture bug, not a shape this function should quietly paper over.
 */
export function stateLabel(state: HostCheckValue['state']): { label: string; tone: Tone } {
  if (state === null) return { label: 'not yet evaluated', tone: 'unknown' };
  switch (state) {
    case 'ok':
      return { label: 'ok', tone: 'ok' };
    case 'warn':
      return { label: 'warn', tone: 'warn' };
    case 'crit':
      return { label: 'crit', tone: 'crit' };
    default:
      return { label: 'unknown', tone: 'unknown' };
  }
}

/**
 * verdictLabel renders what a ValueRow shows in place of the rule engine's
 * own stateLabel, when an item decision is in play (v0.3b-3 spec, "Host
 * page, monitoring card").
 *
 * `ignored` wins over whatever `state` happens to carry. The decisive case
 * — this milestone's own review focus — is an item that already had a
 * state row before it was ignored: `internal/items`' own Apply DELETES
 * that row the moment the decision takes effect, so the very next read
 * reports `state: null`, which stateLabel alone would render as "not yet
 * evaluated". That is a different and false statement — nobody failed to
 * judge this tuple yet, judging it was switched off on purpose — and
 * conflating the two is exactly the lie v0.3b-2 went out of its way to
 * avoid for the other three states. "ignored" is its own word, not a
 * colour: the same "distinguishable without colour alone" contract
 * stateLabel's own doc comment already carries.
 */
export function verdictLabel(state: HostCheckValue['state'], ignored: boolean): { label: string; tone: Tone } {
  if (ignored) return { label: 'ignored', tone: 'unknown' };
  return stateLabel(state);
}

/**
 * decisionOriginLabel names where an item decision came from — the exact
 * two sentences the v0.3b-3 design spec gives for this row ("Host page,
 * monitoring card"): an organization-wide pattern names itself, because
 * the pattern is the only thing that tells two org-scope decisions apart;
 * a host-scoped one does not repeat the pattern, since the operator
 * reading it is already standing on the one host it could possibly name.
 */
export function decisionOriginLabel(decision: ItemDecision): string {
  return decision.scope === 'org' ? `By the organization pattern \`${decision.pattern}\`` : 'Set on this host';
}

/** sourceLabel renders the scope a check was inherited from as the thing
 * itself — the organization, the named monitoring group, this host — never
 * as the raw id the envelope carries beside it. */
export function sourceLabel(c: HostCheck): string {
  if (c.source === 'host') return 'This host';
  if (c.source === 'group') return c.source_name ?? 'Monitoring group';
  return 'Organization';
}

/** The tuple a "Rules" disclosure is open for — the same three fields that
 * name a tuple everywhere else on the host card, without `range`: there is
 * no window to pick for a rule, unlike a graph selection. Shared here
 * since backlog 3.42 split the card and its rows apart. */
export interface TupleKey {
  check: string;
  item: string;
  metric: string;
}

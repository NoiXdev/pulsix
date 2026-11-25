import type { Tone } from '../../ui/badge.tsx';
import type { CheckPluginDescriptor, HostCheck, HostCheckValue, ItemDecision } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';

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
 * MetricKinds is what the check plugins' descriptors (GET /check-plugins,
 * backlog 3.54) say about graphs: the metrics that only ever carry a label,
 * and the plugins whose every metric does (service, today) — a check of
 * such a plugin has no series at all.
 */
export interface MetricKinds {
  discreteMetrics: ReadonlySet<string>;
  labelOnlyPlugins: ReadonlySet<string>;
}

/** missingSecretText says where a check's missing secret belongs, by
 * where its plugin runs (the descriptors' runs): the host's agent.yaml for
 * a plugin the agent runs, the organization's secrets for one the manager
 * probes (backlog 3.13d). While the descriptors are not known it says only
 * that the secret is missing, never the wrong place. */
export function missingSecretText(
  t: Messages,
  name: string,
  plugin: string,
  plugins: readonly CheckPluginDescriptor[] | undefined,
): string {
  const runs = plugins?.find((p) => p.name === plugin)?.runs;
  if (runs === 'server') return t.hosts.checks.missingSecretServer(name);
  if (runs === 'agent') return t.hosts.checks.missingSecret(name);
  return t.hosts.checks.missingSecretUnknown(name);
}

/** metricKinds reads MetricKinds off the descriptors. Without them (the
 * read failed) both sets are empty and isGraphable judges by the reading
 * alone. */
export function metricKinds(plugins: readonly CheckPluginDescriptor[] | undefined): MetricKinds {
  const discreteMetrics = new Set<string>();
  const labelOnlyPlugins = new Set<string>();
  for (const p of plugins ?? []) {
    for (const m of p.metrics) if (m.kind === 'discrete') discreteMetrics.add(m.name);
    if (p.metrics.length > 0 && p.metrics.every((m) => m.kind === 'discrete')) labelOnlyPlugins.add(p.name);
  }
  return { discreteMetrics, labelOnlyPlugins };
}

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
 * metrics route refuses it with a 400, naming the label); a metric the
 * descriptors call discrete has none either, however its latest reading
 * happens to have failed (docker.state, raid.state, …); and a check whose
 * plugin only ever produces labels has none at all. Everything else is a
 * number over time.
 */
export function isGraphable(check: HostCheck, v: HostCheckValue, kinds: MetricKinds): boolean {
  if (v.label !== undefined || kinds.discreteMetrics.has(v.metric)) return false;
  return !kinds.labelOnlyPlugins.has(check.plugin);
}

/**
 * offersForget says whether a value's row carries the item's **Forget**
 * action (backlog 3.12b, spec "Manual forget"). Forget acts on the whole
 * item — every metric of `(check.key, v.item)` — so the row offers it only
 * when:
 *
 *  - the viewer may write checks;
 *  - the value belongs to a named item. A whole-host metric (item '') is
 *    not an item and carries no row action, like the Ignore/Watch button;
 *    a removed check's whole-host values are the automatic expiry's;
 *  - EVERY value of that item on this check is stale (the server's `stale`,
 *    never recomputed — see HostChecksCard). One stale metric of a live
 *    item (a metric dropped or renamed) does not make the item gone, and
 *    forgetting it would close and then reopen its live alerts. A fresh
 *    item would come back within one interval anyway; the API does not
 *    refuse that, the page simply does not offer it;
 *  - this is the item's first value in render order — one button per item,
 *    not one per stale metric that would each forget the same item.
 *
 * Vanished values (backlog 3.88) are left out of all of this: such a row
 * carries the metric's own Forget instead (HostCheckValueRow), and it is
 * stale anyway. So when the item's first value is vanished and the whole
 * item later goes quiet, the item Forget sits on its first value that is
 * not vanished rather than nowhere.
 *
 * The spec also names rows "whose check no longer asks for" the item.
 * Those never reach this card: GET /hosts/{id}/checks drops them
 * (stillAsked, internal/api/host_check_handlers.go), so they are left to
 * the automatic expiry.
 */
export function offersForget(check: HostCheck, v: HostCheckValue, canWrite: boolean): boolean {
  if (!canWrite || v.item === '') return false;
  const ofItem = (check.values ?? []).filter((x) => x.item === v.item && !x.vanished);
  return ofItem.length > 0 && ofItem[0].metric === v.metric && ofItem.every((x) => x.stale);
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
export function clockSkewLabel(offsetSeconds: number | undefined, t: Messages): string | null {
  if (offsetSeconds === undefined) return null;
  const seconds = Math.round(Math.abs(offsetSeconds));
  if (seconds < CLOCK_SKEW_FLOOR) return null;
  const magnitude = seconds >= 90 ? t.hosts.clock.minutes(Math.round(seconds / 60)) : t.hosts.clock.seconds(seconds);
  return `${magnitude} ${offsetSeconds > 0 ? t.hosts.clock.behind : t.hosts.clock.ahead}`;
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
export function stateLabel(state: HostCheckValue['state'], t: Messages): { label: string; tone: Tone } {
  if (state === null) return { label: t.hosts.state.notEvaluated, tone: 'unknown' };
  switch (state) {
    case 'ok':
      return { label: t.hosts.state.ok, tone: 'ok' };
    case 'warn':
      return { label: t.hosts.state.warn, tone: 'warn' };
    case 'crit':
      return { label: t.hosts.state.crit, tone: 'crit' };
    default:
      return { label: t.hosts.state.unknown, tone: 'unknown' };
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
export function verdictLabel(state: HostCheckValue['state'], ignored: boolean, t: Messages): { label: string; tone: Tone } {
  if (ignored) return { label: t.hosts.state.ignored, tone: 'unknown' };
  return stateLabel(state, t);
}

/**
 * decisionOriginLabel names where an item decision came from — the exact
 * two sentences the v0.3b-3 design spec gives for this row ("Host page,
 * monitoring card"): an organization-wide pattern names itself, because
 * the pattern is the only thing that tells two org-scope decisions apart;
 * a host-scoped one does not repeat the pattern, since the operator
 * reading it is already standing on the one host it could possibly name.
 * With checkKey (the item's own check), a decision on another key is marked
 * as reaching the item via the plugin's decision.
 */
export function decisionOriginLabel(decision: ItemDecision, t: Messages, checkKey?: string): string {
  const origin = decision.scope === 'org' ? t.hosts.value.originOrg(decision.pattern) : t.hosts.value.originHost;
  // A decision whose check key is not the item's own names the plugin: it
  // reaches this copied check through the plugin (backlog 3.13g).
  return checkKey !== undefined && decision.check_key !== checkKey ? `${origin} (${t.hosts.value.viaPlugin})` : origin;
}

/** sourceLabel renders the scope a check was inherited from as the thing
 * itself — the organization, the named monitoring group, this host — never
 * as the raw id the envelope carries beside it. A check defined in the
 * host's own agent.yaml (backlog 3.5a) was inherited from nothing: it
 * names where it lives instead, which is also where it is changed. */
export function sourceLabel(c: HostCheck, t: Messages): string {
  if (c.source === 'local') return t.hosts.scope.definedOnHost;
  if (c.source === 'host') return t.hosts.scope.thisHost;
  if (c.source === 'group') return c.source_name ?? t.hosts.scope.monitoringGroup;
  return t.hosts.scope.organization;
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

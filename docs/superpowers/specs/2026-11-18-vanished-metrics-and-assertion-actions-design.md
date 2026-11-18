# Vanished metrics (3.88) and assertion row actions (3.89) — design

Owner's instruction 2026-11-18: "ja weiter" (continue in the backlog). The
design calls below are the controller's, from a read-only investigation of the
code; they are recorded here so the owner can check them.

## 3.88 Metrics a check stops reporting

### Facts that shape the design
- One check run is **not** reliably one complete batch: agent output is chunked
  at 200 results, a partly failed send re-queues the rest for up to 15 minutes,
  single results can be dropped (invalid UTF-8, the per-host tuple cap), and a
  failed run legitimately carries fewer metrics than a good one (a down http
  probe reports only `http.up` and `http.failure`; `dns_ms`, `tls_ms` and
  `cert_*` depend on the redirect chain, not only on params).
- So deleting rows on ingest ("missing from this run") or on configuration
  change would drop live rows or be undone by buffered old results. Both are
  rejected.
- Today a stale row is judged per row against `now` (stale after 3 × interval),
  rules turn it `unknown`, the unknown rolls up to the host, and the item expiry
  (3.12b) never forgets it because a sibling metric keeps the item fresh. A
  removed `body_contains` therefore leaves the host unknown for good.

### Design
1. **Vanished rule.** A tuple `(host, check_key, item, metric)` is *vanished*
   when its `at` is older than the newest reading of its own
   `(host, check_key, item)` by more than 3 × the check's interval. Only a
   reading without an error (a value or a label) counts as the item's newest:
   an error proves the collection ran, not that the other metrics stopped (a
   SMART disk smartctl cannot open reports only an error on `smart.passed`, and
   its crit alerts must stay open as unknown). An error row itself is judged
   against that newest like any other row (amended 2026-11-18 after the final
   review). One pure Go function, used by
   the host-check API and by rule loading, with a test that pins both call
   sites to it.
2. **Evaluation.** A vanished tuple leaves evaluation the way an item the check
   no longer asks for does since 3.55: it is reported as unmeasured, its open
   alerts close as stale, and it no longer rolls up into the host's state.
3. **Params-driven metrics.** The plugin descriptor gains an optional
   `MetricAsked(params, metric) bool` (default: every metric asked). `http`
   answers false for `http.body_match` when `body_contains` is unset. It is
   applied wherever `ItemAsked` is applied today (host page `stillAsked`, rule
   `Asks`, effective-rules view), so removing `body_contains` takes effect at
   once instead of after 3 intervals.
4. **Host page.** The API adds an additive `vanished: boolean` on a value row.
   The SPA shows a vanished row muted with a "No longer reported" marker instead
   of a verdict, and offers Forget on it; it is never graph-less because of it.
5. **Metric expiry.** Next to the item expiry, the hourly expiry loop (already
   single-replica through its advisory lock) forgets a metric whose `at` is
   older than its item's newest error-free reading by more than the item expiry period
   (`PULSIX_ITEM_EXPIRY`, default 7 days). The item's own newer reading is the
   proof of life. `ForgetMetric` follows `forgetItem`'s lock order (host
   evaluation cursor, re-check, delete the current row with `FOR UPDATE SKIP
   LOCKED`, delete the metric's states and state changes, close that metric's
   alerts as forgotten, mark the host dirty) and keeps the item's discovery row.
6. No migration; older agents are unaffected; API change is additive.

### Accepted behaviour changes
- During an http outage longer than 3 intervals, the timing metrics a down
  probe does not report become vanished: alerts on them close as stale (the
  `http.up` / failure alerts carry the outage). After the expiry period they are
  forgotten and come back as new tuples when the site is up again.
- A single metric that stops on its own (not a configuration change) reads "no
  longer reported" instead of keeping the host unknown. The agent's existing
  24-hour error for vanished smart/temperature metrics still shows first.

## 3.89 Assertion items on the host page
- The generic value list of an `http` check leaves out the `http.assertion` and
  `http.assertion_actual` rows of assertion items (pure helper in
  `web/src/features/hosts/httpCheck.ts`).
- The assertion table (`web/src/ui/AssertionTable.tsx`, presentational) takes
  optional per-row `actions` and `detail`; an actions column appears only when a
  row has actions. `HostCheckHttp` builds them with the existing components: the
  item decision button (Ignore …), Forget (same gating as generic rows), and a
  Rules toggle opening the effective rules for
  `{check, item: <assertion name>, metric: 'http.assertion'}`; the ignored marker
  shows in `detail`. Viewers without write permission see no write actions.

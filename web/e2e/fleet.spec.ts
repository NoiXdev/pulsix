/**
 * The fleet overview (v0.3b-2 spec, "Fleet page"): a host in each of the
 * four states, and one filter that narrows the page down to exactly the
 * hosts this spec created.
 *
 * Four real agents (the same binary agent.spec.ts enrols, web/e2e/agent.ts)
 * report to four hosts this spec tags alike; a host-scoped rule pushes two
 * of them to warn and crit, and the fourth is stopped outright and left to
 * go stale. Nothing here fakes a value or a state directly — every host's
 * roll-up is whatever internal/rules' own evaluator concluded from a real
 * report, exactly like the Go half of this gate (internal/e2e/rules_test.go),
 * whose header comment explains why a threshold this spec controls is used
 * rather than trying to drive the actual percentage up and down: a
 * throwaway measurement on the very machine these suites run on showed
 * genuine idle CPU noise alone at 20-32%, with no induced load at all —
 * high enough to defeat any fixed "moderate load" band chosen with real
 * margin either side. Reading real values against a rule this spec wrote
 * is deterministic in a way that inducing real load never can be.
 *
 * Depends on the `agent` project (playwright.config.ts): it needs the
 * agent binary global-setup builds and skips, visibly, when that build did
 * not happen — the same guard agent.spec.ts uses.
 */
import { test, expect, type Locator, type Page } from '@playwright/test';
import { ADMIN_STORAGE_STATE, ORG } from './env.ts';
import { agentBinaryAvailable, startAgent, type RunningAgent } from './agent.ts';
import { pickOption } from './pickOption.ts';

test.use({ storageState: ADMIN_STORAGE_STATE });
test.skip(!agentBinaryAvailable(), 'the agent binary was not built by global-setup (see web/e2e/agent.ts)');

const base = `/o/${ORG.slug}`;
const TAG = 'suite=fleet-e2e';
const OK_HOST = 'fleet-e2e-ok';
const WARN_HOST = 'fleet-e2e-warn';
const CRIT_HOST = 'fleet-e2e-crit';
const UNKNOWN_HOST = 'fleet-e2e-unknown';

/** enrolAndStart is agent.spec.ts's own two opening steps (create an
 * unbound token, start the real binary with it), factored out here
 * because this spec repeats them for four hosts. */
async function enrolAndStart(page: Page, hostName: string): Promise<RunningAgent> {
  await page.goto(`${base}/agents`);
  await page.getByRole('button', { name: /Enrol agent/ }).click();
  await page.getByLabel('Host name (optional)').fill(hostName);
  await page.getByRole('button', { name: 'Create token' }).click();
  const token = (await page.getByLabel('Enrollment token').textContent())?.trim() ?? '';
  expect(token).toMatch(/^pxa1\./);
  await page.getByRole('button', { name: 'Done' }).click();
  return startAgent(token, hostName);
}

/** agentRow scopes an assertion to one Agents page row: the `data-list-row`
 * container that holds the row's link (named by the host) and its menu. */
function agentRow(page: Page, hostName: string) {
  return page.locator('[data-list-row]', { has: page.getByRole('link', { name: hostName, exact: true }) });
}

async function waitOnline(page: Page, hostName: string): Promise<void> {
  await expect(agentRow(page, hostName).getByText('online', { exact: true })).toBeVisible({ timeout: 60_000 });
}

/** openHost always starts from the Agents page, so it works regardless of
 * which page this spec is currently on. */
async function openHost(page: Page, hostName: string): Promise<void> {
  await page.goto(`${base}/agents`);
  await agentRow(page, hostName).getByRole('link', { name: hostName, exact: true }).click();
  await expect(page.getByRole('heading', { name: hostName })).toBeVisible();
}

/** addTag tags the host page currently open, the same flow hosts.spec.ts
 * already exercises for host administration. */
async function addTag(page: Page, tag: string): Promise<void> {
  await page.getByPlaceholder('role=db').fill(tag);
  await page.getByRole('button', { name: 'Add tag' }).click();
  await expect(page.getByText(tag)).toBeVisible();
}

/** cpuRow scopes an assertion to the host page's cpu.used_pct value row.
 * `hasText`/`has` substring matching over `<li>` was tried first and
 * matches the WRONG li: the cpu check's own metrics (cpu.used_pct,
 * cpu.load1/5/15) each get an inner `<li>` nested inside one outer `<li>`
 * for the whole "cpu" check card, and that outer li's full text also
 * contains "cpu.used_pct" — so a substring filter matches both, and a
 * state assertion then sees all four metrics' Pills at once. metricLabel
 * (hostChecks.ts) renders a whole-host metric as just its own name in its
 * own `<span>`, which is unique on the page; walking to its nearest `<li>`
 * ancestor lands on the one metric's row and nothing wider. */
function cpuRow(page: Page) {
  return page.getByText('cpu.used_pct', { exact: true }).locator('xpath=ancestor::li[1]');
}

/** waitForCpuState polls via the host page's own auto-refetch (useChecks.ts,
 * a 30s refetchInterval), not a manual reload — the same "nothing pushes
 * this" reality FleetPage's own totals line documents, except this one
 * page does poll on its own. */
async function waitForCpuState(page: Page, want: string, timeout: number): Promise<void> {
  await expect(cpuRow(page).getByText(want, { exact: true })).toBeVisible({ timeout });
}

/** selectHost searches for hostName in a host picker (RuleDialog's and
 * CheckDialog's are the same shape) and selects it. The search box's
 * result list is its own query (`useHosts`), a round trip behind the
 * keystroke that fills it — pickOption's own toBeVisible() wait
 * (playwright.config.ts's default expect timeout, 10s) is what now waits
 * for hostName's own option to actually arrive before clicking it. */
async function selectHost(dialog: Locator, hostName: string): Promise<void> {
  await dialog.getByLabel('Search monitored hosts').fill(hostName);
  await pickOption(dialog, 'Host', hostName);
}

/** createHostRule adds a host-scoped, form-mode cpu.used_pct rule from the
 * Rules page, with a threshold every real reading crosses (any percentage
 * is > -1) — the lever this file's own header comment explains, and the
 * same one internal/e2e/rules_test.go uses for the identical reason. */
async function createHostRule(page: Page, hostName: string, severity: 'warn' | 'crit'): Promise<void> {
  await page.goto(`${base}/checks/rules`);
  await page.getByRole('button', { name: 'New rule' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Name').fill(`e2e-fleet-${severity}`);
  await pickOption(dialog, 'Metric', 'cpu.used_pct');
  await pickOption(dialog, 'Severity', severity === 'warn' ? 'Warning' : 'Critical');
  await pickOption(dialog, 'Applies to', 'One host');
  await selectHost(dialog, hostName);
  await pickOption(dialog, 'Operator', { value: '>' });
  await dialog.getByLabel('Value').fill('-1');
  await dialog.getByLabel('For (seconds)').fill('0');
  await dialog.getByRole('button', { name: 'Save' }).click();
  await expect(dialog).toHaveCount(0);
}

/** disableSeedCheck turns off one of the seed "Linux base" checks
 * (internal/checks.SeedDefinitions) for one host, by creating a host-scoped
 * override that is otherwise identical and simply unchecks "Enabled" — the
 * same override-to-disable move an operator would make, and the reason
 * this exists: `disk`'s own seed interval is five minutes, so its first
 * reading lands well into this test's own runtime, and on at least one
 * real run here it landed as a collection error — invisible on the warn,
 * crit and unknown hosts (their own cpu tuple is already worse, and
 * `rules.Worse` keeps the roll-up at the worse of the two — states.go's own
 * "a known problem outranks an unknown one"), but not on the ok host, whose
 * only other tuples are ok, so the roll-up followed disk into unknown
 * instead. This test is about what the rule engine concludes from
 * cpu.used_pct; disk and service (whose own state is "not supported on
 * this platform" on a non-Linux runner, per service_stub.go — harmless, but
 * also not this test's business) are switched off everywhere so nothing
 * else can cast a vote in the roll-up this spec reads. A disabled check
 * resolves no interval, so its tuple is excluded from evaluation entirely
 * (states.go's own `measured` contract) rather than merely hidden. */
async function disableSeedCheck(page: Page, hostName: string, pluginLabel: 'Disk' | 'Service units'): Promise<void> {
  await page.goto(`${base}/checks`);
  await page.getByRole('button', { name: 'New check' }).click();
  const dialog = page.getByRole('dialog');
  await pickOption(dialog, 'Plugin', pluginLabel);
  if (pluginLabel === 'Service units') {
    // required, and otherwise blank for a check this test means to disable
    // outright — its own unit list is never read.
    await dialog.getByLabel('Units (one per line)').fill('sshd');
  }
  await pickOption(dialog, 'Binds to', 'One host');
  await selectHost(dialog, hostName);
  await dialog.getByLabel('Enabled').uncheck();
  await dialog.getByRole('button', { name: 'Save' }).click();
  await expect(dialog).toHaveCount(0);
}

/** addFastCheck overrides hostName's cpu check to a 10-second interval —
 * the "short-interval definition of its own" internal/e2e/rules_test.go
 * also creates, so its staleness deadline (3x that) is 30 seconds rather
 * than the seed cpu check's own three minutes. Every host gets this, not
 * only the one bound for unknown: this test runs four real agents over
 * several real minutes, and a first version of it left ok's host on the
 * seed's own 60s/180s cadence — the whole scenario comfortably outlasted
 * three missed 60s ticks on a machine also busy running the other three
 * agents, a manager and a browser, and ok's own host went stale before
 * this test ever got back to reading it. Every host having its own short
 * deadline is what makes the assertions below about the rule engine's
 * verdict rather than about how this machine happened to schedule things. */
async function addFastCheck(page: Page, hostName: string): Promise<void> {
  await page.goto(`${base}/checks`);
  await page.getByRole('button', { name: 'New check' }).click();
  const dialog = page.getByRole('dialog');
  await pickOption(dialog, 'Plugin', 'CPU');
  await pickOption(dialog, 'Binds to', 'One host');
  await selectHost(dialog, hostName);
  await dialog.getByLabel('Interval (seconds)').fill('10');
  await dialog.getByRole('button', { name: 'Save' }).click();
  await expect(dialog).toHaveCount(0);
}

test('the fleet page shows a host in each state, narrowed by one filter', async ({ page }) => {
  // Four real agents, one host stopped and left to go stale: this genuinely
  // takes minutes, not the suite's default 60s per test (playwright.config.ts,
  // sized for every other spec's much shorter journey).
  test.setTimeout(10 * 60_000);

  let okAgent: RunningAgent | undefined;
  let warnAgent: RunningAgent | undefined;
  let critAgent: RunningAgent | undefined;
  let unknownAgent: RunningAgent | undefined;

  try {
    // --- Four hosts, tagged alike --------------------------------------
    okAgent = await enrolAndStart(page, OK_HOST);
    await waitOnline(page, OK_HOST);
    await openHost(page, OK_HOST);
    await addTag(page, TAG);
    await addFastCheck(page, OK_HOST);
    await disableSeedCheck(page, OK_HOST, 'Disk');
    await disableSeedCheck(page, OK_HOST, 'Service units');
    // ok gets no rule of its own: the seed set's own thresholds (85-97%,
    // internal/rules.SeedRules) are nowhere near what a real, otherwise
    // idle test machine reports — that gap is what makes "ok" the state a
    // host with no override lands on.

    warnAgent = await enrolAndStart(page, WARN_HOST);
    await waitOnline(page, WARN_HOST);
    await openHost(page, WARN_HOST);
    await addTag(page, TAG);
    await addFastCheck(page, WARN_HOST);
    await disableSeedCheck(page, WARN_HOST, 'Disk');
    await disableSeedCheck(page, WARN_HOST, 'Service units');
    await createHostRule(page, WARN_HOST, 'warn');

    critAgent = await enrolAndStart(page, CRIT_HOST);
    await waitOnline(page, CRIT_HOST);
    await openHost(page, CRIT_HOST);
    await addTag(page, TAG);
    await addFastCheck(page, CRIT_HOST);
    await disableSeedCheck(page, CRIT_HOST, 'Disk');
    await disableSeedCheck(page, CRIT_HOST, 'Service units');
    await createHostRule(page, CRIT_HOST, 'crit');

    unknownAgent = await enrolAndStart(page, UNKNOWN_HOST);
    await waitOnline(page, UNKNOWN_HOST);
    await openHost(page, UNKNOWN_HOST);
    await addTag(page, TAG);
    await addFastCheck(page, UNKNOWN_HOST);
    await disableSeedCheck(page, UNKNOWN_HOST, 'Disk');
    await disableSeedCheck(page, UNKNOWN_HOST, 'Service units');

    // --- Each host reaches its own state, read from its own page --------
    await openHost(page, OK_HOST);
    await waitForCpuState(page, 'ok', 90_000);

    await openHost(page, WARN_HOST);
    await waitForCpuState(page, 'warn', 90_000);

    await openHost(page, CRIT_HOST);
    await waitForCpuState(page, 'crit', 90_000);

    // unknown needs one real reading first — there has to be a tuple to
    // go stale at all — then the agent stops for real. Nothing here fakes
    // silence; the machine's own genuine silence is what the evaluator
    // reads three intervals later.
    await openHost(page, UNKNOWN_HOST);
    await waitForCpuState(page, 'ok', 60_000);
    await unknownAgent.stop();
    await waitForCpuState(page, 'unknown', 120_000);

    // --- One filter, four rows, nothing else -----------------------------
    // useFleet (useFleet.ts) carries no refetchInterval — unlike the host
    // page this spec just polled, nothing here refreshes the page on its
    // own, so each attempt below is a fresh navigation. That is also why
    // this is a poll and not a single read: every host's own state was
    // already confirmed once, live, on its own page above, but the 30s
    // staleness window this spec's own fast checks leave (10s interval x 3)
    // is tight enough that ordinary jitter on a machine running four real
    // agents, a manager and a browser at once can catch one host a few
    // seconds into a state it is about to leave — the same reason every
    // other wait in this file polls rather than asserts once.
    async function waitForFleetState(hostName: string, want: string, timeout: number): Promise<void> {
      const deadline = Date.now() + timeout;
      for (;;) {
        await page.goto(`${base}/fleet`);
        await page.getByLabel('Tag').fill(TAG);
        const row = page.locator('[data-list-row]', { has: page.getByRole('link', { name: hostName, exact: true }) });
        try {
          await expect(row.getByText(want, { exact: true })).toBeVisible({ timeout: 5_000 });
          return;
        } catch (err) {
          if (Date.now() > deadline) throw err;
        }
      }
    }

    await waitForFleetState(OK_HOST, 'ok', 60_000);
    await waitForFleetState(WARN_HOST, 'warn', 60_000);
    await waitForFleetState(CRIT_HOST, 'crit', 60_000);
    await waitForFleetState(UNKNOWN_HOST, 'unknown', 60_000);

    // The filter actually filters: hosts.spec.ts's own "web-04.fra" carries
    // no suite=fleet-e2e tag and must not appear in this filtered view.
    await expect(page.getByRole('link', { name: 'web-04.fra', exact: true })).toHaveCount(0);
  } finally {
    // RunningAgent.stop() is idempotent (agent.ts's own contract), so
    // stopping unknownAgent again here (already stopped above) is a
    // harmless no-op.
    await okAgent?.stop();
    await warnAgent?.stop();
    await critAgent?.stop();
    await unknownAgent?.stop();
  }
});

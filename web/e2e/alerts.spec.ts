/**
 * One whole alert episode (v0.4a design doc, "GUI" and "Lifecycle"): a real
 * agent drives a host's cpu tuple to crit, the alert appears on the Alerts
 * page, an operator acknowledges it with a note, and removing the rule that
 * holds the tuple at crit lets it recover — the alert closes as `resolved`
 * and leaves the open list.
 *
 * One host for the whole episode, not three: unlike items.spec.ts's two
 * acts (which needed two distinct mounts because both land on the SAME
 * `(host, check_key, item)` story and the row actions it asserts have no
 * per-row name), this is a single episode told in three sequential acts on
 * one alert, so there is only ever one alert row to find. What still needs
 * isolation is AlertRow's own un-scoped "Acknowledge"/"Snooze" buttons
 * (AlertRow.tsx: `aria-label="Acknowledge"`, not `aria-label="Acknowledge
 * <host>"`) — ambiguous the moment two rows are visible on the SAME page at
 * once. fleet.spec.ts's own WARN_HOST/CRIT_HOST (and items.spec.ts's own
 * host) can easily still have an open or recently-stale alert by the time
 * this spec runs in the same shared manager. The fix is not a second host
 * per act, it is scoping the whole page itself: `GET
 * /orgs/{org}/alerts?host={id}` (AlertsPage's own `?host=` — HostAlertsCard's
 * escape hatch to full history, reused here) narrows every request this
 * spec makes to exactly this host's own alert, so no other spec's row is
 * ever on the page to collide with.
 *
 * cpu is the metric and the seed disk/service checks are disabled, the same
 * lever fleet.spec.ts's own header comment explains: a throwaway load
 * measurement on the CI runner showed idle cpu noise alone at 20-32%, so a
 * host-scoped rule this spec writes itself (threshold -1, every real
 * reading crosses it) is what makes the crit transition deterministic
 * rather than racing the runner's own load.
 */
import { test, expect, type Locator, type Page } from '@playwright/test';
import { ADMIN_STORAGE_STATE, ORG } from './env.ts';
import { agentBinaryAvailable, startAgent, type RunningAgent } from './agent.ts';
import { pickOption } from './pickOption.ts';

test.use({ storageState: ADMIN_STORAGE_STATE });
test.skip(!agentBinaryAvailable(), 'the agent binary was not built by global-setup (see web/e2e/agent.ts)');

const base = `/o/${ORG.slug}`;
const HOST_NAME = 'alerts-e2e-host';
const RULE_NAME = 'e2e-alert-crit';

/** enrolAndStart is agent.spec.ts's own two opening steps (create an
 * unbound token, start the real binary with it) — every spec in this suite
 * keeps its own copy of this shape rather than importing one another. */
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

function agentRow(page: Page, hostName: string) {
  return page.getByRole('link', { name: hostName, exact: true }).locator('xpath=../..');
}

async function waitOnline(page: Page, hostName: string): Promise<void> {
  await expect(agentRow(page, hostName).getByText('online', { exact: true })).toBeVisible({ timeout: 60_000 });
}

/** openHost always starts from the Agents page and returns the host's own
 * id, read back out of the URL hostDetailRoute lands on (router.tsx:
 * `/o/$org/hosts/$id`) — the one place this spec can get it without
 * fetching the hosts list itself. */
async function openHost(page: Page, hostName: string): Promise<string> {
  await page.goto(`${base}/agents`);
  await agentRow(page, hostName).getByRole('link', { name: hostName, exact: true }).click();
  await expect(page.getByRole('heading', { name: hostName })).toBeVisible();
  const match = /\/hosts\/([^/?#]+)/.exec(page.url());
  if (!match) throw new Error(`host page URL did not carry an id: ${page.url()}`);
  return match[1];
}

/** selectHost is fleet.spec.ts's own helper, verbatim: CheckDialog's
 * HostPicker and RuleDialog's host picker are the same shape (a search box
 * over a paginated host list, then a ui/Select the search result fills). */
async function selectHost(dialog: Locator, hostName: string): Promise<void> {
  await dialog.getByLabel('Search monitored hosts').fill(hostName);
  // pickOption's own toBeVisible() wait (playwright.config.ts's default
  // expect timeout, 10s) replaces the old toBeAttached guard here: the
  // Host picker's options still only arrive once the search narrows them.
  await pickOption(dialog, 'Host', hostName);
}

/** createCritRule adds a host-scoped, form-mode cpu.used_pct rule with a
 * threshold every real reading crosses (-1, `for_seconds: 0`) — the same
 * lever and the same reasoning fleet.spec.ts's own createHostRule uses. */
async function createCritRule(page: Page, hostName: string): Promise<void> {
  await page.goto(`${base}/checks/rules`);
  await page.getByRole('button', { name: 'New rule' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Name').fill(RULE_NAME);
  await pickOption(dialog, 'Metric', 'cpu.used_pct');
  await pickOption(dialog, 'Severity', 'Critical');
  await pickOption(dialog, 'Applies to', 'One host');
  await selectHost(dialog, hostName);
  await pickOption(dialog, 'Operator', { value: '>' });
  await dialog.getByLabel('Value').fill('-1');
  await dialog.getByLabel('For (seconds)').fill('0');
  await dialog.getByRole('button', { name: 'Save' }).click();
  await expect(dialog).toHaveCount(0);
}

/** deleteCritRule removes the rule createCritRule added. With no rule left
 * to match this tuple, the next evaluation tick judges it against nothing
 * — "no match is ok" (internal/rules/evaluate.go's own step 2 comment) —
 * and an improvement takes effect at once, unlike a deterioration's
 * for_duration wait. This is how the episode's host "recovers": nothing
 * here touches the agent's real cpu reading, which never left the 20-32%
 * idle band fleet.spec.ts's own header comment measured; it is the rule
 * that stops calling that reading critical. */
async function deleteCritRule(page: Page): Promise<void> {
  await page.goto(`${base}/checks/rules`);
  page.once('dialog', (d) => void d.accept());
  await page.getByRole('button', { name: `Delete ${RULE_NAME}` }).click();
  await expect(page.getByRole('button', { name: `Delete ${RULE_NAME}` })).toHaveCount(0);
}

/** disableSeedCheck turns off one of the seed "Linux base" checks for one
 * host, the same override fleet.spec.ts's own helper creates — so that
 * nothing but cpu.used_pct can cast a vote in this host's roll-up and the
 * episode below is unambiguously about the one rule this spec wrote. */
async function disableSeedCheck(page: Page, hostName: string, pluginLabel: 'Disk' | 'Service units'): Promise<void> {
  await page.goto(`${base}/checks`);
  await page.getByRole('button', { name: 'New check' }).click();
  const dialog = page.getByRole('dialog');
  await pickOption(dialog, 'Plugin', pluginLabel);
  if (pluginLabel === 'Service units') {
    await dialog.getByLabel('Units (one per line)').fill('sshd');
  }
  await pickOption(dialog, 'Binds to', 'One host');
  await selectHost(dialog, hostName);
  await dialog.getByLabel('Enabled').uncheck();
  await dialog.getByRole('button', { name: 'Save' }).click();
  await expect(dialog).toHaveCount(0);
}

/** addFastCheck overrides hostName's cpu check to a 10-second interval, the
 * same short-cadence override fleet.spec.ts's own helper uses, needed so
 * this spec's own staleness/eval patience (sized in multiples of it) stays
 * in the tens of seconds rather than the seed check's own three minutes. */
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

function cpuRow(page: Page): Locator {
  return page.getByText('cpu.used_pct', { exact: true }).locator('xpath=ancestor::li[1]');
}

/** waitForCpuState polls via the host page's own auto-refetch (useChecks.ts,
 * a 30s refetchInterval) — fleet.spec.ts's own helper, verbatim. */
async function waitForCpuState(page: Page, hostName: string, want: string, timeout: number): Promise<void> {
  await openHost(page, hostName);
  await expect(cpuRow(page).getByText(want, { exact: true })).toBeVisible({ timeout });
}

/** alertRow returns the SINGLE alert row on an already-`?host=`-scoped
 * Alerts page, found by walking up from the one span every row carries
 * (AlertRow.tsx's own `data-testid="alert-open"`) — robust even though
 * AlertRow's own root `<div>` carries no testid or role of its own. */
function alertRow(page: Page): Locator {
  return page.getByTestId('alert-open').locator('xpath=..');
}

/** waitForHostAlert polls the host-scoped Alerts page (a fresh navigation
 * each attempt, the same "no live push" pattern fleet.spec.ts's own
 * waitForFleetState uses) until the one alert on it reads `wantOpenText`
 * in its own "Open"/"Closed · …" pill — e.g. `'Open'` once the tuple goes
 * crit, or a string containing `'resolved'` once the rule is gone and the
 * tuple recovers. Scoping every request to this host's own id
 * (AlertsPage's `?host=` search param) is what keeps this spec immune to
 * another spec's own host's alert sharing the page — see this file's own
 * header comment. */
async function waitForHostAlert(page: Page, hostId: string, wantOpenText: string | RegExp, timeout: number): Promise<void> {
  const deadline = Date.now() + timeout;
  for (;;) {
    await page.goto(`${base}/alerts?host=${hostId}`);
    await expect(page.getByRole('heading', { name: 'Alerts' })).toBeVisible();
    const pill = page.getByTestId('alert-open');
    if ((await pill.count()) > 0) {
      const text = (await pill.first().textContent())?.trim() ?? '';
      const matched = typeof wantOpenText === 'string' ? text === wantOpenText : wantOpenText.test(text);
      if (matched) return;
    }
    if (Date.now() > deadline) {
      throw new Error(`timed out waiting for host ${hostId}'s alert to read "${wantOpenText}"`);
    }
    await page.waitForTimeout(3_000);
  }
}

test('one alert episode: a host goes crit, the alert is acknowledged, and it closes as resolved when the host recovers', async ({ page }) => {
  // Discovery and evaluation both ride the real ticker's own fixed-interval
  // cadence (internal/rules.Ticker) — not something a test can shorten —
  // and this spec waits through it twice (crit, then recovery), so its own
  // patience has to outlast both legs plus the agent's own startup.
  test.setTimeout(5 * 60_000);

  let agent: RunningAgent | undefined;
  try {
    // --- Set the stage: one host, cpu only, a rule that always fires ----
    agent = await enrolAndStart(page, HOST_NAME);
    await waitOnline(page, HOST_NAME);
    await openHost(page, HOST_NAME);
    await addFastCheck(page, HOST_NAME);
    await disableSeedCheck(page, HOST_NAME, 'Disk');
    await disableSeedCheck(page, HOST_NAME, 'Service units');
    await createCritRule(page, HOST_NAME);

    // --- Act 1: drive the host to crit, see the alert appear -------------
    await waitForCpuState(page, HOST_NAME, 'crit', 90_000);
    const hostId = /\/hosts\/([^/?#]+)/.exec(page.url())?.[1];
    if (!hostId) throw new Error(`host page URL did not carry an id: ${page.url()}`);

    await waitForHostAlert(page, hostId, 'Open', 90_000);
    const row = alertRow(page);
    await expect(row.getByTestId('alert-peak').getByText('crit', { exact: true })).toBeVisible();
    await expect(row.getByTestId('alert-current').getByText('crit', { exact: true })).toBeVisible();
    await expect(row.getByTestId('alert-ack').getByText('Unacknowledged', { exact: true })).toBeVisible();

    // --- Act 2: acknowledge it with a note --------------------------------
    await row.getByRole('button', { name: 'Acknowledge' }).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog.getByRole('heading', { name: 'Acknowledge alert' })).toBeVisible();
    const NOTE = 'e2e: investigating the crit spike';
    await dialog.getByLabel('Note').fill(NOTE);
    await dialog.getByRole('button', { name: 'Acknowledge' }).click();
    await expect(dialog).toHaveCount(0);

    await expect(row.getByTestId('alert-ack').getByText('Acknowledged', { exact: true })).toBeVisible();
    await expect(row.getByTestId('alert-ack').getByText(NOTE)).toBeVisible();
    // Acknowledging never mutes (design doc, "Acknowledging is not muting")
    // — the row still offers Snooze, not Unsnooze.
    await expect(row.getByRole('button', { name: 'Snooze' })).toBeVisible();

    // --- Act 3: resolve the host, see the alert close and leave the open
    //     list ----------------------------------------------------------
    await deleteCritRule(page);
    await waitForCpuState(page, HOST_NAME, 'ok', 90_000);
    await waitForHostAlert(page, hostId, /^Closed/, 90_000);
    await expect(alertRow(page).getByTestId('alert-open').getByText('resolved')).toBeVisible();
    // The acknowledgement survives the close — decision 2 only ever voids
    // an ack on a worsening severity, never on a close.
    await expect(alertRow(page).getByTestId('alert-ack').getByText('Acknowledged', { exact: true })).toBeVisible();

    // Leaves the open list: the same host-scoped page, narrowed further by
    // its own State filter, now shows no rows at all.
    await pickOption(page, 'State', 'Open');
    await expect(page.getByTestId('alert-open')).toHaveCount(0);
    await expect(page.getByText('No alerts match this view.')).toBeVisible();
  } finally {
    await agent?.stop();
  }
});

/**
 * The Items tab and the host page's own ignore action (v0.3b-3 spec): an
 * operator ignores a mount straight from the host page, then opens the
 * Items tab, watches the pattern editor's own preview count the one item
 * this host has actually discovered, saves the decision, and sees it
 * listed.
 *
 * One real agent (the same binary fleet.spec.ts and agent.spec.ts enrol,
 * web/e2e/agent.ts), reporting one mount this spec names explicitly — "/",
 * the one mount point every platform these suites run on is guaranteed to
 * have (the same lever internal/e2e/items_test.go's own header comment
 * explains for its own, Go-side half of this gate). Discovery depends on a
 * real evaluation tick (internal/rules.Ticker, a fixed 15-second interval,
 * not something a test can shorten), so the preview below is polled —
 * reopening the dialog on a short interval — rather than asserted once:
 * the item simply is not there yet for the first several seconds after
 * the mount's first reading lands.
 *
 * Host-scoped throughout, not organization-scoped: this suite's shared
 * manager runs every other spec's own agents too, several of which also
 * report a mount named "/" from their own hosts, and an organization-wide
 * pattern's preview counts every host's items, not just this one's. A
 * host-scoped decision (and a host-scoped preview, since the dialog
 * resolves a preview exactly like it would resolve the decision it is
 * about to save) is immune to whatever another spec's own host happens to
 * be reporting at the same time.
 */
import { test, expect, type Locator, type Page } from '@playwright/test';
import { ADMIN_STORAGE_STATE, ORG } from './env.ts';
import { agentBinaryAvailable, startAgent, type RunningAgent } from './agent.ts';

test.use({ storageState: ADMIN_STORAGE_STATE });
test.skip(!agentBinaryAvailable(), 'the agent binary was not built by global-setup (see web/e2e/agent.ts)');

const base = `/o/${ORG.slug}`;
const HOST_NAME = 'items-e2e-host';
const MOUNT = '/';
const CHECK_KEY = 'disk';

/** enrolAndStart is agent.spec.ts's own two opening steps (create an
 * unbound token, start the real binary with it) — the same helper
 * fleet.spec.ts gives itself, repeated here rather than imported: every
 * spec in this suite keeps its own copy of this shape. */
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

async function openHost(page: Page, hostName: string): Promise<void> {
  await page.goto(`${base}/agents`);
  await agentRow(page, hostName).getByRole('link', { name: hostName, exact: true }).click();
  await expect(page.getByRole('heading', { name: hostName })).toBeVisible();
}

/** selectHost is fleet.spec.ts's own helper, verbatim: CheckDialog's
 * HostPicker and ItemDecisionDialog's DecisionHostPicker are the same
 * shape (a search box over a paginated host list, then a <select> the
 * search result fills), so one function drives both. */
async function selectHost(dialog: Locator, hostName: string): Promise<void> {
  await dialog.getByLabel('Search monitored hosts').fill(hostName);
  const hostSelect = dialog.getByLabel('Host', { exact: true });
  await expect(hostSelect.getByRole('option', { name: hostName, exact: true })).toBeAttached({ timeout: 10_000 });
  await hostSelect.selectOption({ label: hostName });
}

/** addMountCheck overrides hostName's disk check to the one explicit mount
 * this spec follows, at the shortest interval the API accepts — the same
 * "mounts" param internal/e2e/items_test.go gives the agent's real disk
 * plugin directly, through the GUI's own CheckDialog here instead. */
async function addMountCheck(page: Page, hostName: string): Promise<void> {
  await page.goto(`${base}/checks`);
  await page.getByRole('button', { name: 'New check' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Plugin').selectOption({ label: 'Disk' });
  await dialog.getByLabel('Mounts (one per line, empty means every mount)').fill(MOUNT);
  await dialog.getByLabel('Binds to').selectOption({ label: 'One host' });
  await selectHost(dialog, hostName);
  await dialog.getByLabel('Interval (seconds)').fill('10');
  await dialog.getByRole('button', { name: 'Save' }).click();
  await expect(dialog).toHaveCount(0);
}

/** mountRow scopes an assertion to the host page's own "/ disk.used_pct"
 * value row — metricLabel's own rendering of an item-bound metric
 * (hostChecks.ts: `${item} ${metric}`) — the same ancestor-`<li>` walk
 * fleet.spec.ts's own cpuRow uses, and for the same reason: a substring
 * match on "disk.used_pct" alone would also catch this mount's
 * disk.free_bytes and disk.inodes_used_pct siblings' shared outer `<li>`. */
function mountRow(page: Page): Locator {
  return page.getByText(`${MOUNT} disk.used_pct`, { exact: true }).locator('xpath=ancestor::li[1]');
}

/** waitForMountReading polls the host page (useHostChecks' own 30s
 * refetchInterval) until the mount's disk.used_pct row exists at all —
 * the same "nothing pushes this but the page's own poll" reality
 * fleet.spec.ts's waitForCpuState documents, needed here before either act
 * below has anything to click or discover. A row for a numeric metric
 * only ever appears once a real value is behind it — the dash fallback in
 * ValueRow is for a value that is `undefined` on an existing reading, not
 * for "no reading yet" (that case renders no row at all, "No reading yet."
 * instead) — so the row's own appearance already is the reading. */
async function waitForMountReading(page: Page, timeout: number): Promise<void> {
  await expect(mountRow(page)).toBeVisible({ timeout });
}

test('an operator ignores a mount from the host page, then the Items tab sees, previews and saves the same decision', async ({ page }) => {
  // Discovery rides the real evaluation ticker's own fixed 15s interval
  // (internal/rules.Ticker, DefaultEvalInterval) — not something a test
  // can shorten — so this spec's own patience has to outlast it, the same
  // reason fleet.spec.ts extends its own timeout for a real agent's
  // reporting cadence.
  test.setTimeout(3 * 60_000);

  let agent: RunningAgent | undefined;
  try {
    agent = await enrolAndStart(page, HOST_NAME);
    await waitOnline(page, HOST_NAME);
    await addMountCheck(page, HOST_NAME);
    // addMountCheck's own first act is `page.goto(`${base}/checks`)` (it
    // has to be: that is where "New check" lives), so `page` is sitting on
    // the Checks list, not the host page, once it returns. This is the
    // navigation back — without it the wait below polls the Checks page's
    // own DOM for a row that can only ever appear on the host page's
    // monitoring card, and times out never having looked at the right page
    // at all. Raising the timeout would not have fixed that, only made the
    // failure slower.
    await openHost(page, HOST_NAME);
    await waitForMountReading(page, 60_000);

    // --- Ignoring an item from the host page --------------------------
    // Scoped to the disk.used_pct row specifically: disk reports three
    // metrics per mount (used_pct, free_bytes, inodes_used_pct), each its
    // own ValueRow and so each carrying an identically-labelled "Ignore
    // disk /" button (ItemDecisionButton's aria-label names the check and
    // the item, not the metric) — an unscoped locator would match all
    // three and Playwright's strict mode refuses to click an ambiguous
    // one. Any of the three would write the same host-scoped exact
    // decision; this spec's own assertions just need to look at the same
    // row consistently.
    //
    // Before: unignored, the row's own fast path offers "Ignore".
    const row = mountRow(page);
    const ignoreButton = row.getByRole('button', { name: `Ignore ${CHECK_KEY} ${MOUNT}` });
    await expect(ignoreButton).toBeVisible();
    await expect(row.getByText('ignored', { exact: true })).toHaveCount(0);

    await ignoreButton.click();

    // After: the row's own verdict pill flips to "ignored" (verdictLabel's
    // own word for it, never conflated with "not yet evaluated") and the
    // button becomes its own undo, "Watch" — both without a reload, since
    // ItemDecisionButton invalidates the host page's own queries itself,
    // and both without waiting for the evaluation ticker either: `ignored`
    // is resolved fresh from the decision list on every read
    // (host_check_handlers.go's hostIgnoredItems), not from anything the
    // ticker writes.
    await expect(row.getByText('ignored', { exact: true })).toBeVisible();
    await expect(row.getByRole('button', { name: `Watch ${CHECK_KEY} ${MOUNT}` })).toBeVisible();
    await expect(row.getByRole('button', { name: `Ignore ${CHECK_KEY} ${MOUNT}` })).toHaveCount(0);

    // Measurement never stopped: the row is still there with its own
    // reading — ignoring only ever withholds the verdict, never the value.
    await expect(row).toBeVisible();

    // --- The Items tab: create a decision, see the preview's count,
    //     save, see it listed ------------------------------------------
    await page.goto(`${base}/checks/items`);
    await expect(page.getByRole('heading', { name: 'Checks' })).toBeVisible();
    await expect(page.getByRole('tab', { name: 'Items' })).toHaveAttribute('aria-selected', 'true');

    // The pattern editor's own preview resolves against check_items — the
    // evaluation ticker's own discovery pass, not the current reading this
    // spec already waited for above — so it is polled: reopen the dialog,
    // ask the same question, and retry until the mount this host reported
    // has actually been discovered.
    const dialog = await waitForDiscoveredInPreview(page, HOST_NAME, 90_000);

    // The verdict defaults to "ignore" already (ItemDecisionDialog's own
    // default state) — and is an independent decision from the host page's
    // own host-scoped exact one above, naming the exact same (check, item)
    // on purpose: it is what the GUI calls "the same target, now through
    // the general-purpose editor", not a conflicting one — internal/items'
    // own tie-break (the older CreatedAt) is exactly what the host page's
    // earlier write already demonstrated once, below.
    await dialog.getByRole('button', { name: 'Save' }).click();
    await expect(dialog).toHaveCount(0);

    // --- See it listed ---------------------------------------------------
    // "Edit …"/"Delete …" name the decision's own pattern, so — the same
    // shape rules.spec.ts's own listing check uses — they double as proof
    // the row exists without a brittle row-by-CSS-class lookup across a
    // page that may carry other specs' own host-scoped decisions too.
    await expect(page.getByRole('button', { name: `Edit decision for ${MOUNT}` })).toBeVisible();
    await expect(page.getByRole('button', { name: `Delete decision for ${MOUNT}` })).toBeVisible();
  } finally {
    await agent?.stop();
  }
});

/** waitForDiscoveredInPreview opens "New decision", fills in the one
 * target this spec follows (host-scoped, exact, this host's own mount) and
 * polls — closing and reopening the dialog — until the preview reports at
 * least one match, returning the dialog left open on that successful
 * attempt so the caller can carry on filling and saving it. Reopening
 * rather than merely re-typing the pattern is what forces a fresh request
 * each attempt: the preview query is keyed on its whole body and would
 * otherwise serve the same cached "no match" answer for the life of one
 * open dialog. */
async function waitForDiscoveredInPreview(page: Page, hostName: string, timeoutMs: number): Promise<Locator> {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    await page.getByRole('button', { name: 'New decision' }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByLabel('Applies to').selectOption({ label: 'One host' });
    await selectHost(dialog, hostName);
    await dialog.getByLabel('Check').selectOption({ value: CHECK_KEY });
    // Match kind defaults to "exact" already (ItemDecisionDialog's own
    // initial state) — selected by value here anyway, so a future change
    // to that default cannot silently turn this into a glob match.
    await dialog.getByLabel('Match kind').selectOption({ value: 'exact' });
    await dialog.getByLabel('Pattern').fill(MOUNT);

    const matched = dialog.getByText(/^Matches \d+ item/);
    try {
      // Host-scoped, and this host reports exactly one disk item — the
      // exact count, not merely "at least one", is "the preview's count"
      // the brief asks this spec to see.
      await expect(matched).toHaveText('Matches 1 item', { timeout: 5_000 });
      return dialog;
    } catch {
      // Not discovered yet — close and give the next tick a chance.
    }
    await dialog.getByRole('button', { name: 'Cancel' }).click();
    await expect(dialog).toHaveCount(0);
    if (Date.now() > deadline) {
      throw new Error(`timed out waiting for ${hostName}'s mount ${MOUNT} to be discovered`);
    }
    await page.waitForTimeout(3_000);
  }
}

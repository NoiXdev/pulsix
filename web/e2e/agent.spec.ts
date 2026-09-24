/**
 * The v0.3a agent journey (backlog 3.4a, UI half): an operator opens the
 * Agents page, enrols an agent, and watches the host it created appear —
 * monitored, tagged from its own facts, reporting.
 *
 * The agent is the real binary (web/e2e/agent.ts starts what global-setup
 * built), talking to the manager's real gRPC surface. Nothing here is
 * mocked: the token the dialog prints is the token the agent redeems, and
 * the host row that appears is one no fixture created.
 *
 * The Go half of the same gate (internal/e2e/agent_test.go) proves the
 * protocol — enrollment, binding, renewal, revocation. This spec proves
 * the operator can drive it.
 */
import { test, expect } from '@playwright/test';
import { ADMIN_STORAGE_STATE, ORG } from './env.ts';
import { agentBinaryAvailable, startAgent, type RunningAgent } from './agent.ts';

test.use({ storageState: ADMIN_STORAGE_STATE });

// Skip (never silently pass) when global-setup could not build the agent:
// every assertion below would otherwise hang waiting for a host that
// nothing is going to create.
test.skip(!agentBinaryAvailable(), 'the agent binary was not built by global-setup (see web/e2e/agent.ts)');

const base = `/o/${ORG.slug}`;
const AGENT_HOST = 'agent-e2e-host';

test('an operator enrols an agent and the host it creates starts reporting', async ({ page }) => {
  let agent: RunningAgent | undefined;
  try {
    // --- The operator creates an enrollment token ----------------------
    await page.goto(`${base}/agents`);
    await expect(page.getByRole('heading', { name: 'Agents' })).toBeVisible();
    await page.getByRole('button', { name: /Enrol agent/ }).click();

    await page.getByLabel('Host name (optional)').fill(AGENT_HOST);
    await page.getByRole('button', { name: 'Create token' }).click();

    // The token is shown once, with the command that uses it.
    const token = (await page.getByLabel('Enrollment token').textContent())?.trim() ?? '';
    expect(token).toMatch(/^pxa1\./);
    await expect(page.getByLabel('Install command')).toContainText('/install.sh');
    await expect(page.getByLabel('Install command')).toContainText(token);
    await page.getByRole('button', { name: 'Done' }).click();

    // Until it is redeemed the token waits on the page, without its secret.
    await expect(page.getByText('Pending enrollment token')).toBeVisible();
    await expect(page.getByText(AGENT_HOST, { exact: true }).first()).toBeVisible();

    // --- The agent redeems it ------------------------------------------
    agent = startAgent(token, AGENT_HOST);

    // The page polls on the heartbeat cadence; the row appears on its own.
    const row = page.getByRole('link', { name: AGENT_HOST });
    await expect(row).toBeVisible({ timeout: 60_000 });
    await expect(page.getByText('online')).toBeVisible({ timeout: 60_000 });

    // --- The host it created carries what the agent reported -----------
    await row.click();
    await expect(page.getByRole('heading', { name: AGENT_HOST })).toBeVisible();
    // The Agent card, with the state the list showed.
    const agentCard = page.locator('section, div').filter({ hasText: 'Agent' }).first();
    await expect(agentCard).toBeVisible();
    // Facts the agent collected from the machine it runs on, and the
    // automatic tag derived from them.
    await expect(page.getByText('Facts')).toBeVisible();
    await expect(page.getByText(/^os$/).first()).toBeVisible({ timeout: 60_000 });
    // Enrolment is what makes a host monitored.
    await expect(page.getByRole('switch', { name: /Monitored/i })).toBeChecked();

    // --- Revoking ends it ----------------------------------------------
    await page.goto(`${base}/agents`);
    page.once('dialog', (d) => void d.accept());
    await page.getByRole('button', { name: `Revoke agent on ${AGENT_HOST}` }).click();
    await expect(page.getByText('No agents enrolled yet.')).toBeVisible({ timeout: 30_000 });
  } finally {
    await agent?.stop();
  }
});

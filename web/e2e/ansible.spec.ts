/**
 * The Ansible E2E journey (backlog 2.12a) — the milestone gate's UI half:
 * operator logs in → generates an SSH credential and installs its public
 * key on the fixture host → adds the fixture host and a jump host with its
 * own credential, made the zone default → runs the connection test for the
 * host THROUGH the bastion and pins the observed host key → registers the
 * fixture git repository (deploy key) and syncs it → writes an editor
 * playbook → creates a job template scoped to the host with the `greeting`
 * extra var → runs it → the run view streams the log live, shows the
 * per-host `ok` and the stats → then the failure path: a second template
 * whose playbook fails, the failed host marked, the template edited so the
 * retry's second attempt succeeds, and `Retry failed` starting a new run.
 *
 * The compose-stack contract (the fixture service names, the git URL)
 * lives in ./ansible-env.ts; everything the spec needs to run — the pulsix
 * worker claiming the jobs, the ssh-host/bastion/git-server fixtures — is
 * wired by the compose stack. The worker image is Task 17's deliverable,
 * so until that merge the probe below answers "no" and the spec skips with
 * a visible reason; CI's `e2e-ansible-ui` job activates with it (see
 * .github/workflows/ci.yml).
 */
import { test, expect, type Page } from '@playwright/test';
import { ADMIN_STORAGE_STATE, ORG } from './env.ts';
import { SSH_FIXTURE_HOST, BASTION_FIXTURE_HOST, GIT_FIXTURE_URL, FIXTURE_REPO_BRANCH } from './ansible-env.ts';
import { workerServiceAvailable } from './worker.ts';
import { pickOption } from './pickOption.ts';

test.use({ storageState: ADMIN_STORAGE_STATE });

// Skip (never silently pass) while the worker service is absent: the run
// view would otherwise queue jobs forever and every assertion would hang.
test.skip(!workerServiceAvailable(), 'pulsix worker service is not running in the compose test stack (the worker image is Task 17\'s deliverable)');

const base = `/o/${ORG.slug}`;
const HOST_NAME = 'ansible-e2e-host';
const GREETING = 'hello from the e2e';

const HELLO_PLAYBOOK = `- name: Pulsix greeting
  hosts: all
  gather_facts: false
  tasks:
    - name: Pulsix greeting
      ansible.builtin.debug:
        msg: "{{ greeting }}"
`;

const FAIL_PLAYBOOK = `- name: Pulsix deliberate failure
  hosts: all
  gather_facts: false
  tasks:
    - name: Pulsix deliberate failure
      ansible.builtin.fail:
        msg: "pulsix e2e deliberate failure"
      when: should_fail | default('true') | bool
`;

async function toast(page: Page, text: string | RegExp): Promise<void> {
  await expect(page.getByText(text).first()).toBeVisible();
}

/** addGeneratedCredential creates a key credential in generate mode and
 * dismisses the one-time public-key panel; the private key never shows. */
async function addGeneratedCredential(page: Page, name: string, kindLabel: string, username: string): Promise<void> {
  await page.goto(`${base}/credentials`);
  await page.getByRole('button', { name: 'Add credential' }).click();
  const dialog = page.getByRole('dialog', { name: 'Add credential' });
  await pickOption(dialog, 'Kind', kindLabel);
  await dialog.getByLabel('Name', { exact: true }).fill(name);
  await dialog.getByLabel('Username').fill(username);
  await dialog.getByRole('button', { name: 'Create credential' }).click();
  const keyDialog = page.getByRole('dialog', { name: 'Install the public key' });
  await expect(keyDialog.getByLabel('Public key', { exact: true })).toContainText(`pulsix:${ORG.slug}:${name}`);
  await expect(page.locator('body')).not.toContainText('PRIVATE KEY');
  await keyDialog.getByRole('button', { name: 'Done' }).click();
}

test('run a playbook against a managed host through the bastion, then retry the failure path', async ({ page }) => {
  // --- Credentials: the host login, the bastion login and the deploy key.
  await addGeneratedCredential(page, 'ansible-e2e-login', 'SSH key', 'ansible');
  await addGeneratedCredential(page, 'ansible-e2e-bastion', 'SSH key', 'ops');
  await addGeneratedCredential(page, 'ansible-e2e-deploy', 'Git deploy key', 'git');

  // --- The jump host and the zone default -----------------------------
  await page.goto(`${base}/jump-hosts`);
  await page.getByRole('button', { name: 'Add jump host' }).click();
  let dialog = page.getByRole('dialog', { name: 'Add jump host' });
  // Not exact: BastionDialog also has a "Username" field.
  await dialog.getByLabel('Name', { exact: true }).fill('ansible-e2e-edge');
  await dialog.getByLabel('Address').fill(BASTION_FIXTURE_HOST);
  await dialog.getByLabel('Username').fill('ops');
  await pickOption(dialog, 'Credential', 'ansible-e2e-bastion');
  // "Create jump host", not "Save": BastionDialog's own button text is
  // conditional on editing vs. creating (v0.4g Task 7) and this dialog has
  // no `bastion` prop — a pre-existing mismatch this spec never caught
  // until Task 9 first ran it (this journey also skips itself, see the
  // worker-service guard above, so it had not run even then).
  await dialog.getByRole('button', { name: 'Create jump host' }).click();
  await toast(page, 'Jump host ansible-e2e-edge saved');

  // The default zone's default jump host: every host in it routes through
  // the bastion unless something nearer overrides. JumpHostsPage.tsx labels
  // this row's picker "<zone name> <zone slug>" — "Default default" for the
  // seeded zone — so pickOption's exact match needs the whole thing, not
  // the substring match the pre-Select version used.
  await pickOption(page, 'Default default', 'ansible-e2e-edge');
  await toast(page, 'Default jump host of Default saved');

  // --- The managed host, its SSH settings and the connection test ------
  await page.goto(`${base}/hosts`);
  await page.getByRole('button', { name: 'Add host' }).click();
  dialog = page.getByRole('dialog', { name: 'Add host' });
  await dialog.getByLabel('Name').fill(HOST_NAME);
  await dialog.getByLabel('Address').fill(SSH_FIXTURE_HOST);
  await pickOption(dialog, 'Zone', 'Default');
  await dialog.getByLabel('Managed (Ansible via SSH)').check();
  await dialog.getByRole('button', { name: 'Create host' }).click();
  await toast(page, `Host ${HOST_NAME} created`);

  await page.getByRole('link', { name: HOST_NAME, exact: true }).click();
  await page.getByLabel('User').fill('ansible');
  await pickOption(page, 'Credential', 'ansible-e2e-login');
  await page.getByRole('button', { name: 'Save SSH settings' }).click();
  await toast(page, 'SSH settings saved');

  // The connection test goes THROUGH the zone default bastion, and the
  // observed host key is pinned (trust on first use).
  await page.getByRole('button', { name: 'Run connection test' }).click();
  await toast(page, 'Connection test started');
  await expect(page.getByRole('table', { name: 'Per-host results' })).toBeVisible();
  await expect(page.getByText('connected (via', { exact: false })).toBeVisible();
  await page.goBack();

  await expect(page.getByRole('button', { name: 'Pin host key' })).toBeEnabled({ timeout: 30_000 });
  await page.getByRole('button', { name: 'Pin host key' }).click();
  await toast(page, 'Host key pinned');

  // --- The fixture git repository and its sync -------------------------
  await page.goto(`${base}/repos`);
  await page.getByRole('button', { name: 'Add repository' }).click();
  dialog = page.getByRole('dialog', { name: 'Add repository' });
  await dialog.getByLabel('Name', { exact: true }).fill('ansible-e2e-repo');
  await dialog.getByLabel('URL').fill(GIT_FIXTURE_URL);
  await dialog.getByLabel('Branch').fill(FIXTURE_REPO_BRANCH);
  await pickOption(dialog, 'Credential', 'ansible-e2e-deploy');
  await dialog.getByRole('button', { name: 'Create repository' }).click();
  await toast(page, 'Repository ansible-e2e-repo created');

  await page.getByRole('button', { name: 'Sync ansible-e2e-repo' }).click();
  await toast(page, 'Sync of ansible-e2e-repo started');
  await expect(page.getByText('synced', { exact: false })).toBeVisible({ timeout: 60_000 });

  // --- The editor playbook and the job template ------------------------
  await page.goto(`${base}/playbooks`);
  await page.getByRole('button', { name: 'New editor playbook' }).click();
  dialog = page.getByRole('dialog', { name: 'New editor playbook' });
  await dialog.getByLabel('Playbook YAML', { exact: false }).fill(HELLO_PLAYBOOK);
  await dialog.getByRole('button', { name: 'Save playbook' }).click();
  await toast(page, /playbook saved/i);

  await page.goto(`${base}/templates`);
  await page.getByRole('button', { name: 'Add template' }).click();
  dialog = page.getByRole('dialog', { name: 'New job template' });
  await dialog.getByLabel('Name').fill('ansible-e2e-hello');
  await pickOption(dialog, 'Playbook', 'Pulsix greeting');
  await dialog.getByRole('checkbox', { name: `Include ${HOST_NAME}` }).check();
  // The typed extra var: a schema field makes the value form appear.
  await dialog.getByRole('button', { name: 'Add field' }).click();
  let editor = page.getByRole('group', { name: 'Field field_1' });
  await editor.getByLabel('Name').fill('greeting');
  await dialog.getByLabel('greeting').fill(GREETING);
  await dialog.getByRole('button', { name: 'Create template' }).click();
  await toast(page, 'Template ansible-e2e-hello created');

  // --- The run: live log, per-host ok, stats ---------------------------
  await page.getByRole('button', { name: 'Run ansible-e2e-hello' }).click();
  await expect(page).toHaveURL(/\/runs\/[0-9a-f-]+$/);
  await expect(page.getByRole('table', { name: 'Per-host results' })).toBeVisible({ timeout: 60_000 });
  // The fixture's final task lands ok with the greeting the extra var
  // carried — the run view streamed it live.
  await expect(
    page.getByRole('list', { name: 'Run events' }).locator('li', { hasText: 'Pulsix greeting' }).last(),
  ).toContainText('hello', { timeout: 60_000 });
  await expect(page.getByRole('cell', { name: HOST_NAME })).toBeVisible();
  await expect(page.getByRole('table', { name: 'Per-host results' }).getByText('ok', { exact: true })).toBeVisible();
  await expect(page.getByText('succeeded', { exact: true })).toBeVisible();

  // --- The failure path and the retry ----------------------------------
  await page.goto(`${base}/playbooks`);
  await page.getByRole('button', { name: 'New editor playbook' }).click();
  dialog = page.getByRole('dialog', { name: 'New editor playbook' });
  await dialog.getByLabel('Playbook YAML', { exact: false }).fill(FAIL_PLAYBOOK);
  await dialog.getByRole('button', { name: 'Save playbook' }).click();
  await toast(page, /playbook saved/i);

  await page.goto(`${base}/templates`);
  await page.getByRole('button', { name: 'Add template' }).click();
  dialog = page.getByRole('dialog', { name: 'New job template' });
  await dialog.getByLabel('Name').fill('ansible-e2e-fail');
  await pickOption(dialog, 'Playbook', 'Pulsix deliberate failure');
  await dialog.getByRole('checkbox', { name: `Include ${HOST_NAME}` }).check();
  await dialog.getByRole('button', { name: 'Add field' }).click();
  editor = page.getByRole('group', { name: 'Field field_1' });
  await editor.getByLabel('Name').fill('should_fail');
  await dialog.getByLabel('should_fail').fill('true');
  await dialog.getByRole('button', { name: 'Create template' }).click();
  await toast(page, 'Template ansible-e2e-fail created');

  await page.getByRole('button', { name: 'Run ansible-e2e-fail' }).click();
  await expect(page).toHaveURL(/\/runs\/[0-9a-f-]+$/);
  await expect(page.getByText('failed', { exact: true })).toBeVisible({ timeout: 60_000 });
  await expect(page.getByRole('table', { name: 'Per-host results' }).getByText('failed', { exact: true })).toBeVisible();
  const failedUrl = page.url();

  // The fix: the template's extra var flips through the same UI the
  // operator would use, then the run view's retry action re-runs only
  // what failed.
  await page.goto(`${base}/templates`);
  await page.getByRole('button', { name: /^ansible-e2e-fail/ }).click();
  dialog = page.getByRole('dialog', { name: 'ansible-e2e-fail' });
  await dialog.getByLabel('should_fail').fill('false');
  await dialog.getByRole('button', { name: 'Save template' }).click();
  await toast(page, 'Template ansible-e2e-fail saved');

  await page.goto(failedUrl);
  await page.getByRole('button', { name: 'Retry failed' }).click();
  await toast(page, 'Retry started');
  await expect(page).toHaveURL(/\/runs\/[0-9a-f-]+$/);
  await expect(page.getByText('succeeded', { exact: true })).toBeVisible({ timeout: 60_000 });
  await expect(page.getByRole('table', { name: 'Per-host results' }).getByText('ok', { exact: true })).toBeVisible();
});

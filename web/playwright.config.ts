import { defineConfig, devices } from '@playwright/test';
import { BASE_URL } from './e2e/env.ts';

/**
 * The eight journeys are not independent: setup.spec creates the instance
 * (admin, org "acme", TOTP), and login/passkey/hosts/members/activity/
 * tenancy/inventory all assume that instance already exists — most of them load
 * setup.spec's own captured browser storage state (env.ADMIN_STORAGE_STATE)
 * to start already signed in, rather than re-running the wizard.
 *
 * That ordering is declared here, not left to alphabetical file-name luck:
 * each spec is its own Playwright *project*, and every project after
 * `setup` names it (transitively, via the previous project) as a
 * `dependencies` entry — Playwright refuses to start a project until every
 * project it depends on has finished, and aborts the whole run if a
 * dependency fails, which is exactly the semantics "setup, then login,
 * then the rest" needs. `fullyParallel` stays off and there is exactly one
 * worker: these projects share one running manager and one database, and
 * several specs (members → activity, hosts → activity) rely on an earlier
 * spec's writes already being visible.
 */
export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 60_000,
  expect: { timeout: 10_000 },
  reporter: process.env.CI ? [['list'], ['html', { open: 'never', outputFolder: 'playwright-report' }]] : [['list']],
  globalSetup: './e2e/global-setup.ts',
  use: {
    baseURL: BASE_URL,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    video: 'retain-on-failure',
    ...devices['Desktop Chrome'],
  },
  projects: [
    { name: 'setup', testMatch: /setup\.spec\.ts/ },
    { name: 'login', testMatch: /login\.spec\.ts/, dependencies: ['setup'] },
    { name: 'passkey', testMatch: /passkey\.spec\.ts/, dependencies: ['login'] },
    { name: 'hosts', testMatch: /hosts\.spec\.ts/, dependencies: ['passkey'] },
    { name: 'members', testMatch: /members\.spec\.ts/, dependencies: ['hosts'] },
    { name: 'activity', testMatch: /activity\.spec\.ts/, dependencies: ['members'] },
    { name: 'tenancy', testMatch: /tenancy\.spec\.ts/, dependencies: ['activity'] },
    { name: 'inventory', testMatch: /inventory\.spec\.ts/, dependencies: ['tenancy'] },
    // The Ansible E2E (backlog 2.12a) rides on the pulsix worker service
    // in the compose test stack — Task 17's image — so its spec self-skips
    // (with a visible reason, via web/e2e/worker.ts) until that merge;
    // the project is wired so it activates without config changes then.
    // The agent journey (backlog 3.4a, v0.3a): the real agent binary
    // enrolling against this stack's manager. It needs no compose service
    // — global-setup builds the binary and the manager exposes its gRPC
    // surface — so unlike `ansible` below it runs for real from the start.
    { name: 'agent', testMatch: /agent\.spec\.ts/, dependencies: ['inventory'] },
    { name: 'ansible', testMatch: /ansible\.spec\.ts/, dependencies: ['agent'] },
  ],
});

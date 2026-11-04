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
    // The rule pipeline's GUI half (v0.3b-2, backlog 3.4a): rules.spec
    // needs only the organization setup.spec already created; fleet.spec
    // needs the agent binary agent.spec's project already built, so it
    // sits after both.
    { name: 'rules', testMatch: /rules\.spec\.ts/, dependencies: ['agent'] },
    { name: 'fleet', testMatch: /fleet\.spec\.ts/, dependencies: ['rules'] },
    // The item decision pipeline's GUI half (v0.3b-3, backlog 3.6c): one
    // real agent, same as fleet.spec.ts, so it sits right after it in the
    // chain for the same reason — it needs the agent binary built.
    { name: 'items', testMatch: /items\.spec\.ts/, dependencies: ['fleet'] },
    // The alert lifecycle's GUI half (v0.4a, closeout task): one more real
    // agent, same reason items.spec.ts sits right after fleet.spec.ts —
    // it needs the agent binary built and a rule to drive a real tuple to
    // crit.
    { name: 'alerts', testMatch: /alerts\.spec\.ts/, dependencies: ['items'] },
    // The scheduler and notification pages (backlog 2.78). None of them
    // needs an agent or a worker: maintenance.spec and calendar.spec work
    // on organization-wide windows, and notifications.spec sends its test
    // message through the stack's own mailpit, the way members.spec reads
    // an invitation. They sit here, after the agent-driven journeys, only
    // because calendar.spec asserts that exactly ONE maintenance window is
    // on the calendar — its own — and an earlier spec leaving one behind
    // would be indistinguishable from a bug.
    { name: 'maintenance', testMatch: /maintenance\.spec\.ts/, dependencies: ['alerts'] },
    { name: 'calendar', testMatch: /calendar\.spec\.ts/, dependencies: ['maintenance'] },
    { name: 'notifications', testMatch: /notifications\.spec\.ts/, dependencies: ['calendar'] },
    // The v0.2c pages (backlog 2.68), for the same reason the three
    // above were cheap: neither needs an agent or a worker.
    // workers.spec stops where a real worker would begin — enrolling for
    // real needs the worker IMAGE in this stack, which backlog 2.96 is
    // waiting on, the same thing ansible.spec self-skips for.
    // triggers.spec hangs its webhook on a REPOSITORY rather than a job
    // template, because a template needs a playbook, a playbook needs a
    // synced repository, and syncing is a job — which needs that same
    // worker. The schedules page is blocked on it outright: a schedule
    // has nothing to point at without a template.
    { name: 'workers', testMatch: /workers\.spec\.ts/, dependencies: ['notifications'] },
    { name: 'triggers', testMatch: /triggers\.spec\.ts/, dependencies: ['workers'] },
    // Account administration (backlog 7.74): the administrator sets a
    // temporary password for members.spec's operator, who must then change
    // it, and the operator then resets it through a mailed link. It
    // changes that operator's password, so it runs after every spec that
    // signs the operator in with the original one.
    { name: 'accounts', testMatch: /accounts\.spec\.ts/, dependencies: ['triggers'] },
    { name: 'ansible', testMatch: /ansible\.spec\.ts/, dependencies: ['accounts'] },
  ],
});

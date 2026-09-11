import { test, expect } from '@playwright/test';
import { ADMIN, ADMIN_STORAGE_STATE, ORG } from './env.ts';

test.use({ storageState: ADMIN_STORAGE_STATE });

test('earlier actions appear in the feed with the actor\'s name, and filtering by object type narrows it', async ({
  page,
}) => {
  await page.goto(`/o/${ORG.slug}/settings/activity`);
  await expect(page.getByRole('heading', { name: 'Activity' })).toBeVisible();

  // hosts.spec created a host named "web-04.fra" as the administrator;
  // members.spec invited an operator by e-mail. Both are real audited
  // events (internal/api's withAudit + setAuditObject on those routes),
  // recorded by the time this spec — which depends on both — runs.
  const hostEvent = page.getByText('created host web-04.fra');
  const memberEvent = page.getByText('invited ops@example.test as operator');
  await expect(hostEvent).toBeVisible();
  await expect(memberEvent).toBeVisible();

  // The actor is resolved to a real display name, not a kind/uuid — this
  // is finding "Important 6" from task-15a-report.md, and the assertion
  // that would fail if the resolution regressed to `user · <uuid>`. Not
  // exact/unique: the admin performed most of the actions in this run, so
  // the name legitimately appears many times (feed rows, the actor
  // filter's own <select>, the shell footer) — `.first()` only asserts it
  // is resolved at all, which is what this line exists to check.
  await expect(page.getByText(ADMIN.name).first()).toBeVisible();

  // Filtering by object type "host" must hide the member-kind events and
  // keep the host-kind ones — the brief's own success criterion.
  await page.getByRole('button', { name: 'host', exact: true }).click();
  await expect(hostEvent).toBeVisible();
  await expect(memberEvent).toHaveCount(0);

  // Back to "all" restores the member event.
  await page.getByRole('button', { name: 'all', exact: true }).click();
  await expect(memberEvent).toBeVisible();
});

import { describe, expect, it } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../../test/mockApi.ts';
import { ADMIN } from '../../../test/renderRoutes.tsx';
import { renderListPage } from '../../../test/renderListPage.tsx';
import { pickOption } from '../../../test/pickOption.ts';
import { acceptConfirm } from '../../../test/confirmDialog.ts';
import { chooseRowAction, firstCells, hideColumn, numbered, openRowMenu, sortBy } from '../../../test/wholeList.ts';
import { SharedReposPage } from './SharedReposPage.tsx';
import { SharedTemplatesPage } from './SharedTemplatesPage.tsx';
import { SharedChecksPage } from './SharedChecksPage.tsx';

const PREFS = { density: 'standard', page_size: 25, lists: {} };
const repo = (name: string, over: Record<string, unknown> = {}) => ({ id: `r-${name}`, name, url: `https://git/${name}.git`, branch: 'main', subpath: '', ...over });
const tpl = (name: string, over: Record<string, unknown> = {}) => ({ id: `t-${name}`, name, shared_repo_id: 'r-base-0000', playbook_path: 'site.yml', limit: '', ...over });
const check = (key: string, over: Record<string, unknown> = {}) => ({ id: `c-${key}`, key, plugin: 'cpu', interval_seconds: 60, ...over });

const base = (extra: Record<string, unknown>) => ({
  'GET /api/v1/auth/me': ADMIN,
  'GET /api/v1/auth/me/preferences': PREFS,
  'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
  ...extra,
});
const others = [{ path: '/settings/shared/repos/$id', component: () => <div>detail page</div> }];

describe('SharedReposPage list', () => {
  function setup(locale?: 'de', definitions: unknown[] = numbered('repo', 30).map((n) => repo(n))) {
    mockApi(base({ 'GET /api/v1/admin/repos/definitions': { definitions }, 'DELETE /api/v1/admin/repos/definitions/r-repo30': {} }));
    return renderListPage({ path: '/settings/shared/repos', component: SharedReposPage, initial: '/o/acme/settings/shared/repos', locale, others });
  }

  it('sorts by name in the browser, repo9 before repo10', async () => {
    const user = userEvent.setup();
    const router = setup();
    await screen.findByText('1–25 of 30 repository definitions');
    await sortBy(user, 'Name');
    await waitFor(() => expect(firstCells().slice(0, 10)).toEqual(['repo1', 'repo2', 'repo3', 'repo4', 'repo5', 'repo6', 'repo7', 'repo8', 'repo9', 'repo10']));
    expect(router.state.location.search).toMatchObject({ sort: 'name', order: 'asc' });
  });

  it('opens the same confirmation from the row menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 repository definitions');
    await chooseRowAction(user, 'repo30', 'Delete…');
    expect(await acceptConfirm(user)).toBe('Delete repository definition repo30? This removes it from every organization.');
  });

  it('lists Open and Delete… in the row menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 repository definitions');
    await openRowMenu(user, 'repo30');
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Open', 'Delete…']);
  });

  it('keeps the read-only hint the old count line carried', async () => {
    setup();
    expect(await screen.findByText('Projected into every organization, read-only there.')).toBeInTheDocument();
  });

  it('hides a column from the Columns menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 repository definitions');
    await hideColumn(user, 'Branch');
    expect(screen.queryByRole('columnheader', { name: 'Branch' })).not.toBeInTheDocument();
  });

  it('pages 30 rows and follows the page size', async () => {
    const user = userEvent.setup();
    setup();
    expect(await screen.findByText('1–25 of 30 repository definitions')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Next page' }));
    expect(await screen.findByText('26–30 of 30 repository definitions')).toBeInTheDocument();
    await pickOption(screen, 'Rows per page', '10');
    expect(await screen.findByText('1–10 of 30 repository definitions')).toBeInTheDocument();
  });

  it('writes the footer in German', async () => {
    setup('de');
    expect(await screen.findByText('1–25 von 30 Repository-Definitionen')).toBeInTheDocument();
  });
});

describe('SharedTemplatesPage list', () => {
  function setup(locale?: 'de', definitions: unknown[] = numbered('tpl', 30).map((n) => tpl(n))) {
    mockApi(
      base({
        'GET /api/v1/admin/templates/definitions': { definitions },
        'GET /api/v1/admin/repos/definitions': { definitions: [repo('base-0000', { id: 'r-base-0000', name: 'Base repo' })] },
        'DELETE /api/v1/admin/templates/definitions/t-tpl30': {},
      }),
    );
    return renderListPage({ path: '/settings/shared/templates', component: SharedTemplatesPage, initial: '/o/acme/settings/shared/templates', locale, others });
  }

  it('sorts by name in the browser, tpl9 before tpl10', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 job template definitions');
    await sortBy(user, 'Name');
    await waitFor(() => expect(firstCells().slice(0, 10)).toEqual(['tpl1', 'tpl2', 'tpl3', 'tpl4', 'tpl5', 'tpl6', 'tpl7', 'tpl8', 'tpl9', 'tpl10']));
  });

  it('shows the repository by name', async () => {
    setup(undefined, [tpl('one')]);
    expect(await screen.findByText('Base repo')).toBeInTheDocument();
  });

  it('opens the same confirmation from the row menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 job template definitions');
    await chooseRowAction(user, 'tpl30', 'Delete…');
    expect(await acceptConfirm(user)).toBe('Delete job template definition tpl30? This removes it from every organization.');
  });

  it('hides a column from the Columns menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 job template definitions');
    await hideColumn(user, 'Limit');
    expect(screen.queryByRole('columnheader', { name: 'Limit' })).not.toBeInTheDocument();
  });

  it('pages 30 rows and follows the page size', async () => {
    setup();
    expect(await screen.findByText('1–25 of 30 job template definitions')).toBeInTheDocument();
    await pickOption(screen, 'Rows per page', '10');
    expect(await screen.findByText('1–10 of 30 job template definitions')).toBeInTheDocument();
  });

  it('writes the footer in German', async () => {
    setup('de');
    expect(await screen.findByText('1–25 von 30 Job-Vorlagen-Definitionen')).toBeInTheDocument();
  });
});

describe('SharedChecksPage list', () => {
  function setup(locale?: 'de', definitions: unknown[] = numbered('chk', 30).map((n) => check(n))) {
    mockApi(base({ 'GET /api/v1/admin/checks/definitions': { definitions }, 'DELETE /api/v1/admin/checks/definitions/c-chk30': {} }));
    return renderListPage({ path: '/settings/shared/checks', component: SharedChecksPage, initial: '/o/acme/settings/shared/checks', locale, others });
  }

  it('sorts by key in the browser, chk9 before chk10', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 check definitions');
    await sortBy(user, 'Key');
    await waitFor(() => expect(firstCells().slice(0, 10)).toEqual(['chk1', 'chk2', 'chk3', 'chk4', 'chk5', 'chk6', 'chk7', 'chk8', 'chk9', 'chk10']));
  });

  it('sorts the interval as a number', async () => {
    const user = userEvent.setup();
    setup(undefined, [check('a', { interval_seconds: 100 }), check('b', { interval_seconds: 20 }), check('c', { interval_seconds: 90 })]);
    await screen.findByText('1–3 of 3 check definitions');
    await sortBy(user, 'Interval');
    await waitFor(() => expect(firstCells()).toEqual(['b', 'c', 'a']));
  });

  it('opens the same confirmation from the row menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 check definitions');
    await chooseRowAction(user, 'chk30', 'Delete…');
    expect(await acceptConfirm(user)).toBe('Delete check definition chk30? This removes it from every organization.');
  });

  it('hides a column from the Columns menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 check definitions');
    await hideColumn(user, 'Plugin');
    expect(screen.queryByRole('columnheader', { name: 'Plugin' })).not.toBeInTheDocument();
  });

  it('pages 30 rows and follows the page size', async () => {
    setup();
    expect(await screen.findByText('1–25 of 30 check definitions')).toBeInTheDocument();
    await pickOption(screen, 'Rows per page', '10');
    expect(await screen.findByText('1–10 of 30 check definitions')).toBeInTheDocument();
  });

  it('writes the footer in German', async () => {
    setup('de');
    expect(await screen.findByText('1–25 von 30 Check-Definitionen')).toBeInTheDocument();
  });
});

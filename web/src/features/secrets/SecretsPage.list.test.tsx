import { describe, expect, it } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { pickOption } from '../../test/pickOption.ts';
import { acceptConfirm } from '../../test/confirmDialog.ts';
import { chooseRowAction, firstCells, hideColumn, numbered, sortBy } from '../../test/wholeList.ts';
import { SecretsPage } from './SecretsPage.tsx';

const PREFS = { density: 'standard', page_size: 25, lists: {} };
const org = (role: string) => ({ org: { id: 'o', name: 'Acme', slug: 'acme', role, created_at: '' } });
const secret = (name: string, over: Record<string, unknown> = {}) => ({
  name, field_names: ['username'], created_at: '2026-11-01T10:00:00Z', updated_at: '2026-11-10T10:00:00Z', used_by: [], ...over,
});

function setup(opts: { locale?: 'de'; role?: string; secrets?: unknown[]; onDelete?: () => void } = {}) {
  mockApi({
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
    'GET /api/v1/orgs/acme': org(opts.role ?? 'admin'),
    'GET /api/v1/orgs/acme/secrets': { secrets: opts.secrets ?? numbered('s', 30).map((n) => secret(n)) },
    'DELETE /api/v1/orgs/acme/secrets/s30': () => {
      opts.onDelete?.();
      return {};
    },
  });
  return renderListPage({ path: '/settings/secrets', component: SecretsPage, initial: '/o/acme/settings/secrets', locale: opts.locale });
}

describe('SecretsPage list', () => {
  it('sorts by name in the browser, s9 before s10', async () => {
    const user = userEvent.setup();
    const router = setup();
    await screen.findByText('1–25 of 30 secrets');
    await sortBy(user, 'Name');
    await waitFor(() => expect(firstCells().slice(0, 10)).toEqual(['s1', 's2', 's3', 's4', 's5', 's6', 's7', 's8', 's9', 's10']));
    expect(router.state.location.search).toMatchObject({ sort: 'name', order: 'asc' });
  });

  it('sorts the last change by time', async () => {
    const user = userEvent.setup();
    setup({ secrets: [secret('a', { updated_at: '2026-03-01T00:00:00Z' }), secret('b', { updated_at: '2026-01-01T00:00:00Z' }), secret('c', { updated_at: '2026-02-01T00:00:00Z' })] });
    await screen.findByText('1–3 of 3 secrets');
    await sortBy(user, 'Last change');
    await waitFor(() => expect(firstCells()).toEqual(['b', 'c', 'a']));
  });

  it('has Replace and Delete in the row menu, the same confirmation, no inline buttons', async () => {
    const user = userEvent.setup();
    let deleted = 0;
    setup({ onDelete: () => deleted++ });
    await screen.findByText('1–25 of 30 secrets');
    expect(screen.queryByRole('button', { name: 'Delete' })).not.toBeInTheDocument();
    await chooseRowAction(user, 's30', 'Delete');
    expect(await acceptConfirm(user)).toBe('Delete secret s30? This cannot be undone.');
    await waitFor(() => expect(deleted).toBe(1));
  });

  it('answers Delete on a secret a check uses with the blocking dialog, not a confirmation', async () => {
    const user = userEvent.setup();
    setup({ secrets: [secret('s30', { used_by: ['snmp'] })] });
    await chooseRowAction(user, 's30', 'Delete');
    expect(await screen.findByRole('dialog', { name: /in use/i })).toBeInTheDocument();
  });

  it('gives a viewer no row menu', async () => {
    setup({ role: 'viewer' });
    await screen.findByText('1–25 of 30 secrets');
    expect(screen.queryAllByRole('button', { name: /^Actions for/ })).toEqual([]);
  });

  it('hides a column from the Columns menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 secrets');
    await hideColumn(user, 'Fields');
    expect(screen.queryByRole('columnheader', { name: 'Fields' })).not.toBeInTheDocument();
  });

  it('pages 30 rows and follows the page size', async () => {
    const user = userEvent.setup();
    setup();
    expect(await screen.findByText('1–25 of 30 secrets')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Next page' }));
    expect(await screen.findByText('26–30 of 30 secrets')).toBeInTheDocument();
    await pickOption(screen, 'Rows per page', '10');
    expect(await screen.findByText('1–10 of 30 secrets')).toBeInTheDocument();
  });

  it('writes the footer in German', async () => {
    setup({ locale: 'de' });
    expect(await screen.findByText('1–25 von 30 Secrets')).toBeInTheDocument();
  });
});

import { describe, expect, it } from 'vitest';
import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi } from '../../test/mockApi.ts';
import { clippedElements } from '../../test/clipped.ts';
import { check, GROUPS, org, renderPage } from './checksPageTestKit.tsx';

describe('ChecksPage rows', () => {
  it('moves edit, copy and delete into the row menu', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': {
        checks: [
          check(),
          check({ id: 'c8', key: 'disk-shared', plugin: 'disk', origin: 'shared', shared_definition_id: 'sd1' }),
          check({ id: 'c2', key: 'disk-data', plugin: 'disk', origin: 'custom' }),
        ],
      },
    });
    renderPage();

    // The old inline buttons are gone.
    await screen.findByRole('button', { name: 'Actions for cpu (Organization)' });
    expect(screen.queryByRole('button', { name: /^(Edit|Copy|Delete) / })).not.toBeInTheDocument();
    const items = async (name: string) => {
      await user.click(screen.getByRole('button', { name: `Actions for ${name} (Organization)` }));
      const found = screen.getAllByRole('menuitem').map((el) => el.textContent);
      await user.keyboard('{Escape}');
      return found;
    };
    // A seeded definition can be edited, copied and deleted, a shared one
    // (installation-managed) only edited and copied, a custom one edited and
    // deleted.
    expect(await items('cpu')).toEqual(['Edit', 'Copy', 'Delete…']);
    expect(await items('disk-shared')).toEqual(['Edit', 'Copy']);
    expect(await items('disk-data')).toEqual(['Edit', 'Delete…']);
  });

  it('the binding link still opens the host, inside the static row', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': {
        checks: [check({ id: 'c3', key: 'nginx', plugin: 'service', origin: 'custom', binding_kind: 'host', host_id: 'h1', host_name: 'web-1' })],
      },
    });
    const { container } = renderPage();

    const link = await screen.findByRole('link', { name: 'web-1' });
    expect(link.closest('[data-list-row]')).not.toBeNull();
    await user.click(link);
    expect(await screen.findByText('HOST VIEW')).toBeInTheDocument();
    expect(container.querySelector('[data-list-row] > a')).toBeNull();
  });

  it('rows are not links', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': { checks: [check()] },
    });
    const { container } = renderPage();
    await screen.findByRole('button', { name: 'Actions for cpu (Organization)' });
    expect(container.querySelectorAll('[data-list-row]').length).toBeGreaterThan(0);
    expect(container.querySelector('[data-list-row] > a')).toBeNull();
  });

  it('no clipped cards', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': { checks: [check()] },
    });
    const { container } = renderPage();
    await screen.findByRole('button', { name: 'Actions for cpu (Organization)' });
    expect(clippedElements(container)).toEqual([]);
  });

  // Task 7 (v0.2f): a projection of an installation-owned definition
  // (origin "shared") offers Copy — the same reason a seeded row does,
  // since key/plugin/params/interval are fixed here — but, UNLIKE a seeded
  // row, never Delete: spec decision 6 refuses that from inside an
  // organization outright, and offering a button that always 403s is
  // exactly the mistake RepoDialog.tsx's own projected-repo guard avoids.
  it('offers Copy but never Delete on a shared (installation-managed) definition', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': {
        checks: [check({ id: 'c8', key: 'disk-data', plugin: 'disk', origin: 'shared', shared_definition_id: 'sd1' })],
      },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'Actions for disk-data (Organization)' }));
    expect(screen.getByRole('menuitem', { name: 'Copy' })).toBeInTheDocument();
    expect(screen.getByRole('menuitem', { name: 'Edit' })).toBeInTheDocument();
    expect(screen.queryByRole('menuitem', { name: 'Delete…' })).not.toBeInTheDocument();
  });

  it('offers a viewer no row menu', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('viewer'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': { checks: [check(), check({ id: 'c2', key: 'disk-data', plugin: 'disk', origin: 'custom' })] },
    });
    renderPage();

    expect(await screen.findAllByText('cpu')).toHaveLength(2);
    expect(screen.queryByRole('button', { name: /^Actions for / })).not.toBeInTheDocument();
  });

  it('names the binding in each row menu, so two checks with one key stay apart', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': {
        checks: [
          check(),
          check({ id: 'c2', origin: 'custom', binding_kind: 'host', host_id: 'h1', host_name: 'web-01' }),
        ],
      },
    });
    renderPage();

    expect(await screen.findByRole('button', { name: 'Actions for cpu (Organization)' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Actions for cpu (web-01)' })).toBeInTheDocument();
  });
});

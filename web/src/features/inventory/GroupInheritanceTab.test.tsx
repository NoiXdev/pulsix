import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { group, routes, renderAt } from './groupPageTestKit.tsx';

const SCHEMA = {
  fields: [
    { name: 'env', type: 'string', required: true },
    { name: 'level', type: 'enum', options: ['info', 'debug'], default: 'info' },
    { name: 'domain', type: 'string', scope: 'host' },
  ],
};

/* web (g1) below prod; web-01 in web, web-02 in web_eu (below web) and in
 * backup, a top-level group of its own. */
const INVENTORY = {
  all: { children: ['backup', 'prod'] },
  backup: { vars: { env: 'backup', retention: 7 }, hosts: ['web-02'] },
  prod: { vars: { env: 'staging', ntp: 'ntp.example.test', level: 'debug' }, children: ['web'] },
  web: { vars: { env: 'prod', level: 'info' }, children: ['web_eu'], hosts: ['web-01'] },
  web_eu: { vars: { env: 'eu' }, hosts: ['web-02'] },
  _meta: {
    hostvars: {
      'web-01': { ansible_host: '10.0.0.1', domain: 'web-01.acme.io' },
      'web-02': { ansible_host: 'web-02', db_password: '********' },
    },
  },
};

const preview = (inventory: unknown) => ({ inventory, connections: {}, warnings: [] });

function setup(inventory: unknown = INVENTORY, extra: Record<string, unknown> = {}) {
  mockApi(
    routes('admin', {
      'GET /api/v1/orgs/acme/inventory/groups/g1': group({ schema: SCHEMA, vars: { env: 'prod' } }),
      'GET /api/v1/orgs/acme/inventory/preview': preview(inventory),
      ...extra,
    }),
  );
  return renderAt('/o/acme/groups/g1?tab=inheritance');
}

const row = (key: string) => screen.getByTestId(`inheritance-${key}`);
const states = (key: string) => [...row(key).querySelectorAll('td[data-state]')].map((td) => td.getAttribute('data-state'));
const headers = () => within(screen.getByRole('table', { name: 'Variable inheritance' })).getAllByRole('columnheader').map((th) => th.textContent);
const result = (key: string) => row(key).querySelector('td[data-result]')!.textContent;

describe('GroupInheritanceTab', () => {
  it('shows the group view: each level from weakest to strongest, the winner, the overridden and the unset', async () => {
    setup();
    await screen.findByRole('table', { name: 'Variable inheritance' });
    expect(headers()).toEqual(['Variable', 'prodparent group', 'schema defaultof web', 'webthis group', 'Result']);
    expect(screen.getAllByRole('rowheader').map((th) => th.textContent)).toEqual(['env', 'level', 'ntp']);

    // env: the group's own value beats prod's.
    expect(states('env')).toEqual(['lost', 'unset', 'win']);
    expect(within(row('env')).getByText('staging').tagName).toBe('S');
    expect(within(row('env')).getByText('prod')).toBeInTheDocument();
    expect(result('env')).toBe('web · this group');
    // level: unset in web, so its schema default — part of web's level —
    // beats prod's debug.
    expect(states('level')).toEqual(['lost', 'win', 'unset']);
    expect(result('level')).toBe('schema default · of web');
    // ntp: only prod sets it; the other levels show a dot.
    expect(states('ntp')).toEqual(['win', 'unset', 'unset']);
    expect(within(row('ntp')).getAllByText('not set')).toHaveLength(2);
    expect(result('ntp')).toBe('prod · parent group');

    const legend = screen.getByRole('list', { name: 'Legend' });
    expect(within(legend).getAllByRole('listitem').map((li) => li.textContent)).toEqual([
      'value the value Ansible uses',
      'value overridden by a stronger level',
      '· not set at this level',
    ]);
    expect(screen.getByText(/schema defaults belong to the group itself/)).toBeInTheDocument();
    expect(screen.queryByText(/same depth merge in alphabetical order/)).not.toBeInTheDocument();
  });

  it('switches to a host: its other groups, its subgroup and its own values, with a picker among the group\'s hosts', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByRole('table', { name: 'Variable inheritance' });
    expect(screen.getByRole('button', { name: 'Group web' })).toHaveAttribute('aria-pressed', 'true');
    expect(screen.queryByRole('combobox', { name: 'Host' })).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Host' }));
    expect(screen.getByRole('button', { name: 'Host' })).toHaveAttribute('aria-pressed', 'true');
    // The first host by name; web-01 is a direct member.
    expect(screen.getByRole('combobox', { name: 'Host' })).toHaveValue('web-01');
    expect(headers()).toEqual(['Variable', 'prodparent group', 'schema defaultof web', 'webthis group', 'web-01host', 'Result']);
    expect(states('domain')).toEqual(['unset', 'unset', 'unset', 'win']);
    expect(result('ansible_host')).toBe('web-01 · host');
    expect(screen.getByText(/its connection settings; they beat every group/)).toBeInTheDocument();

    // web-02 is in web_eu, below web, and in backup elsewhere in the tree.
    await pickOption(screen, 'Host', 'web-02');
    await waitFor(() =>
      expect(headers()).toEqual([
        'Variable', 'backupother group of the host', 'prodparent group', 'schema defaultof web', 'webthis group', 'web_eusubgroup', 'web-02host', 'Result',
      ]),
    );
    expect(states('env')).toEqual(['lost', 'lost', 'unset', 'lost', 'win', 'unset']);
    expect(result('env')).toBe('web_eu · subgroup');
    // backup sits at prod's depth but leaves ntp unset: depth alone decides.
    expect(result('ntp')).toBe('prod · parent group');
    // A masked secret stays masked.
    expect(within(row('db_password')).getByText('********')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Group web' }));
    expect(headers()).toHaveLength(5);
  });

  it('says when groups of the same depth were decided by their names', async () => {
    const user = userEvent.setup();
    setup({ ...INVENTORY, backup: { vars: { ntp: 'backup.ntp' }, hosts: ['web-02'] } });
    await screen.findByRole('table', { name: 'Variable inheritance' });
    await user.click(screen.getByRole('button', { name: 'Host' }));
    await pickOption(screen, 'Host', 'web-02');
    await waitFor(() => expect(result('ntp')).toBe('prod · parent groupsame depth as backup: alphabetical order decides'));
    expect(screen.getByText(/same depth merge in alphabetical order/)).toBeInTheDocument();
  });

  it('reads a group without managed hosts from the organization\'s preview, and offers no host view', async () => {
    const asked: string[] = [];
    setup(undefined, {
      'GET /api/v1/orgs/acme/inventory/preview': (req: MockRequest) => {
        const g = req.search.get('group') ?? '';
        asked.push(g);
        // Scoped to web, nothing renders: no managed host is at or below it.
        return preview(g === 'g1' ? { all: {}, _meta: { hostvars: {} } } : { ...INVENTORY, web: { vars: { env: 'prod' } } });
      },
    });
    await screen.findByRole('table', { name: 'Variable inheritance' });
    expect(asked).toEqual(['g1', '']);
    expect(states('env')).toEqual(['lost', 'unset', 'win']);
    expect(screen.getByRole('button', { name: 'Host' })).toBeDisabled();
    expect(screen.getByText('No managed host is in this group or its subgroups.')).toBeInTheDocument();
  });

  it('says so when no variable is set anywhere on the path', async () => {
    mockApi(
      routes('admin', {
        'GET /api/v1/orgs/acme/inventory/preview': preview({ all: { children: ['prod'] }, prod: { children: ['web'] }, web: { hosts: ['web-01'] }, _meta: { hostvars: { 'web-01': {} } } }),
        'GET /api/v1/orgs/acme/inventory/groups/g1': group({ schema: { fields: [] }, vars: {} }),
      }),
    );
    renderAt('/o/acme/groups/g1?tab=inheritance');
    expect(await screen.findByText('No variable is set anywhere on this path.')).toBeInTheDocument();
  });
});

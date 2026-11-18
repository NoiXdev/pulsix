import { describe, expect, it } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { pickOption } from '../../test/pickOption.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { useListFilters } from '../lists/filterSearch.ts';
import { FleetFilters } from './FleetFilters.tsx';
import { FLEET_FILTERS, validateFleetSearch } from './fleetSearch.ts';

const ZONES = [
  { id: '00000000-0000-4000-8000-000000000003', name: 'Frankfurt', slug: 'fra' },
  { id: '00000000-0000-4000-8000-000000000009', name: 'Amsterdam', slug: 'ams' },
];
const GROUPS = [{ id: '00000000-0000-4000-8000-000000000004', name: 'Web tier', selector: 'role=web', priority: 0, created_at: '', updated_at: '' }];

function Harness({ tagError }: { tagError?: string }) {
  const list = useListFilters(FLEET_FILTERS);
  return <FleetFilters zones={ZONES} groups={GROUPS} list={list} tagError={tagError} />;
}

function setup(initial = '/o/acme/fleet', tagError?: string) {
  return renderListPage({ path: '/fleet', component: () => <Harness tagError={tagError} />, initial, extraSearch: (s) => ({ ...validateFleetSearch(s) }) });
}

describe('FleetFilters', () => {
  it('writes a zone selection to the URL', async () => {
    const router = setup();
    await screen.findByLabelText('Filter by zone');
    await pickOption(screen, 'Filter by zone', { value: '00000000-0000-4000-8000-000000000009' });
    await waitFor(() => expect(router.state.location.search).toEqual({ zone: '00000000-0000-4000-8000-000000000009' }));
  });

  it('clears the zone filter when "All zones" is chosen again', async () => {
    const router = setup('/o/acme/fleet?zone=00000000-0000-4000-8000-000000000003&page=2');
    await screen.findByLabelText('Filter by zone');
    await pickOption(screen, 'Filter by zone', { value: '' });
    await waitFor(() => expect(router.state.location.search).toEqual({}));
  });

  it('writes a monitoring group selection by name', async () => {
    const router = setup();
    await screen.findByLabelText('Filter by zone');
    await pickOption(screen, 'Filter by monitoring group', 'Web tier');
    await waitFor(() => expect(router.state.location.search).toEqual({ group: '00000000-0000-4000-8000-000000000004' }));
  });

  it('writes the state to the URL', async () => {
    const router = setup();
    await screen.findByLabelText('Filter by zone');
    await pickOption(screen, 'Filter by state', 'crit');
    await waitFor(() => expect(router.state.location.search).toEqual({ state: 'crit' }));
  });

  it('writes a tag as a single key or key=value token, never building a selector', async () => {
    const user = userEvent.setup();
    const router = setup();
    // A plain text field, not a selector builder: there is nowhere here
    // that offers "!=" or "in(...)" — the API refuses those with a 422
    // (v0.3b-2 controller decision).
    await user.type(await screen.findByLabelText('Filter by tag'), 'role=db');
    await waitFor(() => expect(router.state.location.search).toEqual({ tag: 'role=db' }));
  });

  it('surfaces the API’s own refusal of an unsupported tag expression', async () => {
    setup('/o/acme/fleet?tag=!%3Dweb', 'tag must be a single key or key=value token');
    expect(await screen.findByRole('alert')).toHaveTextContent('tag must be a single key or key=value token');
  });

  // v0.4g final fix round: Zone and Monitoring group each sit next to a
  // <label>, not wrapped by one (an <li role="option"> is not interactive
  // content, so a wrapping label without `htmlFor` fired a synthetic click
  // at the input on every row click, reopening the list it had just
  // closed). This one checks the list itself.
  it('closes the list after picking a zone, rather than reopening it', async () => {
    const user = userEvent.setup();
    setup();
    await user.click(await screen.findByLabelText('Filter by zone'));
    expect(screen.getAllByRole('option').length).toBeGreaterThan(0);
    await user.click(screen.getByRole('option', { name: 'Frankfurt' }));
    expect(screen.queryAllByRole('option')).toHaveLength(0);
  });
});

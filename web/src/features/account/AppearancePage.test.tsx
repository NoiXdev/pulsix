import { afterEach, describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { renderRoutes } from '../../test/renderRoutes.tsx';
import type { Preferences } from '../../api/types.ts';
import { AppearancePage } from './AppearancePage.tsx';

const ME = {
  user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' },
  mfa_complete: true,
  totp_confirmed: true,
  instance_admin: false,
  locale: null,
  effective_locale: 'en',
};

let puts: Preferences[] = [];

function setup(prefs: Partial<Preferences> = {}, locale?: 'de') {
  puts = [];
  mockApi({
    'GET /api/v1/auth/me': ME,
    'GET /api/v1/setup/status': { completed: true, version: 'dev', default_locale: 'en' },
    'GET /api/v1/auth/me/preferences': { density: 'standard', page_size: 25, lists: {}, ...prefs },
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => {
      puts.push(req.body as Preferences);
      return req.body;
    },
  });
  return renderRoutes([{ path: '/appearance', component: AppearancePage }], '/o/acme/appearance', undefined, locale);
}

afterEach(() => {
  puts = [];
});

describe('AppearancePage', () => {
  it('offers three density cards as a radio group and marks the saved one', async () => {
    setup({ density: 'comfortable' });

    const group = await screen.findByRole('radiogroup', { name: 'Density' });
    const radios = within(group).getAllByRole('radio');
    expect(radios.map((r) => r.textContent)).toEqual([
      expect.stringContaining('Comfortable'),
      expect.stringContaining('Standard'),
      expect.stringContaining('Compact'),
    ]);
    await waitFor(() => expect(within(group).getByRole('radio', { name: /Comfortable/ })).toBeChecked());
    expect(within(group).getByRole('radio', { name: /Compact/ })).not.toBeChecked();
  });

  it('saves the density when a card is chosen', async () => {
    const user = userEvent.setup();
    setup({ lists: { hosts: { columns: ['name'] } } });

    const compact = await screen.findByRole('radio', { name: /Compact/ });
    await waitFor(() => expect(compact).toBeEnabled());
    await user.click(compact);

    await waitFor(() => expect(puts).toEqual([{ density: 'compact', page_size: 25, lists: { hosts: { columns: ['name'] } } }]));
    expect(compact).toBeChecked();
  });

  it('moves the choice with the arrow keys', async () => {
    const user = userEvent.setup();
    setup();

    const standard = await screen.findByRole('radio', { name: /Standard/ });
    await waitFor(() => expect(standard).toBeEnabled());
    standard.focus();
    await user.keyboard('{ArrowRight}');

    await waitFor(() => expect(puts).toEqual([{ density: 'compact', page_size: 25, lists: {} }]));
    expect(screen.getByRole('radio', { name: /Compact/ })).toHaveFocus();
  });

  it('saves the page size', async () => {
    setup();

    await screen.findByRole('radiogroup', { name: 'Density' });
    await waitFor(() => expect(screen.getByLabelText('Rows per page')).toBeEnabled());
    await pickOption(screen, 'Rows per page', '50');

    await waitFor(() => expect(puts).toEqual([{ density: 'standard', page_size: 50, lists: {} }]));
  });

  it('lists the saved column choices and resets one list with a single save', async () => {
    const user = userEvent.setup();
    setup({ lists: { hosts: { columns: ['name', 'zone'] }, runs: { columns: ['run'] }, gone: { columns: ['x'] } } });

    expect(await screen.findByText('Hosts')).toBeInTheDocument();
    expect(screen.getByText('Runs')).toBeInTheDocument();
    // A list this build does not know is shown as its id.
    expect(screen.getByText('gone')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Reset Hosts' }));

    await waitFor(() =>
      expect(puts).toEqual([{ density: 'standard', page_size: 25, lists: { runs: { columns: ['run'] }, gone: { columns: ['x'] } } }]),
    );
    await waitFor(() => expect(screen.queryByText('Hosts')).not.toBeInTheDocument());
  });

  it('says so when no list has its own columns', async () => {
    setup();
    expect(await screen.findByText('No list has its own columns yet.')).toBeInTheDocument();
  });

  it('has the language card, moved here from the security page', async () => {
    setup();
    expect(await screen.findByLabelText('Language')).toBeInTheDocument();
  });

  it('speaks German', async () => {
    setup({ lists: { runs: { columns: ['run'] } } }, 'de');
    expect(await screen.findByRole('radiogroup', { name: 'Dichte' })).toBeInTheDocument();
    expect(screen.getByRole('radio', { name: /Kompakt/ })).toBeInTheDocument();
    expect(await screen.findByRole('button', { name: 'Läufe zurücksetzen' })).toBeInTheDocument();
  });
});

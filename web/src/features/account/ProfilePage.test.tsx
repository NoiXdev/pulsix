import { describe, expect, it } from 'vitest';
import { screen, within } from '@testing-library/react';
import { mockApi } from '../../test/mockApi.ts';
import { renderRoutes } from '../../test/renderRoutes.tsx';
import { ProfilePage } from './ProfilePage.tsx';

const ME = { user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' }, mfa_complete: true, totp_confirmed: true, instance_admin: false };
const ORGS = {
  orgs: [
    { id: 'o1', slug: 'acme', name: 'Acme', role: 'owner', created_at: '2026-01-01T00:00:00Z' },
    { id: 'o2', slug: 'beta', name: 'Beta', role: 'viewer', created_at: '2026-01-01T00:00:00Z' },
  ],
};

function setup(orgs: unknown = ORGS) {
  mockApi({ 'GET /api/v1/auth/me': ME, 'GET /api/v1/orgs': orgs });
  renderRoutes([{ path: '/profile', component: ProfilePage }], '/o/acme/profile');
}

describe('ProfilePage', () => {
  it('shows the name, the email and every organization with the role there', async () => {
    setup();

    expect(await screen.findByRole('heading', { name: 'Profile' })).toBeInTheDocument();
    expect(await screen.findByText('Tim R.')).toBeInTheDocument();
    expect(screen.getByText('tim@acme.io')).toBeInTheDocument();
    const acme = screen.getByText('Acme').closest('li') as HTMLElement;
    expect(within(acme).getByText('Owner')).toBeInTheDocument();
    const beta = screen.getByText('Beta').closest('li') as HTMLElement;
    expect(within(beta).getByText('Viewer')).toBeInTheDocument();
  });

  it('is read-only and says an administrator changes the name and email', async () => {
    setup();

    await screen.findByText('Tim R.');
    expect(screen.getByText('Your name and email are changed by an administrator.')).toBeInTheDocument();
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument();
    expect(screen.queryByRole('button')).not.toBeInTheDocument();
  });

  it('has a card with the Name and Email rows and an Organizations card', async () => {
    setup();

    await screen.findByText('Tim R.');
    const account = screen.getByRole('region', { name: 'My account' });
    expect(within(account).getByText('Name')).toBeInTheDocument();
    expect(within(account).getByText('Email')).toBeInTheDocument();
    expect(within(screen.getByRole('region', { name: 'Organizations' })).getByText('Acme')).toBeInTheDocument();
  });

  it('says so when the person belongs to no organization', async () => {
    setup({ orgs: [] });
    expect(await screen.findByText('You do not belong to any organization yet.')).toBeInTheDocument();
  });
});

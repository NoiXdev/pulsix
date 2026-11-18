import { describe, expect, it } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ToastProvider } from '../../ui/toast.tsx';
import { I18nProvider } from '../../i18n/I18nProvider.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { SettingsCard } from '../../ui/settings.tsx';
import { PersonalTokensRow } from './PersonalTokensCard.tsx';

function renderCard(canWrite: boolean, locale?: 'de') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const card = (
    <ToastProvider>
      <SettingsCard title="Access">
        <PersonalTokensRow org="acme" canWrite={canWrite} />
      </SettingsCard>
    </ToastProvider>
  );
  render(<QueryClientProvider client={qc}>{locale ? <I18nProvider locale={locale}>{card}</I18nProvider> : card}</QueryClientProvider>);
}

function routes(allowed: boolean, puts: unknown[] = []) {
  let current = allowed;
  return {
    'GET /api/v1/orgs/acme/settings/personal-tokens': () => ({ allowed: current }),
    'PUT /api/v1/orgs/acme/settings/personal-tokens': (req: MockRequest) => {
      puts.push(req.body);
      current = (req.body as { allowed: boolean }).allowed;
      return { allowed: current };
    },
  };
}

describe('PersonalTokensRow', () => {
  it('lets an admin turn personal tokens off', async () => {
    const user = userEvent.setup();
    const puts: unknown[] = [];
    mockApi(routes(true, puts));
    renderCard(true);
    const toggle = await screen.findByRole('switch', { name: 'Allow personal tokens' });
    await waitFor(() => expect(toggle).toHaveAttribute('aria-checked', 'true'));
    expect(screen.getByText(/stops every personal token here at once/)).toBeInTheDocument();
    await user.click(toggle);
    await waitFor(() => expect(puts).toEqual([{ allowed: false }]));
    await waitFor(() => expect(screen.getByRole('switch', { name: 'Allow personal tokens' })).toHaveAttribute('aria-checked', 'false'));
    expect(await screen.findByText('Personal tokens turned off')).toBeInTheDocument();
  });

  it('shows a viewer the state in a disabled switch', async () => {
    mockApi(routes(false));
    renderCard(false);
    const toggle = await screen.findByRole('switch', { name: 'Allow personal tokens' });
    await waitFor(() => expect(toggle).toHaveAttribute('aria-checked', 'false'));
    expect(toggle).toBeDisabled();
  });

  it('speaks German', async () => {
    mockApi(routes(true));
    renderCard(false, 'de');
    expect(await screen.findByRole('switch', { name: 'Persönliche Tokens erlauben' })).toBeInTheDocument();
    expect(screen.getByText('Persönliche Tokens erlauben')).toBeInTheDocument();
  });
});

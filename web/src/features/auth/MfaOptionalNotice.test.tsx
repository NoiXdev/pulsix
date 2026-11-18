import { describe, expect, it } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { I18nProvider } from '../../i18n/I18nProvider.tsx';
import { mockApi } from '../../test/mockApi.ts';
import { fetchMock } from '../../test/fetchMock.ts';
import { MfaOptionalNotice } from './MfaOptionalNotice.tsx';

function renderNotice(locale: 'en' | 'de' = 'en') {
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <I18nProvider locale={locale}>
        <MfaOptionalNotice />
      </I18nProvider>
    </QueryClientProvider>,
  );
}

describe('MfaOptionalNotice (PULSIX_MFA_OPTIONAL)', () => {
  it('says the second factor is optional while the instance runs in that mode', async () => {
    mockApi({ 'GET /api/v1/setup/status': { completed: true, version: 'dev', default_locale: 'en', mfa_optional: true } });
    renderNotice();

    const strip = await screen.findByRole('status');
    expect(strip).toHaveTextContent('Demo mode: the second factor is optional on this instance.');
    // Persistent: nothing to dismiss it with.
    expect(screen.queryByRole('button')).not.toBeInTheDocument();
  });

  it('speaks German', async () => {
    mockApi({ 'GET /api/v1/setup/status': { completed: true, version: 'dev', default_locale: 'de', mfa_optional: true } });
    renderNotice('de');

    expect(await screen.findByRole('status')).toHaveTextContent('Demo-Modus: Der zweite Faktor ist auf dieser Instanz optional.');
  });

  it('shows nothing when the second factor is mandatory', async () => {
    mockApi({ 'GET /api/v1/setup/status': { completed: true, version: 'dev', default_locale: 'en', mfa_optional: false } });
    renderNotice();

    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    expect(screen.queryByRole('status')).not.toBeInTheDocument();
  });
});

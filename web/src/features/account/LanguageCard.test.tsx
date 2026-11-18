import { describe, expect, it } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { I18nProvider } from '../../i18n/I18nProvider.tsx';
import { LanguageCard } from './LanguageCard.tsx';

const ME = {
  user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' },
  mfa_complete: true,
  totp_confirmed: true,
  instance_admin: false,
};

function renderCard() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <I18nProvider>
        <ToastProvider>
          <LanguageCard />
        </ToastProvider>
      </I18nProvider>
    </QueryClientProvider>,
  );
}

describe('LanguageCard', () => {
  it('offers the instance default, English and Deutsch, and switches the page on a choice (Review Focus 3)', async () => {
    const saved: unknown[] = [];
    let mine: 'en' | 'de' | null = null;
    mockApi({
      'GET /api/v1/auth/me': () => ({ ...ME, locale: mine, effective_locale: mine ?? 'en' }),
      'GET /api/v1/setup/status': { completed: true, version: 'dev', default_locale: 'en' },
      'PUT /api/v1/auth/me/locale': (req: { body: unknown }) => {
        saved.push(req.body);
        mine = (req.body as { locale: 'en' | 'de' | null }).locale;
        return { locale: mine, effective_locale: mine ?? 'en' };
      },
    });
    renderCard();

    const field = await screen.findByLabelText('Interface language');
    await waitFor(() => expect(field).toHaveValue('Instance default (English)'));

    await pickOption(screen, 'Interface language', 'Deutsch');
    // The whole page follows, not just the card.
    expect(await screen.findByRole('heading', { name: 'Sprache' })).toBeInTheDocument();
    expect(saved).toEqual([{ locale: 'de' }]);
    expect(document.documentElement.lang).toBe('de');

    await pickOption(screen, 'Sprache der Oberfläche', 'Standard der Instanz (English)');
    expect(await screen.findByRole('heading', { name: 'Language' })).toBeInTheDocument();
    expect(saved).toEqual([{ locale: 'de' }, { locale: null }]);
  });

  it('shows the API error when saving fails', async () => {
    mockApi({
      'GET /api/v1/auth/me': { ...ME, locale: null, effective_locale: 'en' },
      'GET /api/v1/setup/status': { completed: true, version: 'dev', default_locale: 'en' },
      'PUT /api/v1/auth/me/locale': () =>
        new Response(JSON.stringify({ detail: 'this needs a signed-in session' }), {
          status: 403,
          headers: { 'Content-Type': 'application/json' },
        }),
    });
    renderCard();
    await screen.findByLabelText('Interface language');
    await pickOption(screen, 'Interface language', 'Deutsch');
    const toast = await screen.findByRole('status');
    expect(within(toast).getByText('this needs a signed-in session')).toBeInTheDocument();
  });
});

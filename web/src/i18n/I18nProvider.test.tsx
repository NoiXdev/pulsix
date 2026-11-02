import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { mockApi, type MockRequest } from '../test/mockApi.ts';
import { fetchMock } from '../test/fetchMock.ts';
import { refetchMe } from '../api/hooks/useMe.ts';
import { I18nProvider } from './I18nProvider.tsx';
import { useLocale, useT } from './useT.ts';
import { useFormat } from './format.ts';

const ME = {
  user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' },
  mfa_complete: true,
  totp_confirmed: true,
  instance_admin: false,
};

const UNAUTHORIZED = () =>
  new Response(JSON.stringify({ detail: 'unauthorized' }), {
    status: 401,
    headers: { 'Content-Type': 'application/json' },
  });

/** Shows one catalogue string, the active locale, and a button per locale
 * that calls setLocale — enough to watch the whole tree switch. */
function Consumer() {
  const t = useT();
  const { locale, setLocale } = useLocale();
  return (
    <div>
      <span data-testid="label">{t.language.label}</span>
      <span data-testid="locale">{locale}</span>
      <button type="button" onClick={() => void setLocale('de')}>
        to-de
      </button>
    </div>
  );
}

function renderWithProvider(qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })) {
  render(
    <QueryClientProvider client={qc}>
      <I18nProvider>
        <Consumer />
      </I18nProvider>
    </QueryClientProvider>,
  );
  return qc;
}

beforeEach(() => {
  localStorage.clear();
  document.documentElement.lang = '';
  fetchMock.mockReset();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('I18nProvider resolves the language (Review Focus 2)', () => {
  it("uses the signed-in user's effective_locale from /auth/me", async () => {
    mockApi({
      'GET /api/v1/auth/me': { ...ME, locale: null, effective_locale: 'de' },
      'GET /api/v1/setup/status': { completed: true, version: 'dev', default_locale: 'en' },
    });
    renderWithProvider();
    await waitFor(() => expect(screen.getByTestId('label')).toHaveTextContent('Sprache'));
    expect(document.documentElement.lang).toBe('de');
  });

  it('uses the instance default from /setup/status when nobody is signed in', async () => {
    mockApi({
      'GET /api/v1/auth/me': UNAUTHORIZED,
      'GET /api/v1/setup/status': { completed: true, version: 'dev', default_locale: 'de' },
    });
    renderWithProvider();
    await waitFor(() => expect(screen.getByTestId('label')).toHaveTextContent('Sprache'));
  });

  it("prefers the user's choice over the instance default", async () => {
    mockApi({
      'GET /api/v1/auth/me': { ...ME, locale: 'en', effective_locale: 'en' },
      'GET /api/v1/setup/status': { completed: true, version: 'dev', default_locale: 'de' },
    });
    renderWithProvider();
    // Both answers have arrived once the status query settles; the user's
    // own English must win over the German instance default.
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.getByTestId('locale')).toHaveTextContent('en'));
    expect(screen.getByTestId('label')).toHaveTextContent('Language');
  });

  it.each([
    ['de-DE', 'de', 'Sprache'],
    ['fr-FR', 'en', 'Language'],
  ])('before setup, a browser language of %s starts the wizard in %s', async (browser, locale, label) => {
    vi.spyOn(navigator, 'language', 'get').mockReturnValue(browser);
    mockApi({
      'GET /api/v1/auth/me': UNAUTHORIZED,
      'GET /api/v1/setup/status': { completed: false, version: 'dev', default_locale: 'en' },
    });
    renderWithProvider();
    await waitFor(() => expect(screen.getByTestId('locale')).toHaveTextContent(locale));
    expect(screen.getByTestId('label')).toHaveTextContent(label);
  });

  it('does not wait for the network: the first render uses the last known language', () => {
    localStorage.setItem('pulsix.locale', 'de');
    // Neither request ever answers.
    fetchMock.mockImplementation(() => new Promise<Response>(() => {}));
    renderWithProvider();
    expect(screen.getByTestId('label')).toHaveTextContent('Sprache');
  });

  it('renders English first when nothing is known yet', () => {
    fetchMock.mockImplementation(() => new Promise<Response>(() => {}));
    renderWithProvider();
    expect(screen.getByTestId('label')).toHaveTextContent('Language');
  });

  it('follows a new session after the cache is cleared (sign-in)', async () => {
    let signedIn = false;
    mockApi({
      'GET /api/v1/auth/me': () => (signedIn ? { ...ME, locale: 'de', effective_locale: 'de' } : UNAUTHORIZED()),
      'GET /api/v1/setup/status': { completed: true, version: 'dev', default_locale: 'en' },
    });
    const qc = renderWithProvider();
    // The signed-out answer has to land before the session changes, or
    // this would only test a first fetch.
    await waitFor(() => expect(qc.getQueryState(['me'])?.status).toBe('error'));
    await waitFor(() => expect(qc.getQueryState(['setup-status'])?.status).toBe('success'));
    expect(screen.getByTestId('label')).toHaveTextContent('Language');

    // What the login page does: reset the cache, then fetch /auth/me with
    // the new session cookie.
    signedIn = true;
    await act(async () => {
      qc.clear();
      await refetchMe(qc);
    });
    await waitFor(() => expect(screen.getByTestId('label')).toHaveTextContent('Sprache'));
  });
});

describe('setLocale (Review Focus 3)', () => {
  it('saves the choice and re-renders in German without a reload', async () => {
    const user = userEvent.setup();
    let saved: unknown;
    mockApi({
      'GET /api/v1/auth/me': { ...ME, locale: null, effective_locale: 'en' },
      'GET /api/v1/setup/status': { completed: true, version: 'dev', default_locale: 'en' },
      'PUT /api/v1/auth/me/locale': (req: MockRequest) => {
        saved = req.body;
        return { locale: 'de', effective_locale: 'de' };
      },
    });
    renderWithProvider();
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(screen.getByTestId('label')).toHaveTextContent('Language');

    await user.click(screen.getByRole('button', { name: 'to-de' }));

    await waitFor(() => expect(screen.getByTestId('label')).toHaveTextContent('Sprache'));
    expect(saved).toEqual({ locale: 'de' });
    expect(document.documentElement.lang).toBe('de');
  });
});

describe('a fixed locale', () => {
  it('renders the given language without any request', () => {
    render(
      <I18nProvider locale="de">
        <Consumer />
      </I18nProvider>,
    );
    expect(screen.getByTestId('label')).toHaveTextContent('Sprache');
    expect(document.documentElement.lang).toBe('de');
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('is English with no provider at all, which is how the page tests render', () => {
    render(<Consumer />);
    expect(screen.getByTestId('label')).toHaveTextContent('Language');
  });
});

describe('useFormat', () => {
  function Amount() {
    return <span data-testid="amount">{useFormat().number(1234.5)}</span>;
  }

  it('formats for the active language', () => {
    render(
      <I18nProvider locale="de">
        <Amount />
      </I18nProvider>,
    );
    expect(screen.getByTestId('amount')).toHaveTextContent('1.234,5');
  });
});

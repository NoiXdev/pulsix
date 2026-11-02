import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { I18nProvider } from '../../i18n/I18nProvider.tsx';
import { SetupWizardPage } from './SetupWizardPage.tsx';

function renderWizard({ i18n = false }: { i18n?: boolean } = {}) {
  const rootRoute = createRootRoute();
  const indexRoute = createRoute({ getParentRoute: () => rootRoute, path: '/', component: () => <div>ROOT</div> });
  const setupRoute = createRoute({ getParentRoute: () => rootRoute, path: '/setup', component: SetupWizardPage });
  const securityRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/account/enrol',
    component: () => <div>ENROLMENT PAGE</div>,
  });
  const routeTree = rootRoute.addChildren([indexRoute, setupRoute, securityRoute]);
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: ['/setup'] }) });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });

  const tree = <RouterProvider router={router} />;
  render(
    <QueryClientProvider client={qc}>{i18n ? <I18nProvider>{tree}</I18nProvider> : tree}</QueryClientProvider>,
  );
}

/**
 * Important 2 (task-15a-report.md fix round 1): the SMTP step validated
 * its zod schema (host required, a valid "from" address, ...) even with
 * SMTP switched off and its fields unrendered, so "Continue" silently did
 * nothing and a first installation could never complete without mail.
 */
describe('SetupWizardPage — completing the wizard with SMTP off', () => {
  it('reaches the final step and completes setup with smtp omitted from the request', async () => {
    const user = userEvent.setup();
    let completeBody: unknown;
    mockApi({
      'GET /api/v1/setup/status': { completed: false },
      'POST /api/v1/setup/complete': (req: MockRequest) => {
        completeBody = req.body;
        return {};
      },
      'GET /api/v1/auth/me': {
        user: { id: 'u1', email: 'root@acme.io', display_name: 'Root' },
        mfa_complete: false,
        totp_confirmed: false,
        instance_admin: true,
      },
    });

    renderWizard();

    await user.type(await screen.findByLabelText('Name'), 'Root Admin');
    await user.type(screen.getByLabelText('Email address'), 'root@acme.io');
    await user.type(screen.getByLabelText('Password'), 'correct horse battery staple');
    await user.type(screen.getByLabelText('Repeat password'), 'correct horse battery staple');
    await user.click(screen.getByRole('button', { name: 'Continue' }));

    await user.type(await screen.findByLabelText('Organization name'), 'Acme Hosting');
    await user.type(screen.getByLabelText('URL slug'), 'acme');
    await user.click(screen.getByRole('button', { name: 'Continue' }));

    // The SMTP step: leave "Configure SMTP now" unchecked and continue.
    // Before the fix, this silently failed the hidden schema's
    // validation and never advanced past this point.
    await screen.findByRole('heading', { name: 'Outgoing mail' });
    await user.click(screen.getByRole('button', { name: 'Continue' }));

    expect(await screen.findByText('Enrol a second factor now')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Create administrator & continue' }));

    expect(await screen.findByText('ENROLMENT PAGE')).toBeInTheDocument();
    expect(completeBody).toMatchObject({ admin_email: 'root@acme.io', org_slug: 'acme' });
    // smtp: undefined is dropped by JSON serialization the same way the
    // real request body would be — the key must be entirely absent, not
    // present with some empty/default value the hidden fields left behind.
    expect(completeBody).not.toHaveProperty('smtp');
  });

  it('validates SMTP fields normally once "Configure SMTP now" is checked', async () => {
    const user = userEvent.setup();
    mockApi({ 'GET /api/v1/setup/status': { completed: false } });
    renderWizard();

    await user.type(await screen.findByLabelText('Name'), 'Root Admin');
    await user.type(screen.getByLabelText('Email address'), 'root@acme.io');
    await user.type(screen.getByLabelText('Password'), 'correct horse battery staple');
    await user.type(screen.getByLabelText('Repeat password'), 'correct horse battery staple');
    await user.click(screen.getByRole('button', { name: 'Continue' }));

    await user.type(await screen.findByLabelText('Organization name'), 'Acme Hosting');
    await user.type(screen.getByLabelText('URL slug'), 'acme');
    await user.click(screen.getByRole('button', { name: 'Continue' }));

    await user.click(await screen.findByLabelText('Configure SMTP now'));
    await user.click(screen.getByRole('button', { name: 'Continue' }));

    // Still on the SMTP step: the (now-visible, now-required) host field
    // is empty and should fail validation, unlike the "off" case above.
    expect(await screen.findByText('required')).toBeInTheDocument();
    expect(screen.queryByText('Enrol a second factor now')).not.toBeInTheDocument();
  });
});

describe('SetupWizardPage guard (cheap fix)', () => {
  it('redirects away from /setup when the instance is already set up', async () => {
    mockApi({ 'GET /api/v1/setup/status': { completed: true } });
    renderWizard();

    await screen.findByText('ROOT');
  });
});

describe('the administrator password is typed twice', () => {
  it('refuses to continue when the two do not match', async () => {
    mockApi({ 'GET /api/v1/setup/status': { completed: false } });
    renderWizard();
    await userEvent.type(await screen.findByLabelText('Name'), 'Ada');
    await userEvent.type(screen.getByLabelText('Email address'), 'ada@example.test');
    await userEvent.type(screen.getByLabelText('Password'), 'a-long-enough-password');
    await userEvent.type(screen.getByLabelText('Repeat password'), 'a-long-enough-passwird');
    await userEvent.click(screen.getByRole('button', { name: 'Continue' }));

    // Still on the administrator step: a typo in the one field nobody can
    // read back is the one this wizard cannot let through, because the
    // instance has no other administrator to undo it with.
    expect(await screen.findByText(/passwords do not match/i)).toBeInTheDocument();
    expect(screen.getByLabelText('Repeat password')).toBeInTheDocument();
  });

  it('continues when the two agree', async () => {
    mockApi({ 'GET /api/v1/setup/status': { completed: false } });
    renderWizard();
    await userEvent.type(await screen.findByLabelText('Name'), 'Ada');
    await userEvent.type(screen.getByLabelText('Email address'), 'ada@example.test');
    await userEvent.type(screen.getByLabelText('Password'), 'a-long-enough-password');
    await userEvent.type(screen.getByLabelText('Repeat password'), 'a-long-enough-password');
    await userEvent.click(screen.getByRole('button', { name: 'Continue' }));

    expect(await screen.findByText('Create the first organization')).toBeInTheDocument();
  });
});

describe('the language comes first', () => {
  afterEach(() => vi.restoreAllMocks());

  it('switches the wizard as soon as it is chosen and sends it as the instance default', async () => {
    const user = userEvent.setup();
    let completeBody: unknown;
    mockApi({
      'GET /api/v1/setup/status': { completed: false, version: 'dev', default_locale: 'en' },
      'GET /api/v1/auth/me': () =>
        completeBody
          ? { user: { id: 'u1', email: 'root@acme.io', display_name: 'Root' }, mfa_complete: false, totp_confirmed: false, instance_admin: true }
          : new Response(JSON.stringify({ detail: 'unauthorized' }), { status: 401, headers: { 'Content-Type': 'application/json' } }),
      'POST /api/v1/setup/complete': (req: MockRequest) => {
        completeBody = req.body;
        return {};
      },
    });
    renderWizard({ i18n: true });

    // The first field of the first step.
    const language = await screen.findByLabelText('Language');
    expect(language.compareDocumentPosition(screen.getByLabelText('Name')) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();

    await pickOption(screen, 'Language', 'Deutsch');
    expect(await screen.findByLabelText('Sprache')).toBeInTheDocument();

    await user.type(screen.getByLabelText('Name'), 'Root Admin');
    await user.type(screen.getByLabelText('E-Mail-Adresse'), 'root@acme.io');
    await user.type(screen.getByLabelText('Passwort'), 'correct horse battery staple');
    await user.type(screen.getByLabelText('Passwort wiederholen'), 'correct horse battery stable');
    await user.click(screen.getByRole('button', { name: 'Weiter' }));
    // Validation follows the chosen language too.
    expect(await screen.findByText('Passwörter stimmen nicht überein')).toBeInTheDocument();
    await user.clear(screen.getByLabelText('Passwort wiederholen'));
    await user.type(screen.getByLabelText('Passwort wiederholen'), 'correct horse battery staple');
    await user.click(screen.getByRole('button', { name: 'Weiter' }));

    await user.type(await screen.findByLabelText('Name der Organisation'), 'Acme Hosting');
    await user.type(screen.getByLabelText('URL-Slug'), 'acme');
    await user.click(screen.getByRole('button', { name: 'Weiter' }));
    await screen.findByRole('heading', { name: 'Ausgehende E-Mails' });
    await user.click(screen.getByRole('button', { name: 'Weiter' }));
    await user.click(await screen.findByRole('button', { name: 'Administrator anlegen und fortfahren' }));

    expect(await screen.findByText('ENROLMENT PAGE')).toBeInTheDocument();
    expect(completeBody).toMatchObject({ default_locale: 'de' });
  });

  it('sends the language the wizard is shown in when nobody changes it', async () => {
    // Before setup nothing is stored anywhere, so the wizard starts in the
    // browser's language — here German — and sends exactly that.
    vi.spyOn(navigator, 'language', 'get').mockReturnValue('de-DE');
    const user = userEvent.setup();
    let completeBody: unknown;
    mockApi({
      'GET /api/v1/setup/status': { completed: false },
      'GET /api/v1/auth/me': () =>
        completeBody
          ? { user: { id: 'u1', email: 'root@acme.io', display_name: 'Root' }, mfa_complete: false }
          : new Response(JSON.stringify({ detail: 'unauthorized' }), { status: 401, headers: { 'Content-Type': 'application/json' } }),
      'POST /api/v1/setup/complete': (req: MockRequest) => {
        completeBody = req.body;
        return {};
      },
    });
    renderWizard({ i18n: true });
    await user.type(await screen.findByLabelText('Name'), 'Root Admin');
    await user.type(screen.getByLabelText('E-Mail-Adresse'), 'root@acme.io');
    await user.type(screen.getByLabelText('Passwort'), 'correct horse battery staple');
    await user.type(screen.getByLabelText('Passwort wiederholen'), 'correct horse battery staple');
    await user.click(screen.getByRole('button', { name: 'Weiter' }));
    await user.type(await screen.findByLabelText('Name der Organisation'), 'Acme Hosting');
    await user.type(screen.getByLabelText('URL-Slug'), 'acme');
    await user.click(screen.getByRole('button', { name: 'Weiter' }));
    await screen.findByRole('heading', { name: 'Ausgehende E-Mails' });
    await user.click(screen.getByRole('button', { name: 'Weiter' }));
    await user.click(await screen.findByRole('button', { name: 'Administrator anlegen und fortfahren' }));
    expect(await screen.findByText('ENROLMENT PAGE')).toBeInTheDocument();
    expect(completeBody).toMatchObject({ default_locale: 'de' });
  });
});

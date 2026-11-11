import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../../test/mockApi.ts';
import { pickOption } from '../../../test/pickOption.ts';
import { SYNCED_AT, fields, preview, renderPage, routes, source, status, template, variables } from './pageTestFixtures.tsx';

afterEach(() => vi.restoreAllMocks());

describe('TemplateVariablesPage — the source', () => {
  it("shows the source's commit and sync time", async () => {
    mockApi(routes());
    renderPage();
    expect(await screen.findByRole('heading', { name: 'Variables of deploy-site', level: 1 })).toBeInTheDocument();
    const panel = screen.getByRole('region', { name: 'Playbook source' });
    expect(within(panel).getByText('site.yml')).toBeInTheDocument();
    expect(within(panel).getByText('Commit 9f8e7d6')).toBeInTheDocument();
    expect(within(panel).getByText(`synced ${new Date(SYNCED_AT).toLocaleString('en')}`)).toBeInTheDocument();
    expect(within(panel).queryByText('Newer commit available')).not.toBeInTheDocument();
  });

  it('says when a newer commit is available', async () => {
    mockApi(routes({ 'GET /api/v1/orgs/acme/job-templates/t1/variables': variables({ source: source({ newer: true }) }) }));
    renderPage();
    expect(await screen.findByText('Newer commit available')).toBeInTheDocument();
  });

  it('says an inline playbook was saved since, not that a newer commit waits', async () => {
    const inline = source({ kind: 'inline', commit: 'text:abc', synced_at: undefined, repo_id: undefined, path: undefined, newer: true });
    mockApi(routes({ 'GET /api/v1/orgs/acme/job-templates/t1/variables': variables({ source: inline }) }));
    renderPage();
    expect(await screen.findByText('Playbook saved since the last sync')).toBeInTheDocument();
    expect(screen.queryByText('Newer commit available')).not.toBeInTheDocument();
  });

  it('says so in German too', async () => {
    const inline = source({ kind: 'inline', commit: 'text:abc', synced_at: undefined, repo_id: undefined, path: undefined, newer: true });
    mockApi(routes({ 'GET /api/v1/orgs/acme/job-templates/t1/variables': variables({ source: inline }) }));
    renderPage('de');
    expect(await screen.findByText('Playbook seit dem letzten Abgleich gespeichert')).toBeInTheDocument();
    expect(screen.queryByText('Neuerer Commit verfügbar')).not.toBeInTheDocument();
  });

  it('says when the worker that synced the repository does not read variable sources', async () => {
    mockApi(routes({ 'GET /api/v1/orgs/acme/job-templates/t1/variables': variables({ source: source({ worker_reads: false, available: false }) }) }));
    renderPage();
    expect(await screen.findByText('The worker that synced this repository does not read variable sources.')).toBeInTheDocument();
  });

  it('blames an older worker, not a missing sync, for a playbook its record lacks', async () => {
    mockApi(routes({ 'GET /api/v1/orgs/acme/job-templates/t1/variables': variables({ source: source({ worker_reads: false, found: false, available: false }) }) }));
    renderPage();
    expect(await screen.findByText('The worker that synced this repository does not read variable sources.')).toBeInTheDocument();
    expect(screen.queryByText(/sync its repository/)).not.toBeInTheDocument();
  });

  it('asks for a sync when the repository has no record yet', async () => {
    mockApi(routes({ 'GET /api/v1/orgs/acme/job-templates/t1/variables': variables({ source: source({ worker_reads: false, found: false, available: false, synced_at: undefined, commit: '' }) }) }));
    renderPage();
    expect(await screen.findByText('No variable sources were read for this playbook yet — sync its repository.')).toBeInTheDocument();
    expect(screen.queryByText('The worker that synced this repository does not read variable sources.')).not.toBeInTheDocument();
  });

  it('names an inline playbook and says its roles are not read', async () => {
    mockApi(routes({ 'GET /api/v1/orgs/acme/job-templates/t1/variables': variables({ source: source({ kind: 'inline', commit: 'text:abc', synced_at: undefined, repo_id: undefined, path: undefined }) }) }));
    renderPage();
    expect(await screen.findByText('Inline playbook')).toBeInTheDocument();
    expect(screen.getByText('Roles are not read for an inline playbook.')).toBeInTheDocument();
  });

  it('shows the review badges', async () => {
    mockApi(routes({ 'GET /api/v1/orgs/acme/job-templates/t1/variables': variables({ review: { gone: 1, needs_review: 1, missing_required: 1, left_out: 0, not_sent: [] } }) }));
    renderPage();
    expect(await screen.findByText('1 field gone from the source — review')).toBeInTheDocument();
    expect(screen.getByText('1 change needs review')).toBeInTheDocument();
    expect(screen.getByText('1 required field has no value')).toBeInTheDocument();
  });

  it('names the variables a run leaves out, with a badge', async () => {
    mockApi(routes({ 'GET /api/v1/orgs/acme/job-templates/t1/variables': variables({ review: { gone: 0, needs_review: 0, missing_required: 0, left_out: 0, not_sent: ['token'] } }) }));
    renderPage();
    expect(
      await screen.findByText('Not sent to runs until a value is entered again: token. Each holds a stored secret under a field that is no longer a secret.'),
    ).toBeInTheDocument();
    expect(screen.getByText('1 not sent to runs')).toBeInTheDocument();
  });

  it('shows no not-sent notice while a run leaves nothing out', async () => {
    mockApi(routes({ 'GET /api/v1/orgs/acme/job-templates/t1/variables': variables({ review: { gone: 0, needs_review: 0, missing_required: 0, left_out: 0, not_sent: [] } }) }));
    renderPage();
    expect(await screen.findByRole('heading', { name: 'Variables of deploy-site' })).toBeInTheDocument();
    expect(screen.queryByText(/^Not sent to runs until/)).not.toBeInTheDocument();
    expect(screen.queryByText(/\d+ not sent to runs/)).not.toBeInTheDocument();
  });
});

describe('TemplateVariablesPage — mode', () => {
  it('links an unlinked template through the preview, never through a mode write', async () => {
    const user = userEvent.setup();
    let modePut = false;
    let applied: MockRequest | undefined;
    mockApi(routes({
      'GET /api/v1/orgs/acme/job-templates/t1/variables': variables({ mode: 'off', extra_vars_schema: undefined, extra_vars: {} }),
      'PUT /api/v1/orgs/acme/job-templates/t1/variables/mode': () => {
        modePut = true;
        return variables();
      },
      'POST /api/v1/orgs/acme/job-templates/t1/variables/preview': preview,
      'POST /api/v1/orgs/acme/job-templates/t1/variables/apply': (req: MockRequest) => {
        applied = req;
        return variables({ mode: 'automatic' });
      },
    }));
    renderPage();
    await user.click(await screen.findByRole('radio', { name: 'Automatic' }));
    const dialog = await screen.findByRole('dialog', { name: 'Sync with the playbook' });
    expect(modePut).toBe(false);
    await user.click(within(dialog).getByRole('button', { name: 'Apply' }));
    await waitFor(() => expect(applied).toBeDefined());
    expect(applied?.body).toMatchObject({ mode: 'automatic' });
    expect(modePut).toBe(false);
  });

  it('switches a linked template from manual to automatic with a mode write', async () => {
    const user = userEvent.setup();
    let put: MockRequest | undefined;
    mockApi(routes({
      'PUT /api/v1/orgs/acme/job-templates/t1/variables/mode': (req: MockRequest) => {
        put = req;
        return variables({ mode: 'automatic', revision: 4 });
      },
    }));
    renderPage();
    await user.click(await screen.findByRole('radio', { name: 'Automatic' }));
    await waitFor(() => expect(put).toBeDefined());
    expect(put?.body).toEqual({ mode: 'automatic' });
    expect(put?.headers.get('If-Match')).toBe('"3"');
  });

  it('asks before it unlinks the variables', async () => {
    const user = userEvent.setup();
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    let put = false;
    mockApi(routes({
      'PUT /api/v1/orgs/acme/job-templates/t1/variables/mode': () => {
        put = true;
        return variables({ mode: 'off' });
      },
    }));
    renderPage();
    await user.click(await screen.findByRole('radio', { name: 'Off' }));
    expect(confirm).toHaveBeenCalled();
    expect(put).toBe(false);
  });
});

describe('TemplateVariablesPage — fields and values', () => {
  it('shows the values form for the active fields only', async () => {
    mockApi(routes());
    renderPage();
    const values = await screen.findByRole('region', { name: 'Values' });
    expect(within(values).getByLabelText('region')).toHaveValue('eu');
    expect(within(values).getByLabelText('token')).toHaveAttribute('readonly');
    expect(within(values).queryByLabelText('debug')).not.toBeInTheDocument();
  });

  it('names the values stored under inactive fields and how to discard one', async () => {
    mockApi(routes());
    renderPage();
    expect(await screen.findByText('Stored but not sent: debug')).toBeInTheDocument();
    expect(screen.getByText(/To discard it, switch the field active/)).toBeInTheDocument();
  });

  // Owner decision 4: a removed field keeps its stored value, and adding it
  // back under its name brings the value back.
  describe('values of removed and inactive fields', () => {
    const withRemoved = variables({ extra_vars: { region: 'eu', token: { $secret: 'set' }, debug: true, old_plain: 'kept', old_secret: { $secret: 'set' } } });
    const saved = async (user: ReturnType<typeof userEvent.setup>, put: () => MockRequest | undefined) => {
      await user.click(screen.getByRole('button', { name: 'Save variables' }));
      await waitFor(() => expect(put()).toBeDefined());
      return (put()?.body as { extra_vars: Record<string, unknown> }).extra_vars;
    };
    const addField = async (user: ReturnType<typeof userEvent.setup>, name: string, type?: string) => {
      await user.click(screen.getByRole('button', { name: 'Add field' }));
      const dialog = screen.getByRole('dialog', { name: /^Field field_/ });
      const input = within(dialog).getByLabelText('Name');
      await user.clear(input);
      await user.type(input, name);
      if (type) await pickOption(within(dialog), 'Type', { value: type });
      await user.click(within(dialog).getByRole('button', { name: 'Done' }));
    };

    it('shows and sends the stored value of a removed field that is added back', async () => {
      const user = userEvent.setup();
      let put: MockRequest | undefined;
      mockApi(routes({
        'GET /api/v1/orgs/acme/job-templates/t1/variables': withRemoved,
        'PUT /api/v1/orgs/acme/job-templates/t1/variables': (req: MockRequest) => {
          put = req;
          return variables();
        },
      }));
      renderPage();
      expect(await screen.findByText('Stored but not sent: debug, old_plain, old_secret')).toBeInTheDocument();
      await addField(user, 'old_plain');
      expect(within(screen.getByRole('region', { name: 'Values' })).getByLabelText('old_plain')).toHaveValue('kept');
      expect(await saved(user, () => put)).toEqual({ region: 'eu', token: { $secret: 'keep', from: 'token' }, old_plain: 'kept' });
    });

    it('keeps the stored secret of a removed secret field that is added back', async () => {
      const user = userEvent.setup();
      let put: MockRequest | undefined;
      mockApi(routes({
        'GET /api/v1/orgs/acme/job-templates/t1/variables': withRemoved,
        'PUT /api/v1/orgs/acme/job-templates/t1/variables': (req: MockRequest) => {
          put = req;
          return variables();
        },
      }));
      renderPage();
      await screen.findByRole('region', { name: 'Values' });
      await addField(user, 'old_secret', 'secret');
      expect(within(screen.getByRole('region', { name: 'Values' })).getByLabelText('old_secret')).toHaveAttribute('readonly');
      expect((await saved(user, () => put)).old_secret).toEqual({ $secret: 'keep', from: 'old_secret' });
    });

    it('says a removed secret added back under another type cannot be shown, and does not send its marker', async () => {
      const user = userEvent.setup();
      let put: MockRequest | undefined;
      mockApi(routes({
        'GET /api/v1/orgs/acme/job-templates/t1/variables': withRemoved,
        'PUT /api/v1/orgs/acme/job-templates/t1/variables': (req: MockRequest) => {
          put = req;
          return variables();
        },
      }));
      renderPage();
      const values = await screen.findByRole('region', { name: 'Values' });
      await addField(user, 'old_secret');
      expect(within(values).getByLabelText('old_secret')).toHaveValue('');
      expect(within(values).getByText(/The stored value was a secret and cannot be shown under this type/)).toBeInTheDocument();
      expect(await saved(user, () => put)).not.toHaveProperty('old_secret');
    });

    it('sends the stored value of an inactive field switched active', async () => {
      const user = userEvent.setup();
      let put: MockRequest | undefined;
      mockApi(routes({
        'PUT /api/v1/orgs/acme/job-templates/t1/variables': (req: MockRequest) => {
          put = req;
          return variables();
        },
      }));
      renderPage();
      await user.click(await screen.findByRole('switch', { name: 'debug active' }));
      expect(within(screen.getByRole('region', { name: 'Values' })).getByLabelText('debug')).toBeChecked();
      expect((await saved(user, () => put)).debug).toBe(true);
    });
  });

  it('saves the schema unchanged, the active values, and the secret as a keep marker, with If-Match', async () => {
    const user = userEvent.setup();
    let put: MockRequest | undefined;
    mockApi(routes({
      'PUT /api/v1/orgs/acme/job-templates/t1/variables': (req: MockRequest) => {
        put = req;
        return variables({ revision: 4 });
      },
    }));
    renderPage();
    await user.type(await screen.findByLabelText('region'), '-west');
    await user.click(screen.getByRole('button', { name: 'Save variables' }));
    await waitFor(() => expect(put).toBeDefined());
    expect(put?.headers.get('If-Match')).toBe('"3"');
    expect(put?.body).toEqual({
      extra_vars_schema: { fields },
      extra_vars: { region: 'eu-west', token: { $secret: 'keep', from: 'token' } },
    });
    expect(await screen.findByText('Variables saved')).toBeInTheDocument();
  });

  it('offers no second save while the saved variables are read again', async () => {
    const user = userEvent.setup();
    let reads = 0;
    mockApi(routes({
      // The read after the save never answers: the page stays on the saved draft.
      'GET /api/v1/orgs/acme/job-templates/t1/variables': () => (++reads === 1 ? variables() : new Promise(() => {})),
      'PUT /api/v1/orgs/acme/job-templates/t1/variables': () => variables({ revision: 4 }),
    }));
    renderPage();
    await user.type(await screen.findByLabelText('region'), '-west');
    await user.click(screen.getByRole('button', { name: 'Save variables' }));
    expect(await screen.findByText('Variables saved')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Save variables' })).toBeDisabled();
  });

  it('sends a replaced secret as plaintext and shows a 422 at its field', async () => {
    const user = userEvent.setup();
    let put: MockRequest | undefined;
    mockApi(routes({
      'PUT /api/v1/orgs/acme/job-templates/t1/variables': (req: MockRequest) => {
        put = req;
        return status(422, { status: 422, title: 'validation failed', errors: [{ location: 'body.extra_vars.token', message: 'too short' }] });
      },
    }));
    renderPage();
    const values = await screen.findByRole('region', { name: 'Values' });
    await user.click(within(values).getByRole('button', { name: 'Replace' }));
    await user.type(within(values).getByLabelText('token'), 'x');
    await user.click(screen.getByRole('button', { name: 'Save variables' }));
    expect(await within(values).findByText('too short')).toBeInTheDocument();
    expect((put?.body as { extra_vars: unknown }).extra_vars).toEqual({ region: 'eu', token: 'x' });
  });

  it('builds a field and fills it before the template round-trips', async () => {
    const user = userEvent.setup();
    let put: MockRequest | undefined;
    mockApi(routes({
      'GET /api/v1/orgs/acme/job-templates/t1/variables': variables({ mode: 'off', extra_vars_schema: undefined, extra_vars: {} }),
      'PUT /api/v1/orgs/acme/job-templates/t1/variables': (req: MockRequest) => {
        put = req;
        return variables();
      },
    }));
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Add field' }));
    const fieldDialog = screen.getByRole('dialog', { name: 'Field field_1' });
    const name = within(fieldDialog).getByLabelText('Name');
    await user.clear(name);
    await user.type(name, 'site');
    await user.click(within(fieldDialog).getByRole('button', { name: 'Done' }));
    await user.type(within(screen.getByRole('region', { name: 'Values' })).getByLabelText('site'), 'shop');
    await user.click(screen.getByRole('button', { name: 'Save variables' }));
    await waitFor(() => expect(put).toBeDefined());
    expect(put?.body).toEqual({ extra_vars_schema: { fields: [{ name: 'site', type: 'string' }] }, extra_vars: { site: 'shop' } });
  });

  it('offers a reload when the template changed since it was loaded (412)', async () => {
    const user = userEvent.setup();
    mockApi(routes({ 'PUT /api/v1/orgs/acme/job-templates/t1/variables': () => status(412) }));
    renderPage();
    await user.type(await screen.findByLabelText('region'), '-west');
    await user.click(screen.getByRole('button', { name: 'Save variables' }));
    const text = await screen.findByText(/^The template changed since this page was loaded: someone saved it/);
    const notice = text.closest('[role="alert"]') as HTMLElement;
    expect(within(notice).getByRole('button', { name: 'Reload' })).toBeInTheDocument();
  });

  it('asks before leaving with unsaved changes', async () => {
    const user = userEvent.setup();
    mockApi(routes());
    const router = renderPage();
    await user.type(await screen.findByLabelText('region'), '-west');
    await user.click(screen.getByRole('link', { name: 'Job templates' }));
    const dialog = await screen.findByRole('dialog', { name: 'Are you sure?' });
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    expect(router.state.location.pathname).toBe('/o/acme/templates/t1/variables');
  });

  it('leaves without asking when nothing changed', async () => {
    const user = userEvent.setup();
    const confirm = vi.spyOn(window, 'confirm');
    mockApi(routes());
    renderPage();
    await user.click(await screen.findByRole('link', { name: 'Job templates' }));
    expect(await screen.findByText('TEMPLATE LIST')).toBeInTheDocument();
    expect(confirm).not.toHaveBeenCalled();
  });

  it("shows a projection's variables read-only, without a sync", async () => {
    mockApi(routes({
      'GET /api/v1/orgs/acme/job-templates/t1': { template: { ...template, shared_template_id: 'def-1' } },
      'GET /api/v1/orgs/acme/job-templates/t1/variables': variables({ mode: 'off' }),
    }));
    renderPage();
    expect(await screen.findByText("This template belongs to the installation's definition: its variables are read-only here.")).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Save variables' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Add field' })).not.toBeInTheDocument();
    expect(screen.getByRole('radio', { name: 'Manual' })).toBeDisabled();
  });
});

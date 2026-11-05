import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest, type MockRoute } from '../../../test/mockApi.ts';
import { SYNCED_AT, preview, renderPage, routes, status, variables } from './pageTestFixtures.tsx';

describe('TemplateVariablesPage — sync now', () => {
  async function openPreview(extra: Record<string, MockRoute> = {}) {
    const user = userEvent.setup();
    mockApi(routes({ 'POST /api/v1/orgs/acme/job-templates/t1/variables/preview': preview, ...extra }));
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Sync now' }));
    return { user, dialog: await screen.findByRole('dialog', { name: 'Sync with the playbook' }) };
  }

  it('shows what was added, changed and is gone', async () => {
    const { dialog } = await openPreview();
    const added = within(dialog).getByRole('region', { name: 'Added' });
    expect(within(added).getByText('port')).toBeInTheDocument();
    expect(within(added).getByText('inactive')).toBeInTheDocument();
    const changed = within(dialog).getByRole('region', { name: 'Changed' });
    expect(within(changed).getByText('region')).toBeInTheDocument();
    expect(within(changed).getByText('— → Region')).toBeInTheDocument();
    const gone = within(dialog).getByRole('region', { name: 'Gone from the source' });
    expect(within(gone).getByText('legacy')).toBeInTheDocument();
    expect(within(dialog).getByText('2 unchanged')).toBeInTheDocument();
  });

  it("applies with the preview's revision and sync time, verbatim, and If-Match", async () => {
    let applied: MockRequest | undefined;
    const { user, dialog } = await openPreview({
      'POST /api/v1/orgs/acme/job-templates/t1/variables/apply': (req: MockRequest) => {
        applied = req;
        return variables({ revision: 4 });
      },
    });
    expect(within(dialog).getByLabelText('Also remove gone fields')).not.toBeChecked();
    await user.click(within(dialog).getByRole('button', { name: 'Apply' }));
    await waitFor(() => expect(applied).toBeDefined());
    expect(applied?.body).toEqual({ mode: 'manual', source_rev: 'aaaaaaa1111111', source_synced_at: SYNCED_AT, remove_gone: false });
    expect(applied?.headers.get('If-Match')).toBe('"3"');
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Sync with the playbook' })).not.toBeInTheDocument());
  });

  it('removes the gone fields only when asked to', async () => {
    let applied: MockRequest | undefined;
    const { user, dialog } = await openPreview({
      'POST /api/v1/orgs/acme/job-templates/t1/variables/apply': (req: MockRequest) => {
        applied = req;
        return variables();
      },
    });
    await user.click(within(dialog).getByLabelText('Also remove gone fields'));
    await user.click(within(dialog).getByRole('button', { name: 'Apply' }));
    await waitFor(() => expect(applied).toBeDefined());
    expect(applied?.body).toMatchObject({ remove_gone: true });
  });

  it('asks to preview again when the source changed since (409)', async () => {
    const { user, dialog } = await openPreview({
      'POST /api/v1/orgs/acme/job-templates/t1/variables/apply': () => status(409),
    });
    await user.click(within(dialog).getByRole('button', { name: 'Apply' }));
    expect(await within(dialog).findByText('The source changed — preview again')).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: 'Preview again' })).toBeInTheDocument();
  });

  it('offers a reload when the template changed since (412)', async () => {
    const { user, dialog } = await openPreview({
      'POST /api/v1/orgs/acme/job-templates/t1/variables/apply': () => status(412),
    });
    await user.click(within(dialog).getByRole('button', { name: 'Apply' }));
    expect(await within(dialog).findByText('The template changed since this page was loaded. Reload it, then sync again.')).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: 'Reload' })).toBeInTheDocument();
  });

  it('asks for a value that no longer fits, and sends it with the apply', async () => {
    let applied: MockRequest | undefined;
    const held = {
      ...preview,
      preview: {
        ...preview.preview,
        changed: [{
          name: 'port', current_origin: 'source', needs_review: true, reason: 'value',
          field: { name: 'port', type: 'enum', options: ['80', '443'] },
          changes: [{ attr: 'options', old: ['80', '8080'], new: ['80', '443'] }],
        }],
      },
    };
    const { user, dialog } = await openPreview({
      'POST /api/v1/orgs/acme/job-templates/t1/variables/preview': held,
      'POST /api/v1/orgs/acme/job-templates/t1/variables/apply': (req: MockRequest) => {
        applied = req;
        return variables();
      },
    });
    const changed = within(dialog).getByRole('region', { name: 'Changed' });
    expect(within(changed).getByText('Needs review')).toBeInTheDocument();
    expect(within(changed).getByText('The stored value no longer fits: enter it again.')).toBeInTheDocument();
    await user.click(within(changed).getByLabelText('port'));
    await user.click(screen.getByRole('option', { name: '443' }));
    await user.click(within(dialog).getByRole('button', { name: 'Apply' }));
    await waitFor(() => expect(applied).toBeDefined());
    expect(applied?.body).toMatchObject({ values: { port: '443' } });
  });

  it('asks for the value of an added required field without a default, and shows a refused value', async () => {
    const applied: MockRequest[] = [];
    const { user, dialog } = await openPreview({
      'POST /api/v1/orgs/acme/job-templates/t1/variables/preview': {
        ...preview,
        preview: { ...preview.preview, added: [{ field: { name: 'admin_user', label: 'Admin user', type: 'string', required: true, origin: 'source' } }] },
      },
      'POST /api/v1/orgs/acme/job-templates/t1/variables/apply': (req: MockRequest) => {
        applied.push(req);
        return status(422, { status: 422, title: 'validation failed', errors: [{ location: 'body.values.admin_user', message: 'is required' }, { location: 'body.values.other', message: 'unknown field' }] });
      },
    });
    const added = within(dialog).getByRole('region', { name: 'Added' });
    expect(within(added).getByText('active')).toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: 'Apply' }));
    expect(await within(added).findByText('is required')).toBeInTheDocument();
    expect(within(dialog).getByText('other: unknown field')).toBeInTheDocument();
    await user.type(within(added).getByLabelText('Admin user'), 'root');
    await user.click(within(dialog).getByRole('button', { name: 'Apply' }));
    await waitFor(() => expect(applied).toHaveLength(2));
    expect(applied[1].body).toMatchObject({ values: { admin_user: 'root' } });
  });

  it('says how many source fields were left out', async () => {
    const { dialog } = await openPreview({
      'POST /api/v1/orgs/acme/job-templates/t1/variables/preview': {
        ...preview,
        preview: { ...preview.preview, left_out: 4, notes: [{ level: 'warning', code: 'fields_left_out', message: 'x', file: 'site.yml' }] },
      },
    });
    expect(within(dialog).getByText(/4 source fields were left out/)).toBeInTheDocument();
  });

  it('names the review reason of a change a limit may have caused', async () => {
    const { dialog } = await openPreview({
      'POST /api/v1/orgs/acme/job-templates/t1/variables/preview': {
        ...preview,
        preview: { ...preview.preview, changed: [{ ...preview.preview.changed[0], needs_review: true, reason: 'cut' }] },
      },
    });
    expect(within(dialog).getByText('A limit cut fields from the source, so this change may come from the cut.')).toBeInTheDocument();
  });

  it('offers no sync while the page holds unsaved changes', async () => {
    const user = userEvent.setup();
    mockApi(routes());
    renderPage();
    await user.type(await screen.findByLabelText('region'), '-west');
    expect(screen.getByRole('button', { name: 'Sync now' })).toBeDisabled();
    expect(screen.getByText('Save or discard your changes before you sync with the playbook.')).toBeInTheDocument();
  });
});

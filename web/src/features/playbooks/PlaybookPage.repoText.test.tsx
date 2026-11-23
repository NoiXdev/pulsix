import { describe, expect, it } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { codeOf, findCodeEditor } from '../../test/codeEditor.ts';
import { OUTLINE, renderAt, routes } from './playbookPageTestKit.tsx';

/* The Editor tab of a repository playbook (backlog 3.127): its text as of
 * the last sync, read-only, with Check, Outline and Variables; or the note
 * where it lives and why the sync stored no text. */

const TEXT = '- name: Site\n  hosts: all\n  tasks:\n    - ansible.builtin.debug:\n        msg: "{{ domain }}"\n';
const SYNCED = '2026-11-20T10:00:00Z';
const content = (extra = {}) => ({ path: 'playbooks/site.yml', commit: 'abcdef1234567890', synced_at: SYNCED, content: TEXT, ...extra });
const leftOut = (reason: string) => ({ path: 'playbooks/site.yml', commit: 'abcdef1234567890', synced_at: SYNCED, left_out: reason });
const discovered = (extra = {}) => ({
  playbooks: [{ path: 'playbooks/site.yml', playbook_id: 'p2', fields: 3, failed: false, left_out: false, warnings: 0, ...extra }],
  sources: { commit: 'abcdef1234567890', synced_at: SYNCED, incomplete: false, left_out: [], notes: [], worker_reads: true },
});
const problem = (status: number, detail: string) =>
  new Response(JSON.stringify({ status, detail }), { status, headers: { 'Content-Type': 'application/json' } });

const repoRoutes = (body: unknown, extra = {}) =>
  routes('operator', {
    'GET /api/v1/orgs/acme/playbooks/p2/content': body,
    'GET /api/v1/orgs/acme/repos/r1/playbooks': discovered(),
    ...extra,
  });

const note = () => screen.findByRole('note', { name: 'Edited in its repository' });

describe('PlaybookPage repository text', () => {
  it('shows the text of the last sync read-only, with the sync line and no Save', async () => {
    mockApi(repoRoutes(content()));
    renderAt('/o/acme/playbooks/p2');
    const editor = await findCodeEditor('Playbook YAML');
    expect(codeOf(editor)).toBe(TEXT);
    expect(editor).toHaveAttribute('aria-readonly', 'true');
    expect(editor).toHaveAttribute('contenteditable', 'false');
    fireEvent.keyDown(editor, { key: 'Tab', keyCode: 9 });
    expect(codeOf(editor)).toBe(TEXT);
    const when = new Date(SYNCED).toLocaleString('en');
    const line = screen.getByText(`As of the sync on ${when}, commit abcdef1`);
    expect(within(line.parentElement!).getByRole('link', { name: 'infra @ main' })).toHaveAttribute('href', '/o/acme/repos?q=infra');
    expect(screen.getByText('Read-only: changes come through Git, and the next sync brings them in.')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument();
    expect(screen.queryByRole('note', { name: 'Edited in its repository' })).not.toBeInTheDocument();
  });

  it('says the text is from an earlier sync when the last one left it out', async () => {
    mockApi(repoRoutes(content({ commit: '' })));
    renderAt('/o/acme/playbooks/p2');
    await findCodeEditor('Playbook YAML');
    const when = new Date(SYNCED).toLocaleString('en');
    expect(screen.getByText(`From an earlier sync: the last one, on ${when}, left this playbook unread`)).toBeInTheDocument();
    expect(screen.queryByText(/commit/)).not.toBeInTheDocument();
  });

  it('checks and outlines the text, and lists its variables with the stored sources', async () => {
    const outlined: unknown[] = [];
    mockApi(repoRoutes(content(), { 'POST /api/v1/orgs/acme/playbooks/outline': (req: MockRequest) => (outlined.push(req.body), OUTLINE) }));
    renderAt('/o/acme/playbooks/p2');
    const check = await screen.findByRole('region', { name: 'Check' });
    expect(within(check).getByText('Parses as a list of plays.')).toBeInTheDocument();
    expect(within(check).getByText('Checked in the browser, on the text of the last sync.')).toBeInTheDocument();
    const outline = screen.getByRole('region', { name: 'Outline' });
    expect(await within(outline).findByRole('button', { name: 'Line 5: Install package' })).toBeInTheDocument();
    expect(outlined).toEqual([{ content: TEXT }]);
    const variables = screen.getByRole('region', { name: 'Variables' });
    expect(await within(variables).findByText('Its variable sources at the last sync: 3 variables, which a template using it offers as fields.')).toBeInTheDocument();
    expect(within(variables).getByText('domain')).toBeInTheDocument();
  });

  it('says what the stored sources lack', async () => {
    mockApi(repoRoutes(content(), { 'GET /api/v1/orgs/acme/repos/r1/playbooks': discovered({ failed: true }) }));
    const view = renderAt('/o/acme/playbooks/p2');
    const variables = await screen.findByRole('region', { name: 'Variables' });
    expect(await within(variables).findByText('The last sync could not read its variable sources.')).toBeInTheDocument();
    view.unmount();

    mockApi(repoRoutes(content(), { 'GET /api/v1/orgs/acme/repos/r1/playbooks': discovered({ left_out: true, fields: 1 }) }));
    const again = renderAt('/o/acme/playbooks/p2');
    expect(
      await within(await screen.findByRole('region', { name: 'Variables' })).findByText('A limit left its variable sources unread at the last sync; the read before found 1 variable.'),
    ).toBeInTheDocument();
    again.unmount();

    mockApi(repoRoutes(content(), { 'GET /api/v1/orgs/acme/repos/r1/playbooks': { playbooks: [], sources: discovered().sources } }));
    renderAt('/o/acme/playbooks/p2');
    expect(
      await within(await screen.findByRole('region', { name: 'Variables' })).findByText('The last sync recorded no variable sources for this playbook.'),
    ).toBeInTheDocument();
  });

  it.each([
    ['too_large', 'The last sync stored no text for this playbook: the file is over 512 KiB.'],
    ['budget', "The last sync stored no text for this playbook: the sync's texts were full (4 MiB in all)."],
    ['not_utf8', 'The last sync stored no text for this playbook: the file is not UTF-8 text.'],
    ['unreadable', 'The last sync stored no text for this playbook: the file could not be read.'],
    [
      'not_sent',
      'The last sync stored no text for this playbook: the file may be gone from the repository, or the sync ran on an older worker. Sync again once workers are updated.',
    ],
  ])('keeps the note and says why there is no text (%s)', async (reason, sentence) => {
    const outlined: unknown[] = [];
    mockApi(repoRoutes(leftOut(reason), { 'POST /api/v1/orgs/acme/playbooks/outline': () => (outlined.push('outline'), OUTLINE) }));
    renderAt('/o/acme/playbooks/p2');
    const n = await note();
    expect(await within(n).findByText(sentence)).toBeInTheDocument();
    await waitFor(() => expect(n).toHaveTextContent('playbooks/site.yml lives in infra @ main. Changes come through Git'));
    expect(screen.queryByRole('textbox', { name: 'Playbook YAML' })).not.toBeInTheDocument();
    expect(within(screen.getByRole('region', { name: 'Outline' })).getByText('No outline: the last sync stored no text for this playbook.')).toBeInTheDocument();
    expect(outlined).toEqual([]);
  });

  it('says a repository never synced has no text yet', async () => {
    mockApi(repoRoutes(problem(404, 'the repository has not been synced yet')));
    renderAt('/o/acme/playbooks/p2');
    expect(await within(await note()).findByText('The repository has not been synced yet: sync it to see the text here.')).toBeInTheDocument();
  });

  it('says when the text could not be read', async () => {
    mockApi(repoRoutes(problem(500, 'boom')));
    renderAt('/o/acme/playbooks/p2');
    expect(await within(await note()).findByText('The text could not be read: boom')).toBeInTheDocument();
  });

  it('speaks German about a sync from an older worker', async () => {
    mockApi(repoRoutes(leftOut('not_sent')));
    renderAt('/o/acme/playbooks/p2', 'de');
    const n = await screen.findByRole('note', { name: 'Wird im Repository bearbeitet' });
    expect(await within(n).findByText(/Synchronisiere erneut, sobald die Worker aktualisiert sind\./)).toBeInTheDocument();
  });

  it('shows a viewer the same read-only text', async () => {
    mockApi({ ...repoRoutes(content()), 'GET /api/v1/orgs/acme': { org: { id: 'o1', name: 'Acme', slug: 'acme', role: 'viewer', created_at: '' } } });
    renderAt('/o/acme/playbooks/p2');
    await waitFor(async () => expect(codeOf(await findCodeEditor('Playbook YAML'))).toBe(TEXT));
  });
});

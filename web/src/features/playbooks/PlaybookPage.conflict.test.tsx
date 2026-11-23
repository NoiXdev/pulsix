import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { codeOf, findCodeEditor, replaceCode } from '../../test/codeEditor.ts';
import { INLINE, editorPlaybook, renderAt, routes } from './playbookPageTestKit.tsx';

/* Saving an editor playbook over someone else's save (backlog 3.127): the
 * save sends If-Match with the revision the draft's base came from, a 412
 * keeps the draft and shows a notice with their changes, Reload and Save
 * mine anyway. */

const THEIRS = '- name: Install nginx\n  hosts: everywhere\n  tasks: []\n';
const MINE = '- hosts: mine\n';
const NOTICE = 'Someone saved this playbook since you started editing.';

const stale = () =>
  new Response(JSON.stringify({ status: 412, detail: 'the playbook changed since it was read' }), { status: 412, headers: { 'Content-Type': 'application/json' } });

/** A server holding one playbook at a revision: GET answers it, PUT saves
 * when If-Match is absent or current and answers 412 when stale. `others`
 * saves someone else's text behind the page's back. */
function server(start = editorPlaybook({ revision: 4 })) {
  let current = start;
  const puts: { ifMatch: string | null; body: unknown }[] = [];
  const api = routes('operator', {
    'GET /api/v1/orgs/acme/playbooks/p1': () => ({ playbook: current }),
    'PUT /api/v1/orgs/acme/playbooks/p1': (req: MockRequest) => {
      const ifMatch = req.headers.get('If-Match');
      puts.push({ ifMatch, body: req.body });
      if (ifMatch !== null && ifMatch !== `"${current.revision}"`) return stale();
      current = editorPlaybook({ inline: (req.body as { inline: string }).inline, revision: current.revision + 1 });
      return { playbook: current };
    },
  });
  return {
    api,
    puts,
    others: (inline: string) => {
      current = editorPlaybook({ inline, revision: current.revision + 1 });
    },
  };
}

async function editAndHitConflict() {
  const user = userEvent.setup();
  const s = server();
  mockApi(s.api);
  const view = renderAt('/o/acme/playbooks/p1');
  const editor = await findCodeEditor('Playbook YAML');
  replaceCode(editor, MINE);
  s.others(THEIRS);
  await user.click(screen.getByRole('button', { name: 'Save' }));
  const notice = await screen.findByRole('region', { name: NOTICE });
  return { user, s, view, editor, notice };
}

describe('PlaybookPage save conflicts', () => {
  it('sends If-Match with the revision the draft started from', async () => {
    const user = userEvent.setup();
    const s = server();
    mockApi(s.api);
    renderAt('/o/acme/playbooks/p1');
    replaceCode(await findCodeEditor('Playbook YAML'), MINE);
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByText('Playbook saved')).toBeInTheDocument();
    expect(s.puts).toEqual([{ ifMatch: '"4"', body: { inline: MINE } }]);
    expect(screen.queryByRole('region', { name: NOTICE })).not.toBeInTheDocument();
  });

  it('sends no If-Match to a manager that keeps no revision', async () => {
    const user = userEvent.setup();
    const s = server(editorPlaybook({ revision: undefined }));
    mockApi(s.api);
    renderAt('/o/acme/playbooks/p1');
    replaceCode(await findCodeEditor('Playbook YAML'), MINE);
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(s.puts).toEqual([{ ifMatch: null, body: { inline: MINE } }]));
  });

  it('keeps the draft on a 412 and shows the notice instead of an error', async () => {
    const { editor, notice } = await editAndHitConflict();
    expect(codeOf(editor)).toBe(MINE);
    expect(screen.getByText('Unsaved')).toBeInTheDocument();
    expect(within(notice).getByText(/Your changes are still here, not saved\./)).toBeInTheDocument();
    expect(within(screen.getByRole('region', { name: 'Check' })).queryByText('The server refused the last save:')).not.toBeInTheDocument();
    expect(screen.queryByText('Playbook saved')).not.toBeInTheDocument();
  });

  it('shows their changes as a diff of the text the draft started from against the current one', async () => {
    const { user, notice } = await editAndHitConflict();
    await user.click(within(notice).getByRole('button', { name: 'Show their changes' }));
    const diff = await within(notice).findByRole('figure', { name: 'Differences' });
    const rows = within(diff)
      .getAllByRole('listitem')
      .map((r) => [r.dataset.kind, r.textContent]);
    expect(rows).toContainEqual(['header', '--- when you started editing']);
    expect(rows).toContainEqual(['header', '+++ saved since']);
    expect(rows).toContainEqual(['delete', '-  hosts: web']);
    expect(rows).toContainEqual(['insert', '+  hosts: everywhere']);
    expect(rows).not.toContainEqual(['insert', `+${MINE.trim()}`]);
    await user.click(within(notice).getByRole('button', { name: 'Hide their changes' }));
    expect(within(notice).queryByRole('figure', { name: 'Differences' })).not.toBeInTheDocument();
  });

  it('reloads: drops the draft for the current text, and saves on its revision next', async () => {
    const { user, s, editor, notice } = await editAndHitConflict();
    await user.click(within(notice).getByRole('button', { name: 'Reload' }));
    await waitFor(() => expect(codeOf(editor)).toBe(THEIRS));
    expect(screen.queryByRole('region', { name: NOTICE })).not.toBeInTheDocument();
    expect(screen.queryByText('Unsaved')).not.toBeInTheDocument();
    replaceCode(editor, '- hosts: after\n');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByText('Playbook saved')).toBeInTheDocument();
    expect(s.puts.at(-1)).toEqual({ ifMatch: '"5"', body: { inline: '- hosts: after\n' } });
  });

  it('saves mine anyway on the current revision', async () => {
    const { user, s, editor, notice } = await editAndHitConflict();
    await user.click(within(notice).getByRole('button', { name: 'Save mine anyway' }));
    expect(await screen.findByText('Playbook saved')).toBeInTheDocument();
    expect(s.puts.at(-1)).toEqual({ ifMatch: '"5"', body: { inline: MINE } });
    expect(screen.queryByRole('region', { name: NOTICE })).not.toBeInTheDocument();
    await waitFor(() => expect(screen.queryByText('Unsaved')).not.toBeInTheDocument());
    expect(codeOf(editor)).toBe(MINE);
  });

  it('shows the notice again when the current revision went stale too', async () => {
    const { user, s, editor, notice } = await editAndHitConflict();
    s.others('- hosts: third\n');
    await user.click(within(notice).getByRole('button', { name: 'Save mine anyway' }));
    await waitFor(() => expect(s.puts.at(-1)).toEqual({ ifMatch: '"5"', body: { inline: MINE } }));
    const again = await screen.findByRole('region', { name: NOTICE });
    expect(codeOf(editor)).toBe(MINE);
    await user.click(within(again).getByRole('button', { name: 'Save mine anyway' }));
    expect(await screen.findByText('Playbook saved')).toBeInTheDocument();
    expect(s.puts.at(-1)).toEqual({ ifMatch: '"6"', body: { inline: MINE } });
  });

  it('takes a newer text and its revision into a clean draft, and saves on that revision', async () => {
    const s = server();
    mockApi(s.api);
    const { qc } = renderAt('/o/acme/playbooks/p1');
    const editor = await findCodeEditor('Playbook YAML');
    expect(codeOf(editor)).toBe(INLINE);
    s.others(THEIRS);
    await qc.invalidateQueries({ queryKey: ['playbooks', 'acme'] });
    await waitFor(() => expect(codeOf(editor)).toBe(THEIRS));
    const user = userEvent.setup();
    replaceCode(editor, MINE);
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByText('Playbook saved')).toBeInTheDocument();
    expect(s.puts).toEqual([{ ifMatch: '"5"', body: { inline: MINE } }]);
  });
});

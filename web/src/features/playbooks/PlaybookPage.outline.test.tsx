import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi } from '../../test/mockApi.ts';
import { cursorLineOf, findCodeEditor } from '../../test/codeEditor.ts';
import { editorPlaybook, OUTLINE, renderAt, routes } from './playbookPageTestKit.tsx';

const LONG = Array.from({ length: 20 }, (_, i) => (i === 0 ? '- name: Install nginx' : `  # line ${i + 1}`)).join('\n') + '\n';

const outlinePanel = () => screen.findByRole('region', { name: 'Outline' });

describe('PlaybookPage outline and header', () => {
  it('lists the plays and entries of the outline in the side column', async () => {
    mockApi(routes());
    renderAt('/o/acme/playbooks/p1');
    const panel = await outlinePanel();
    expect(await within(panel).findByText('Install nginx')).toBeInTheDocument();
    expect(within(panel).getByText('web · become')).toBeInTheDocument();
    expect(within(panel).getByRole('button', { name: 'Line 5: Install package' })).toHaveTextContent('packages');
    expect(within(panel).getByRole('button', { name: 'Line 18: reload nginx' })).toBeInTheDocument();
  });

  it('marks the entries that run for real in a dry run', async () => {
    mockApi(routes());
    renderAt('/o/acme/playbooks/p1');
    const panel = await outlinePanel();
    expect(await within(panel).findByRole('button', { name: 'Line 13: Check config (runs for real in a dry run)' })).toBeInTheDocument();
    expect(within(panel).getByRole('button', { name: 'Line 9: Render nginx config' })).toBeInTheDocument();
  });

  it('moves the editor cursor to the line of a clicked entry and focuses it', async () => {
    const user = userEvent.setup();
    mockApi(routes('operator', { 'GET /api/v1/orgs/acme/playbooks/p1': { playbook: editorPlaybook({ inline: LONG }) } }));
    renderAt('/o/acme/playbooks/p1');
    const editor = await findCodeEditor('Playbook YAML');
    await user.click(await within(await outlinePanel()).findByRole('button', { name: 'Line 9: Render nginx config' }));
    expect(cursorLineOf(editor)).toBe(9);
    await waitFor(() => expect(editor).toHaveFocus());
  });

  it('says when the outline is shortened', async () => {
    mockApi(routes('operator', { 'POST /api/v1/orgs/acme/playbooks/outline': { ...OUTLINE, truncated: true } }));
    renderAt('/o/acme/playbooks/p1');
    expect(await within(await outlinePanel()).findByText(/Outline shortened/)).toBeInTheDocument();
  });

  it('has no outline for a repository playbook never synced, and says so', async () => {
    const asked: string[] = [];
    mockApi(routes('operator', { 'POST /api/v1/orgs/acme/playbooks/outline': () => (asked.push('outline'), OUTLINE) }));
    renderAt('/o/acme/playbooks/p2');
    expect(await within(await outlinePanel()).findByText('No outline yet: the repository has not been synced.')).toBeInTheDocument();
    expect(within(screen.getByTestId('playbook-header')).queryByRole('list', { name: 'Tags' })).not.toBeInTheDocument();
    expect(asked).toEqual([]);
  });

  it('shows the counts and the tags of the outline in the header', async () => {
    mockApi(routes());
    renderAt('/o/acme/playbooks/p1');
    const header = await screen.findByTestId('playbook-header');
    expect(await within(header).findByText(/1 play · 3 tasks · 1 handler/)).toBeInTheDocument();
    const tags = within(header).getByRole('list', { name: 'Tags' });
    expect(within(tags).getAllByRole('listitem').map((li) => li.textContent)).toEqual(['config', 'packages']);
  });

  it('opens the Dry run tab from the header', async () => {
    const user = userEvent.setup();
    mockApi(routes());
    const { router } = renderAt('/o/acme/playbooks/p1');
    await user.click(await screen.findByRole('button', { name: 'Dry run…' }));
    await waitFor(() => expect(router.state.location.search).toEqual({ tab: 'dry-run' }));
    expect(screen.getByRole('tab', { name: 'Dry run' })).toHaveAttribute('aria-selected', 'true');
  });

  it('offers no Dry run… to a reader', async () => {
    mockApi(routes('viewer'));
    renderAt('/o/acme/playbooks/p1');
    await screen.findByTestId('playbook-header');
    await within(await outlinePanel()).findByText('Install nginx');
    expect(screen.queryByRole('button', { name: 'Dry run…' })).not.toBeInTheDocument();
  });

  it('speaks German', async () => {
    mockApi(routes());
    renderAt('/o/acme/playbooks/p1', 'de');
    const panel = await screen.findByRole('region', { name: 'Gliederung' });
    expect(await within(panel).findByRole('button', { name: 'Zeile 13: Check config (läuft im Probelauf wirklich)' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Probelauf…' })).toBeInTheDocument();
    expect(within(screen.getByTestId('playbook-header')).getByText(/1 Play · 3 Tasks · 1 Handler/)).toBeInTheDocument();
  });
});

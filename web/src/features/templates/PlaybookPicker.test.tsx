import { useState } from 'react';
import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { renderRoutes } from '../../test/renderRoutes.tsx';
import { PlaybookPicker, type PlaybookChoice } from './PlaybookPicker.tsx';
import { TemplateDialog } from './TemplateDialog.tsx';
import type { Playbook } from '../../api/types.ts';

const repo = { id: 'r1', name: 'infra', url: 'https://git.example/infra.git', branch: 'main', subpath: '', created_at: '', updated_at: '' };
const editorPlaybook: Playbook = { id: 'pb1', source: 'editor', summary: 'Install nginx', created_at: '', updated_at: '' };
const registered: Playbook = { id: 'pb7', source: 'repo', repo_id: 'r1', path: 'site.yml', summary: '', created_at: '', updated_at: '' };
const discovered = (over: Record<string, unknown> = {}) => ({ path: 'deploy.yml', fields: 3, warnings: 0, failed: false, left_out: false, ...over });
const sources = { commit: '9f8e7d6c5b4a', synced_at: '2026-11-05T10:00:00.123456Z', worker_reads: true, incomplete: false, left_out: [], notes: [] };

function Harness({ initial = { from: '', playbookId: '', path: '' } }: { initial?: PlaybookChoice }) {
  const [choice, setChoice] = useState<PlaybookChoice>(initial);
  return (
    <>
      <PlaybookPicker org="acme" playbooks={[editorPlaybook, registered]} value={choice} onChange={setChoice} />
      <pre data-testid="choice">{JSON.stringify(choice)}</pre>
    </>
  );
}

const choiceNow = () => JSON.parse(screen.getByTestId('choice').textContent ?? '{}') as PlaybookChoice;
const optionsOf = async (label: string) => {
  const user = userEvent.setup();
  await user.click(screen.getByLabelText(label));
  const rows = screen.getAllByRole('option').map((o) => ({ value: o.getAttribute('data-value'), text: o.textContent }));
  await user.keyboard('{Escape}');
  return rows;
};

describe('PlaybookPicker', () => {
  it('offers the inline playbooks by default', async () => {
    mockApi({ 'GET /api/v1/orgs/acme/repos': { repos: [repo] } });
    renderRoutes([{ path: '/x', component: () => <Harness /> }], '/o/acme/x');
    await screen.findByLabelText('Playbook source');
    await pickOption(screen, 'Playbook', { value: 'pb1' });
    expect(choiceNow()).toEqual({ from: 'inline', playbookId: 'pb1', path: '' });
  });

  it("lists a repository's discovered paths, with the registered one's id", async () => {
    mockApi({
      'GET /api/v1/orgs/acme/repos': { repos: [repo] },
      'GET /api/v1/orgs/acme/repos/r1/playbooks': { sources, playbooks: [discovered(), discovered({ path: 'site.yml', playbook_id: 'pb7' })] },
    });
    renderRoutes([{ path: '/x', component: () => <Harness /> }], '/o/acme/x');
    await screen.findByLabelText('Playbook source');
    await pickOption(screen, 'Playbook source', { value: 'r1' });
    await screen.findByText('Commit 9f8e7d6');
    const values = (await optionsOf('Playbook')).map((o) => o.value);
    expect(values).toEqual(expect.arrayContaining(['deploy.yml', 'site.yml']));
    await pickOption(screen, 'Playbook', { value: 'site.yml' });
    expect(choiceNow()).toEqual({ from: 'r1', playbookId: 'pb7', path: 'site.yml' });
    await pickOption(screen, 'Playbook', { value: 'deploy.yml' });
    expect(choiceNow()).toEqual({ from: 'r1', playbookId: '', path: 'deploy.yml' });
  });

  it('never presents a playbook the sync did not read, or left out, as an ordinary one', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/repos': { repos: [repo] },
      'GET /api/v1/orgs/acme/repos/r1/playbooks': {
        sources,
        playbooks: [discovered({ path: 'broken.yml', failed: true }), discovered({ path: 'big.yml', left_out: true })],
      },
    });
    renderRoutes([{ path: '/x', component: () => <Harness /> }], '/o/acme/x');
    await screen.findByLabelText('Playbook source');
    await pickOption(screen, 'Playbook source', { value: 'r1' });
    await screen.findByText('Commit 9f8e7d6');
    const options = await optionsOf('Playbook');
    expect(options.find((o) => o.value === 'broken.yml')?.text).toContain('not read');
    expect(options.find((o) => o.value === 'big.yml')?.text).toContain('left out by the last sync');
  });

  it('says a repository that was never synced lists no playbooks yet', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/repos': { repos: [repo] },
      'GET /api/v1/orgs/acme/repos/r1/playbooks': { playbooks: [] },
    });
    renderRoutes([{ path: '/x', component: () => <Harness /> }], '/o/acme/x');
    await screen.findByLabelText('Playbook source');
    await pickOption(screen, 'Playbook source', { value: 'r1' });
    expect(await screen.findByText('Not synced yet — sync the repository to list its playbooks')).toBeInTheDocument();
  });

  it("starts at the source of a template's current playbook", async () => {
    mockApi({
      'GET /api/v1/orgs/acme/repos': { repos: [repo] },
      'GET /api/v1/orgs/acme/repos/r1/playbooks': { sources, playbooks: [discovered({ path: 'site.yml', playbook_id: 'pb7' })] },
    });
    renderRoutes([{ path: '/x', component: () => <Harness initial={{ from: '', playbookId: 'pb7', path: '' }} /> }], '/o/acme/x');
    await waitFor(() => expect(screen.getByLabelText('Playbook source')).toHaveValue('infra'));
    await waitFor(() => expect(screen.getByLabelText('Playbook')).toHaveValue('site.yml'));
  });
});

describe('TemplateDialog with a playbook from a repository', () => {
  const routes = (posted: { pick?: unknown; create?: unknown }) => ({
    'GET /api/v1/orgs/acme/repos': { repos: [repo] },
    'GET /api/v1/orgs/acme/repos/r1/playbooks': { sources, playbooks: [discovered(), discovered({ path: 'site.yml', playbook_id: 'pb7' })] },
    'GET /api/v1/orgs/acme/playbooks': { playbooks: [editorPlaybook, registered] },
    'GET /api/v1/orgs/acme/inventory/groups': { groups: [] },
    'GET /api/v1/orgs/acme/hosts': { hosts: [], next_cursor: '' },
    'GET /api/v1/orgs/acme/credentials': { credentials: [] },
    'POST /api/v1/orgs/acme/repos/r1/playbooks': (req: MockRequest) => {
      posted.pick = req.body;
      return { created: true, playbook: { id: 'pb9', source: 'repo', repo_id: 'r1', path: 'deploy.yml', summary: '', created_at: '', updated_at: '' } };
    },
    'POST /api/v1/orgs/acme/job-templates': (req: MockRequest) => {
      posted.create = req.body;
      return { template: { id: 't1', name: 'deploy-site' } };
    },
  });

  async function create(path: string) {
    const user = userEvent.setup();
    renderRoutes([{ path: '/x', component: () => <TemplateDialog org="acme" canReadCredentials canCreateCredential={false} onClose={() => {}} /> }], '/o/acme/x');
    const dialog = await screen.findByRole('dialog', { name: 'New job template' });
    await user.type(within(dialog).getByLabelText('Name'), 'deploy-site');
    await pickOption(within(dialog), 'Playbook source', { value: 'r1' });
    await screen.findByText('Commit 9f8e7d6');
    await pickOption(within(dialog), 'Playbook', { value: path });
    await user.click(within(dialog).getByRole('button', { name: 'Create template' }));
  }

  it('registers an unregistered path first and creates the template with its id', async () => {
    const posted: { pick?: unknown; create?: unknown } = {};
    mockApi(routes(posted));
    await create('deploy.yml');
    await screen.findByText('Template deploy-site created');
    expect(posted.pick).toEqual({ path: 'deploy.yml' });
    expect(posted.create).toMatchObject({ name: 'deploy-site', playbook_id: 'pb9' });
  });

  it('uses the id of a registered path without registering it again', async () => {
    const posted: { pick?: unknown; create?: unknown } = {};
    mockApi(routes(posted));
    await create('site.yml');
    await screen.findByText('Template deploy-site created');
    expect(posted.pick).toBeUndefined();
    expect(posted.create).toMatchObject({ playbook_id: 'pb7' });
  });
});

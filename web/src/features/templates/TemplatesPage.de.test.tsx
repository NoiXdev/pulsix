import { describe, expect, it } from 'vitest';
import userEvent from '@testing-library/user-event';
import { screen } from '@testing-library/react';
import { renderListPage } from '../../test/renderListPage.tsx';
import { mockApi } from '../../test/mockApi.ts';
import { TemplatesPage } from './TemplatesPage.tsx';

const ORG = { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'operator', created_at: '' } };
const template = (id: string, name: string, hostIds: string[]) => ({
  id, name, playbook_id: 'pb1', scope: { group_id: null, host_ids: hostIds }, limit: '', extra_vars: {}, created_at: '', updated_at: '',
});

function renderPage() {
  return renderListPage({ path: '/templates', component: TemplatesPage, initial: '/o/acme/templates', locale: 'de' });
}

describe('TemplatesPage in German', () => {
  it('names the run action "Ausführen", the label the triggers hint points to', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/job-templates': { templates: [template('t1', 'deploy-site', ['h1', 'h2']), template('t2', 'patch', ['h1'])] },
      'GET /api/v1/orgs/acme/playbooks': { playbooks: [{ id: 'pb1', source: 'editor', summary: 'Nginx installieren', created_at: '', updated_at: '' }] },
      'GET /api/v1/orgs/acme/inventory/groups': { groups: [] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [] },
    });
    renderPage();

    expect(await screen.findByText('deploy-site')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Ausführen' })).not.toBeInTheDocument();
    await userEvent.setup().click(screen.getByRole('button', { name: 'Aktionen für deploy-site' }));
    expect(await screen.findByRole('menuitem', { name: 'Ausführen' })).toBeInTheDocument();
  });

  it('writes the page and its counts in German, with the singular for one', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/job-templates': { templates: [template('t1', 'deploy-site', ['h1', 'h2']), template('t2', 'patch', ['h1'])] },
      'GET /api/v1/orgs/acme/playbooks': { playbooks: [] },
      'GET /api/v1/orgs/acme/inventory/groups': { groups: [] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [] },
    });
    renderPage();

    expect(await screen.findByRole('heading', { name: 'Job-Vorlagen' })).toBeInTheDocument();
    expect(screen.getByText(/^2 Vorlagen · /)).toBeInTheDocument();
    expect(screen.getByText('2 Hosts')).toBeInTheDocument();
    expect(screen.getByText('1 Host')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Vorlage hinzufügen' })).toBeInTheDocument();
  });
});

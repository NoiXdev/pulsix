import { useState } from 'react';
import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import userEvent from '@testing-library/user-event';
import { mockApi } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { FollowUpsField } from './FollowUpsField.tsx';

const template = (id: string, name: string) => ({
  id, name, playbook_id: 'pb1', scope: { group_id: null, host_ids: [] }, limit: '', extra_vars: {}, created_at: '', updated_at: '',
});

function Harness({ excludeId }: { excludeId?: string }) {
  const [value, setValue] = useState<string[]>([]);
  return <FollowUpsField org="acme" id="fu" value={value} onChange={setValue} excludeId={excludeId} />;
}

function renderField(excludeId?: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <Harness excludeId={excludeId} />
    </QueryClientProvider>,
  );
}

describe('FollowUpsField', () => {
  it('offers the organization templates except the excluded one', async () => {
    mockApi({ 'GET /api/v1/orgs/acme/job-templates': { templates: [template('a', 'alpha'), template('b', 'beta'), template('c', 'gamma')] } });
    renderField('b');
    expect(await screen.findByText('Started on the same hosts when a run of this template succeeds.')).toBeInTheDocument();
    await userEvent.setup().click(screen.getByLabelText('Follow-ups on success'));
    const labels = (await screen.findAllByRole('option')).map((o) => o.getAttribute('data-label')).filter((l) => l !== 'Add a follow-up');
    expect(labels).toEqual(['alpha', 'gamma']);
  });

  it('keeps the order the follow-ups were chosen in', async () => {
    mockApi({ 'GET /api/v1/orgs/acme/job-templates': { templates: [template('a', 'alpha'), template('b', 'beta'), template('c', 'gamma')] } });
    renderField();
    await screen.findByText('Started on the same hosts when a run of this template succeeds.');
    await pickOption(screen, 'Follow-ups on success', 'gamma');
    await pickOption(screen, 'Follow-ups on success', 'alpha');
    const list = screen.getByRole('list', { name: 'Chosen follow-ups' });
    expect(within(list).getAllByRole('listitem').map((li) => li.textContent)).toEqual(['gamma', 'alpha']);
  });
});

import { describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { I18nProvider } from '../../i18n/I18nProvider.tsx';
import { RunRequestFollowUps } from './RunRequestFollowUps.tsx';
import { startableFollowUps, type PictureFollowUps } from './followUps.ts';

function show(followUps: PictureFollowUps, deselected: string[] = [], onToggle = vi.fn(), disabled = false, locale: 'en' | 'de' = 'en') {
  render(
    <I18nProvider locale={locale}>
      <RunRequestFollowUps followUps={followUps} deselected={new Set(deselected)} onToggle={onToggle} disabled={disabled} />
    </I18nProvider>,
  );
  return onToggle;
}

const TREE: PictureFollowUps = {
  start: true,
  templates: [
    {
      template_id: 'f1',
      template_name: 'reload-lb',
      template_revision: 3,
      follow_ups: [
        { template_id: 'f2', template_name: 'warm-cache', template_revision: 9, follow_ups: [{ template_id: 'f1', template_name: 'reload-lb', stop: 'in_chain' }] },
        { template_id: 'f3', template_name: 'too-far', stop: 'too_deep' },
      ],
    },
    { template_id: 'f4', template_name: 'loop-back', stop: 'in_chain' },
    { template_id: '0190aaaa-bbbb-cccc-dddd-eeeeeeeeeeee', template_name: '', stop: 'deleted' },
  ],
};

describe('RunRequestFollowUps', () => {
  it('names the too_deep and deleted stops in German', () => {
    show(TREE, [], vi.fn(), false, 'de');
    expect(screen.getByText(/Kette zu lang/)).toBeInTheDocument();
    expect(screen.getByText(/Vorlage gelöscht/)).toBeInTheDocument();
    expect(screen.getAllByText(/schon in dieser Kette/)).toHaveLength(2);
  });

  it('names the follow-ups without checkboxes when approvals start none', () => {
    show({ start: false, templates: [{ template_id: 'f1', template_name: 'reload-lb' }] });
    expect(
      screen.getByText('Approving starts none of these follow-ups. Your organization has turned that off in Settings → General.'),
    ).toBeInTheDocument();
    expect(screen.getByText('reload-lb')).toBeInTheDocument();
    expect(screen.queryByRole('checkbox')).not.toBeInTheDocument();
    expect(screen.queryByText(/Follow-ups start later/)).not.toBeInTheDocument();
  });

  it('offers a checked checkbox per startable direct follow-up and shows the rest read-only', async () => {
    const user = userEvent.setup();
    const onToggle = show(TREE);
    const boxes = screen.getAllByRole('checkbox');
    expect(boxes).toHaveLength(1);
    expect(boxes[0]).toBeChecked();
    expect(screen.getByRole('checkbox', { name: 'reload-lb' })).toBeEnabled();
    expect(screen.getByText('· revision 3')).toBeInTheDocument();
    expect(screen.getByText('warm-cache')).toBeInTheDocument();
    expect(screen.getByText(/revision 9/)).toBeInTheDocument();
    expect(screen.getAllByText(/already in this chain/)).toHaveLength(2);
    expect(screen.getByText(/chain too long/)).toBeInTheDocument();
    expect(screen.getByText(/template deleted/)).toBeInTheDocument();
    expect(screen.getByText('0190aaaa')).toBeInTheDocument();
    expect(screen.getByText(/Follow-ups start later, when this run succeeds, on the hosts it ran on\./)).toBeInTheDocument();
    await user.click(boxes[0]);
    expect(onToggle).toHaveBeenCalledWith('f1');
  });

  it('unchecks a deselected follow-up and disables the boxes when asked', () => {
    show(TREE, ['f1'], vi.fn(), true);
    const box = screen.getByRole('checkbox', { name: 'reload-lb' });
    expect(box).not.toBeChecked();
    expect(box).toBeDisabled();
  });

  it('starts none and offers no enabled checkbox when truncated', () => {
    show({ ...TREE, truncated: true });
    expect(screen.getByText('More follow-ups than this page can show; approving starts none of them.')).toBeInTheDocument();
    expect(within(document.body).queryAllByRole('checkbox')).toHaveLength(0);
  });
});

describe('startableFollowUps', () => {
  it('is empty without the mode, when truncated or absent, and skips stop-marked ones', () => {
    expect(startableFollowUps(undefined)).toEqual([]);
    expect(startableFollowUps({ ...TREE, start: false })).toEqual([]);
    expect(startableFollowUps({ ...TREE, truncated: true })).toEqual([]);
    expect(startableFollowUps(TREE).map((f) => f.template_id)).toEqual(['f1']);
  });
});

describe('RunRequestFollowUps bindings', () => {
  it('shows the hosts and extra vars an approval binds for each follow-up (backlog 2.143)', () => {
    show({
      start: true,
      templates: [
        {
          template_id: 'f1',
          template_name: 'reload-lb',
          template_revision: 3,
          targets: [
            { id: 'h1', name: 'web-01', address: '10.0.0.11' },
            { id: 'h2', name: 'web-02', address: '10.0.0.12' },
          ],
          extra_vars: { port: 8080, token: { $secret: 'set' } },
          vars_digest: 'd1',
          follow_ups: [{ template_id: 'f2', template_name: 'warm-cache', template_revision: 9, vars_digest: 'd2' }],
        },
      ],
    });
    const lb = screen.getByRole('group', { name: 'What approving binds for reload-lb' });
    expect(lb).toHaveTextContent('Hosts: web-01, web-02');
    expect(lb).toHaveTextContent('Extra vars: port = 8080, token = secret (hidden)');
    const cache = screen.getByRole('group', { name: 'What approving binds for warm-cache' });
    expect(cache).toHaveTextContent('Hosts: none, it would not start');
    expect(cache).not.toHaveTextContent('Extra vars');
    expect(screen.getByText(/Approving also binds the hosts and variables shown for each/)).toBeInTheDocument();
  });

  it('shows no binding for a follow-up the picture does not bind', () => {
    show({ start: true, templates: [{ template_id: 'f1', template_name: 'reload-lb', template_revision: 3 }] });
    expect(screen.queryByRole('group', { name: /What approving binds/ })).not.toBeInTheDocument();
  });
});

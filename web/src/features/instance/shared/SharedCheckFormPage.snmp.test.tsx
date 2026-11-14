import { describe, expect, it } from 'vitest';
import { screen } from '@testing-library/react';
import { mockApi } from '../../../test/mockApi.ts';
import { pickOption } from '../../../test/pickOption.ts';
import { ADMIN, renderRoutes } from '../../../test/renderRoutes.tsx';
import { en } from '../../../i18n/en/index.ts';
import { de } from '../../../i18n/de/index.ts';
import { SharedCheckFormPage } from './SharedCheckFormPage.tsx';

/**
 * The shared check form's snmp plugin (backlog 3.13d): a shared definition
 * reaches every organization, and each reads its own secret of the name,
 * so the secret hint must not speak of "this organization".
 */
describe('SharedCheckFormPage — snmp plugin', () => {
  it('points the secret at each organization’s own secrets', async () => {
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/checks/definitions': { definitions: [] },
    });
    renderRoutes(
      [
        { path: '/settings/shared/checks', component: () => <div>the list</div> },
        { path: '/settings/shared/checks/new', component: SharedCheckFormPage },
      ],
      '/o/acme/settings/shared/checks/new',
    );
    await screen.findByLabelText('Key');
    await pickOption(screen, 'Plugin', { value: 'snmp' });
    expect(screen.getByText(/the organization’s secrets \(Settings → Secrets\)/)).toBeInTheDocument();
    expect(screen.getByText(/each one’s own secret of that name/)).toBeInTheDocument();
    expect(screen.queryByText(/this organization/)).not.toBeInTheDocument();
    expect(screen.queryByText(/agent\.yaml/)).not.toBeInTheDocument();
  });

  it.each([
    ['English', en.checks.params.snmp.secret.hint, /this organization/],
    ['German', de.checks.params.snmp.secret.hint, /dieser Organisation/],
  ] as const)('has a hint that reads right in both forms (%s)', (_name, hint, wrong) => {
    expect(hint).not.toMatch(wrong);
  });
});

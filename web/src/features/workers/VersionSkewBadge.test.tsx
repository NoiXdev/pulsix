import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { I18nProvider } from '../../i18n/I18nProvider.tsx';
import { VersionSkewBadge } from './VersionSkewBadge.tsx';

describe('VersionSkewBadge', () => {
  it.each([
    ['en', 'update_required', 'Update required'],
    ['en', 'newer', 'Newer than manager'],
    ['de', 'update_required', 'Update nötig'],
    ['de', 'newer', 'Neuer als Manager'],
  ] as const)('says what %s %s means', (locale, status, label) => {
    render(
      <I18nProvider locale={locale}>
        <VersionSkewBadge status={status} />
      </I18nProvider>,
    );
    expect(screen.getByText(label)).toBeInTheDocument();
  });

  it.each(['current', 'previous_minor', 'unknown', undefined, 'something_new'])('renders nothing for %s', (status) => {
    const { container } = render(
      <I18nProvider locale="en">
        <VersionSkewBadge status={status} />
      </I18nProvider>,
    );
    expect(container).toBeEmptyDOMElement();
  });
});

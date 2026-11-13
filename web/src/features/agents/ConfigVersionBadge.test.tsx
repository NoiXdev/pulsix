import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { I18nProvider } from '../../i18n/I18nProvider.tsx';
import { ConfigVersionBadge } from './ConfigVersionBadge.tsx';

const A = '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef';
const B = 'fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210';

function renderBadge(locale: 'en' | 'de', reported?: string, expected?: string) {
  return render(
    <I18nProvider locale={locale}>
      <ConfigVersionBadge reported={reported} expected={expected} />
    </I18nProvider>,
  );
}

describe('ConfigVersionBadge', () => {
  it.each([
    ['en', 'Config current'],
    ['de', 'Konfiguration aktuell'],
  ] as const)('says %s: the agent runs the expected configuration', (locale, label) => {
    renderBadge(locale, A, A);
    expect(screen.getByText(label)).toBeInTheDocument();
  });

  it.each([
    ['en', 'Config outdated', 'Agent runs 01234567 · manager expects fedcba98'],
    ['de', 'Konfiguration veraltet', 'Agent läuft mit 01234567 · Manager erwartet fedcba98'],
  ] as const)('says %s: outdated, with both versions shortened', (locale, label, detail) => {
    renderBadge(locale, A, B);
    expect(screen.getByText(label)).toBeInTheDocument();
    expect(screen.getByTitle(detail)).toBeInTheDocument();
  });

  it('names an agent that has applied no configuration yet', () => {
    renderBadge('en', '', B);
    expect(screen.getByText('Config outdated')).toBeInTheDocument();
    expect(screen.getByTitle('Agent runs none · manager expects fedcba98')).toBeInTheDocument();
  });

  it.each([
    [undefined, undefined],
    [A, undefined],
    [undefined, A],
  ])('renders nothing while the check is unknown (%s, %s)', (reported, expected) => {
    const { container } = renderBadge('en', reported, expected);
    expect(container).toBeEmptyDOMElement();
  });
});

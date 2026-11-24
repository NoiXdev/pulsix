import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { I18nProvider } from '../../i18n/I18nProvider.tsx';
import { en } from '../../i18n/en/index.ts';
import { de } from '../../i18n/de/index.ts';
import { AreaGrid } from './AreaGrid.tsx';

const KEYS = ['inventory', 'repos', 'templates', 'jobs', 'monitoring', 'credentials', 'activity'] as const;

describe('AreaGrid descriptions', () => {
  it.each([
    ['en', en],
    ['de', de],
  ] as const)('shows one description per area in %s', (locale, messages) => {
    render(
      <I18nProvider locale={locale}>
        <AreaGrid role="owner" value={[]} onChange={() => undefined} />
      </I18nProvider>,
    );
    const texts = KEYS.map((k) => messages.tokens.areas.descriptions[k]);
    expect(new Set(texts).size).toBe(KEYS.length);
    for (const text of texts) {
      expect(text.length).toBeGreaterThan(10);
      expect(screen.getByText(text)).toBeInTheDocument();
    }
    expect(screen.getByText(messages.tokens.areas.writeHint)).toBeInTheDocument();
  });

  it('keeps the checkbox names free of the description', () => {
    render(
      <I18nProvider locale="en">
        <AreaGrid role="owner" value={[]} onChange={() => undefined} />
      </I18nProvider>,
    );
    expect(screen.getByRole('checkbox', { name: 'Inventory: read' })).toBeInTheDocument();
  });
});

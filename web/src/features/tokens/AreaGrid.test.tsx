import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
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

  it.each([
    ['en', en],
    ['de', de],
  ] as const)('puts each description in its own area row in %s', (locale, messages) => {
    render(
      <I18nProvider locale={locale}>
        <AreaGrid role="owner" value={[]} onChange={() => undefined} />
      </I18nProvider>,
    );
    const a = messages.tokens.areas;
    for (const key of ['inventory', 'repos', 'templates', 'jobs', 'monitoring'] as const) {
      const row = screen.getByText(a.rows[key]).closest('tr') as HTMLElement;
      expect(within(row).getByText(a.descriptions[key])).toBeInTheDocument();
    }
    for (const key of ['credentials', 'activity'] as const) {
      const label = screen.getByText(a.descriptions[key]).closest('label') as HTMLElement;
      expect(within(label).getByText(a[key])).toBeInTheDocument();
    }
  });

  it('links each checkbox to its area description for screen readers', () => {
    render(
      <I18nProvider locale="en">
        <AreaGrid role="owner" value={[]} onChange={() => undefined} />
      </I18nProvider>,
    );
    const a = en.tokens.areas;
    const cases: [string, string][] = [
      ['Inventory: read', a.descriptions.inventory],
      ['Inventory: write', a.descriptions.inventory],
      ['Jobs: request runs', a.descriptions.jobs],
      ['Monitoring & alerts: read', a.descriptions.monitoring],
      [a.credentials, a.descriptions.credentials],
      [a.activity, a.descriptions.activity],
    ];
    for (const [name, text] of cases) {
      expect(screen.getByRole('checkbox', { name })).toHaveAccessibleDescription(text);
    }
  });

  it('keeps the role hint in the description of a disabled checkbox', () => {
    render(
      <I18nProvider locale="en">
        <AreaGrid role="viewer" value={[]} onChange={() => undefined} />
      </I18nProvider>,
    );
    const box = screen.getByRole('checkbox', { name: 'Inventory: write' });
    expect(box).toBeDisabled();
    expect(box).toHaveAccessibleDescription(expect.stringContaining(en.tokens.areas.descriptions.inventory));
    expect(box).toHaveAccessibleDescription(expect.stringContaining('higher role'));
  });
});

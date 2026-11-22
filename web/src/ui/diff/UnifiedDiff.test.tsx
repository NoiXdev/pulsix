import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { UnifiedDiff, MAX_SHOWN_LINES } from './UnifiedDiff.tsx';
import { MAX_COMPARE_LINES } from './lineDiff.ts';

const rows = () => within(screen.getByRole('figure', { name: 'Differences' })).getAllByRole('listitem');

describe('UnifiedDiff', () => {
  it('shows removed, added and context lines of before and after, with the headers', () => {
    render(<UnifiedDiff before={'listen 80;\nroot /srv;\n'} after={'listen 8080;\nroot /srv;\n'} beforeHeader="/etc/nginx/a.conf" afterHeader="/etc/nginx/a.conf" />);
    expect(rows().map((r) => [r.dataset.kind, r.textContent])).toEqual([
      ['header', '--- /etc/nginx/a.conf'],
      ['header', '+++ /etc/nginx/a.conf'],
      ['hunk', '@@ -1,2 +1,2 @@'],
      ['delete', '-listen 80;'],
      ['insert', '+listen 8080;'],
      ['context', ' root /srv;'],
    ]);
  });

  it('says when the two sides do not differ', () => {
    render(<UnifiedDiff before="a\n" after="a\n" />);
    expect(screen.getByText('No differences.')).toBeInTheDocument();
  });

  it('marks a last line without a line break', () => {
    render(<UnifiedDiff before="a" after="b" />);
    expect(rows().map((r) => r.textContent)).toContain('\\ No newline at end of file');
  });

  it('shows a prepared diff as it is, coloured by its lines', () => {
    render(<UnifiedDiff prepared={'--- before\n+++ after\n@@ -1 +1 @@\n-old\n+new\n same'} />);
    expect(rows().map((r) => [r.dataset.kind, r.textContent])).toEqual([
      ['header', '--- before'],
      ['header', '+++ after'],
      ['hunk', '@@ -1 +1 @@'],
      ['delete', '-old'],
      ['insert', '+new'],
      ['context', ' same'],
    ]);
  });

  it('collapses a long diff to its first lines until asked for all', async () => {
    const user = userEvent.setup();
    const after = Array.from({ length: MAX_SHOWN_LINES + 50 }, (_, i) => `line ${i}`).join('\n') + '\n';
    render(<UnifiedDiff before="" after={after} />);
    expect(rows()).toHaveLength(MAX_SHOWN_LINES);
    await user.click(screen.getByRole('button', { name: `Show all ${MAX_SHOWN_LINES + 51} lines` }));
    expect(rows()).toHaveLength(MAX_SHOWN_LINES + 51);
    expect(screen.queryByRole('button', { name: /Show all/ })).not.toBeInTheDocument();
  });

  it('refuses to compare a side that is too large', () => {
    render(<UnifiedDiff before={'x\n'.repeat(MAX_COMPARE_LINES + 1)} after="y\n" />);
    expect(screen.getByText(/Too large to compare/)).toBeInTheDocument();
    expect(screen.queryByRole('figure')).not.toBeInTheDocument();
  });
});

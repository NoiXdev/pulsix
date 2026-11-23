import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { UnifiedDiff, MAX_PREPARED_LINES, MAX_SHOWN_LINES } from './UnifiedDiff.tsx';
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

  it('collapses a long prepared diff too, and cuts one that is far too long even for Show all', async () => {
    const user = userEvent.setup();
    const lines = Array.from({ length: MAX_PREPARED_LINES + 100 }, (_, i) => `+line ${i}`);
    render(<UnifiedDiff prepared={`@@ -0,0 +1,${lines.length} @@\n${lines.join('\n')}\n`} />);
    expect(rows()).toHaveLength(MAX_SHOWN_LINES);
    await user.click(screen.getByRole('button', { name: /^Show all/ }));
    // The cap, plus the note saying the rest is not shown.
    expect(rows()).toHaveLength(MAX_PREPARED_LINES + 1);
    expect(rows().at(-1)).toHaveTextContent(`Cut off: showing the first ${MAX_PREPARED_LINES} of ${MAX_PREPARED_LINES + 101} lines.`);
  });

  it('refuses to compare a side that is too large', () => {
    render(<UnifiedDiff before={'x\n'.repeat(MAX_COMPARE_LINES + 1)} after="y\n" />);
    expect(screen.getByText(/Too large to compare/)).toBeInTheDocument();
    expect(screen.queryByRole('figure')).not.toBeInTheDocument();
  });
});

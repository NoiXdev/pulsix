import { describe, expect, it } from 'vitest';
import { lineDiff, MAX_COMPARE_LINES, type DiffHunk } from './lineDiff.ts';

/** Renders hunks the way `diff -u` prints them, for readable expectations. */
function unified(hunks: readonly DiffHunk[]): string[] {
  return hunks.flatMap((h) => [
    `@@ -${h.oldStart},${h.oldLines} +${h.newStart},${h.newLines} @@`,
    ...h.lines.flatMap((l) => [`${l.kind === 'insert' ? '+' : l.kind === 'delete' ? '-' : ' '}${l.text}`, ...(l.noNewline ? ['\\ No newline at end of file'] : [])]),
  ]);
}

function hunksOf(before: string, after: string, context?: number): string[] {
  const out = lineDiff(before, after, { context });
  if (out.kind !== 'hunks') throw new Error(`expected hunks, got ${out.kind}`);
  return unified(out.hunks);
}

describe('lineDiff', () => {
  it.each([
    ['equal texts', 'a\nb\nc\n', 'a\nb\nc\n', []],
    ['two empty texts', '', '', []],
    ['an insert in the middle', 'a\nb\nc\n', 'a\nb\nx\nc\n', ['@@ -1,3 +1,4 @@', ' a', ' b', '+x', ' c']],
    ['a delete', 'a\nb\nc\n', 'a\nc\n', ['@@ -1,3 +1,2 @@', ' a', '-b', ' c']],
    ['a replaced line', 'listen 80;\nroot /srv;\n', 'listen 8080;\nroot /srv;\n', ['@@ -1,2 +1,2 @@', '-listen 80;', '+listen 8080;', ' root /srv;']],
    ['a new file', '', 'a\nb\n', ['@@ -0,0 +1,2 @@', '+a', '+b']],
    ['a removed file', 'a\n', '', ['@@ -1,1 +0,0 @@', '-a']],
    ['a newline added at the end', 'a\nb', 'a\nb\n', ['@@ -1,2 +1,2 @@', ' a', '-b', '\\ No newline at end of file', '+b']],
    ['a newline dropped at the end', 'a\n', 'a', ['@@ -1,1 +1,1 @@', '-a', '+a', '\\ No newline at end of file']],
  ])('%s', (_name, before, after, want) => {
    expect(hunksOf(before, after)).toEqual(want);
  });

  it('keeps three lines of context and splits far-apart changes into hunks', () => {
    const before = Array.from({ length: 20 }, (_, i) => `l${i + 1}`).join('\n') + '\n';
    const after = before.replace('l2\n', 'L2\n').replace('l18\n', 'L18\n');
    expect(hunksOf(before, after)).toEqual([
      '@@ -1,5 +1,5 @@', ' l1', '-l2', '+L2', ' l3', ' l4', ' l5',
      '@@ -15,6 +15,6 @@', ' l15', ' l16', ' l17', '-l18', '+L18', ' l19', ' l20',
    ]);
  });

  it('merges changes whose context overlaps into one hunk', () => {
    const before = 'a\nb\nc\nd\ne\nf\ng\n';
    const after = 'A\nb\nc\nd\ne\nf\nG\n';
    expect(hunksOf(before, after)).toEqual(['@@ -1,7 +1,7 @@', '-a', '+A', ' b', ' c', ' d', ' e', ' f', '-g', '+G']);
  });

  it('keeps far-apart changes in separate hunks with a smaller context', () => {
    expect(hunksOf('a\nb\nc\nd\ne\nf\ng\n', 'A\nb\nc\nd\ne\nf\nG\n', 2)).toEqual(['@@ -1,3 +1,3 @@', '-a', '+A', ' b', ' c', '@@ -5,3 +5,3 @@', ' e', ' f', '-g', '+G']);
  });

  it('numbers a pure insert after the line it follows', () => {
    expect(hunksOf('a\nb\nc\nd\n', 'a\nb\nx\nc\nd\n', 0)).toEqual(['@@ -2,0 +3,1 @@', '+x']);
  });

  it('finds the smallest edit, not a delete-all-insert-all', () => {
    expect(hunksOf('a\nb\nc\nd\n', 'b\nc\nd\ne\n')).toEqual(['@@ -1,4 +1,4 @@', '-a', ' b', ' c', ' d', '+e']);
  });

  it('numbers the lines of each side', () => {
    const out = lineDiff('a\nb\n', 'a\nx\nb\n');
    if (out.kind !== 'hunks') throw new Error('expected hunks');
    expect(out.hunks[0].lines.map((l) => [l.kind, l.oldLine, l.newLine])).toEqual([
      ['context', 1, 1],
      ['insert', undefined, 2],
      ['context', 2, 3],
    ]);
  });

  it('refuses to compare a side longer than the cap', () => {
    const big = 'x\n'.repeat(MAX_COMPARE_LINES + 1);
    expect(lineDiff(big, 'x\n')).toEqual({ kind: 'tooLarge' });
    expect(lineDiff('x\n', big)).toEqual({ kind: 'tooLarge' });
    expect(lineDiff('x\n'.repeat(MAX_COMPARE_LINES), 'y\n').kind).toBe('hunks');
  });

  it('stays correct when two large texts share nothing', () => {
    const before = Array.from({ length: 3000 }, (_, i) => `a${i}`).join('\n') + '\n';
    const after = Array.from({ length: 3000 }, (_, i) => `b${i}`).join('\n') + '\n';
    const out = lineDiff(before, after);
    if (out.kind !== 'hunks') throw new Error('expected hunks');
    const lines = out.hunks.flatMap((h) => h.lines);
    expect(lines.filter((l) => l.kind === 'delete')).toHaveLength(3000);
    expect(lines.filter((l) => l.kind === 'insert')).toHaveLength(3000);
  });
});

import { describe, expect, it } from 'vitest';
import { invalidVaultLabel } from './vaultIdentity.ts';

// invalidVaultLabel mirrors internal/vaultlabel.Invalid (Go is the
// reference this file's regex is checked against — see that package's own
// test, internal/vaultlabel/vaultlabel_test.go, for the fuller table). The
// BOM and bidirectional-override cases are the whole-branch review's
// finding: a reviewer verified with Go and Node that a label beginning with
// U+FEFF was refused here (JavaScript's String.trim() strips it) but
// accepted by the server (Go's strings.TrimSpace does not treat it as
// whitespace) — a three-way disagreement the two copies were never meant to
// have. Both sides now reject on the rule itself (any Unicode control or
// format character), not as a side effect of either language's trim.
//
// Backlog 2.97 closed the smaller disagreement that widening left behind:
// trim() and strings.TrimSpace still disagree about which of U+0085 (NEL)
// and U+FEFF counts as whitespace (ECMAScript's WhiteSpace production
// includes the BOM and excludes NEL; Go's TrimSpace, following Unicode's
// White_Space property, is the other way around), so which of the two
// checks caught a given label used to decide the reported reason — the
// exact same wording difference the BOM case below used to carry. Both
// checks now share one message (WHITESPACE_OR_CONTROL_OR_FORMAT), so every
// case below asserts the full merged wording, not a substring that would
// still pass if the two halves disagreed again.
describe('invalidVaultLabel', () => {
  const MERGED = 'label has leading or trailing whitespace, or contains a Unicode control or format character';
  const cases: Array<[string, string]> = [
    ['', 'label is empty'],
    [' prod', MERGED],
    ['prod ', MERGED],
    ['prod,staging', 'comma'],
    ['prod@file', "'@'"],
    ['prod\nhost_key_checking = False', MERGED],
    ['prod\rx', MERGED],
    // U+0085 (NEL): Unicode White_Space, so trim() strips it — the
    // whitespace half of the merged check, same reported message as the
    // plain-space cases above.
    ['prod', MERGED],
    ['prod', MERGED],
    // U+FEFF (BYTE ORDER MARK): ECMAScript's trim() also strips a leading
    // or trailing one (unlike Go's TrimSpace — see vaultlabel_test.go's
    // own "leading BOM" case, which lands on the Cc/Cf half instead), so
    // the leading/trailing positions and the mid-label position below
    // reach the merged check by different halves — and now report the
    // exact same message either way, which is the fix.
    ['﻿prod', MERGED],
    ['prod﻿', MERGED],
    ['pro﻿d', MERGED],
    // U+202E (RIGHT-TO-LEFT OVERRIDE), category Cf — renders everything
    // after it right-to-left, which can make a label display as something
    // other than what it is on the one screen whose whole job is naming
    // which password opens which file. Unlike U+FEFF, ECMAScript's
    // trim() does not treat it as whitespace, so both the leading and the
    // mid-label position land on the Cc/Cf half here.
    ['‮prod', MERGED],
    ['pro‮d', MERGED],
  ];
  for (const [label, want] of cases) {
    it(`rejects ${JSON.stringify(label)} for ${JSON.stringify(want)}`, () => {
      expect(invalidVaultLabel(label)).toContain(want);
    });
  }

  it('accepts a plain label', () => {
    expect(invalidVaultLabel('prod')).toBe('');
  });

  it('accepts umlauts, which are neither control nor format characters', () => {
    expect(invalidVaultLabel('produktion-ä')).toBe('');
  });

  it(`rejects an overlong label for its own reason, not the character rule`, () => {
    expect(invalidVaultLabel('a'.repeat(101))).toContain('exceeds');
  });
});

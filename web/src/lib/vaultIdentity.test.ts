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
describe('invalidVaultLabel', () => {
  const cases: Array<[string, string]> = [
    ['', 'label is empty'],
    [' prod', 'leading or trailing whitespace'],
    ['prod ', 'leading or trailing whitespace'],
    ['prod,staging', 'comma'],
    ['prod@file', "'@'"],
    ['prod\nhost_key_checking = False', 'control or format'],
    ['prod\rx', 'control or format'],
    // U+FEFF (BYTE ORDER MARK) in the middle of a label: not caught by the
    // leading/trailing whitespace check, so this is the new rule's own
    // catch — see the dedicated case below for the leading position, which
    // the whitespace check intercepts first on this side.
    ['pro﻿d', 'control or format'],
    // U+202E (RIGHT-TO-LEFT OVERRIDE), category Cf — renders everything
    // after it right-to-left, which can make a label display as something
    // other than what it is on the one screen whose whole job is naming
    // which password opens which file. Unlike U+FEFF, ECMAScript's
    // trim() does not treat it as whitespace, so both the leading and the
    // mid-label position land on the new rule here.
    ['‮prod', 'control or format'],
    ['pro‮d', 'control or format'],
  ];
  for (const [label, want] of cases) {
    it(`rejects ${JSON.stringify(label)} for ${JSON.stringify(want)}`, () => {
      expect(invalidVaultLabel(label)).toContain(want);
    });
  }

  // A leading/trailing U+FEFF is the one case where the client and the
  // server, now that both reject it, still don't reject it for the SAME
  // stated reason: ECMAScript's String.trim() is specified to strip U+FEFF
  // as whitespace, so it never reaches this function's new regex at all —
  // it is caught by the pre-existing whitespace check instead, same as
  // today. Go's strings.TrimSpace does not share that quirk (see
  // internal/vaultlabel/vaultlabel_test.go's own "leading BOM" case, which
  // DOES land on the new rule there). Both sides still refuse the label,
  // which is what the finding this fixes was actually about — the message
  // text naming a different reason for one specific position is a
  // property of the two languages' trim() built-ins disagreeing about
  // what counts as whitespace, not something this file's own rule controls.
  it('still refuses a leading or trailing BOM, via the pre-existing whitespace check', () => {
    expect(invalidVaultLabel('﻿prod')).toContain('whitespace');
    expect(invalidVaultLabel('prod﻿')).toContain('whitespace');
  });

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

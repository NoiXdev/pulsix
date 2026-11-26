# Sibling combinators and more pseudo-classes for html assertions (backlog 3.90) — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** backlog 3.90: "CSS sibling combinators and more pseudo-classes for html assertions — From 3.13b. `+` and `~` and further pseudo-classes for `html(...)`, with a bounded matcher like the current one."

**Architecture:** the grammar lives in `internal/probe/assert/selector.go` (parsed at validation by `internal/checkplugins` and again by the probe), the matcher in `internal/probe/http/html_select.go`: one pre-order walk that carries, per comma-separated part, a bit set of the compounds already matched (spec `docs/superpowers/specs/2026-11-16-web-assertions-design.md`, section 3, "Selector subset"). Sibling combinators become two more ways a state is handed on — to the next element sibling (`+`) or to every later one (`~`) — through a record shared by one parent's children, which the walk visits in order with each earlier sibling's subtree finished first. The SPA validates only the shape of a selector, so it changes only the help text.

## Global Constraints
- Bounds of 3.13b unchanged: 256 bytes per selector, 8 compounds per part, 16 simple selectors per compound, 15 000 000 steps per assertion and 60 000 000 per check, the probe deadline looked at before every node. Every new step is charged before it is taken.
- A selector the 3.13b grammar accepted selects exactly what it selected before (the existing expectation and real-page tests stay green unchanged).
- No proto change, no migration, no new dependency.
- Tests per package (`go test -race -timeout 900s ./internal/probe/... ./internal/checkplugins/...`); `golangci-lint run` and `GOOS=windows golangci-lint run ./internal/probe/...` clean; SPA `npm run lint`, `npx tsc -b`, `npx vitest run` on touched files.

## Rulings
- R1 `+` and `~` are matched in the same walk: a parent's children share one record of the states handed on by earlier element siblings (`adjacent`: those of the element just before, replaced at each element; `general`: those of every earlier element, accumulated). Text nodes neither take nor break a state, as in CSS (comments are not in the probe's tree) — the walk stays linear, no sibling is ever revisited — wrong would mean a missed or extra match, pinned by the expectation tests.
- R2 New pseudo-classes: `:only-child`, `:nth-last-child()`, `:first-of-type`, `:last-of-type`, `:only-of-type`, `:nth-of-type()`, `:nth-last-of-type()`, `:empty`, `:root`, `:not()`. Still refused, naming the pseudo-class: `:has`, `:is`, `:where`, `:nth-child(… of S)`, user-action and form-state pseudo-classes (`:hover`, `:checked`, `:disabled` …), `:lang`, `:dir`, `:link` and pseudo-elements — they either search a subtree (the reason 3.13b refused them) or depend on state a fetched page does not have — a user asking for one gets a clear refusal at save time.
- R3 The `nth` pseudo-classes take CSS's `An+B` (`odd`, `even`, `3`, `2n+1`, `-n+3`, `n`, `+2`, with spaces around the binary sign) with `|A|` and `|B|` at most 1 000 000. `:nth-child(0)`, `(-1)` and `(+2)`, refused by 3.13b as "not a whole number from 1", are now valid CSS (the first two select nothing) — refusing them would be the stricter dialect a user's browser does not have; the cost is only that a selector that can never match is accepted.
- R4 `:not()` takes a comma-separated list of compounds (type, `*`, id, class, attribute and pseudo-classes) without combinators and without a nested `:not`; each simple selector inside counts toward the compound's 16, the `:not` itself as one more — the work per element stays bounded by the compound cap, so the walk stays linear; a user wanting `:not(a b)` gets a refusal naming it.
- R5 `:empty` is an element without child nodes; whitespace text makes an element non-empty (Selectors Level 3, as browsers do); comments are not in the tree and so never count — a user expecting Level 4's whitespace rule sees fewer matches; browsers agree with this choice.
- R6 `:root` is an element whose parent is the document. The probe's tree implies no `html` element, so a fragment page with several top-level elements has several roots — on any real page it is `html`.
- R7 The `of-type` positions are counted per parent only when a selector uses an `of-type` pseudo-class, comparing element names as the tree stores them (lower-cased by the tokenizer), charging one step per element child and its name's bytes — selectors without them cost exactly what they cost before.
- R8 Version skew: no gating and no protocol change. Every new construct (`+`, `~`, the new pseudo-classes, `An+B`) is refused by the 3.13b parser, so an older agent vantage — or an older manager during a rolling upgrade — reports that one assertion as the error "the assertion is not valid" (the item reads unknown) instead of judging it with another meaning; its other assertions run as before. Recorded in `docs/upgrades.md` — cost: a check probed only from older agents shows such an assertion unknown until they are updated.

## Review Focus
1. Sibling states leaking: into a sibling's descendants, across parents, or past a text node in the wrong direction — `TestSelectAllSiblings` (`h1 + p` vs a `p` nested in the next sibling; text and comments between siblings; `~` across non-matching siblings; mixed `a ~ b > c`, `ul > li + li a`).
2. `An+B` parsing: signs, spaces, `n` alone, overflow, `2 n`, `- n`, `n-`, `of S` — `TestParseNth`, `TestParseSelectorRefuses`.
3. Hostile pages with the new constructs stay within the budget: 150 000 siblings under 15 parts of 8-compound `~` chains, 150 000 distinct sibling names under `:nth-of-type`, `:not` lists at the simple cap — rows in `TestSelectAllBoundsHostilePages`.
4. Old selectors unchanged — the 3.13b rows of `TestSelectAllExpectations` and `TestSelectAllOnARealPage` untouched.

## Task 1: Grammar
**Files:** `internal/probe/assert/selector.go` (`Part.Combinators []Combinator` replacing `Child []bool`; `Pseudo{Kind, A, B, Not []Compound}`; `Selector.OfType()`), `selector_test.go`, `internal/checkplugins/http_test.go` (`TestAssertionFormat` rows).
**Tests:** `TestParseSelector`, `TestParseSelectorRefuses`, `TestParseNth`, `TestAssertionFormat`.
- [ ] Commit `feat(probe): html selectors parse sibling combinators and more pseudo-classes`.

## Task 2: Matcher
**Files:** `internal/probe/http/html_select.go` (sibling record, of-type positions, pseudo-classes), `assert_html_internal_test.go`.
**Tests:** `TestSelectAllExpectations` (new rows), `TestSelectAllSiblings`, `TestSelectAllBoundsHostilePages` (new rows).
- [ ] Commit `feat(probe): html assertions match sibling combinators and more pseudo-classes`.

## Task 3: SPA help text and docs
**Files:** `web/src/i18n/{en,de}/checks.ts` (`selectorHint`), `docs/upgrades.md` (skew, R8), `docs/BACKLOG.md` (3.90 done).
- [ ] Commit `docs: backlog 3.90 done`.

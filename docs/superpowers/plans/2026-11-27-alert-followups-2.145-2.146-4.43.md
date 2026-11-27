# Alert follow-ups: acknowledgements and escalation steps reach the told audience, closed items survive the cap (backlog 2.145, 2.146, 4.43) — implementation plan

**Goal:** close three follow-ups of 2.144 and 4.8. An acknowledgement of an alert that eased reaches the routes that paged (2.145); a resolve (and an acknowledgement) also reaches the channels of every escalation step that fired (2.146); a capped alert batch keeps its `closed` items (4.43).

**Architecture:** all in `internal/alertroutes`. `owesResolve` (match.go) becomes `owesTheTold` over an event-kind set; the told-routes read returns each told route's fired step positions too, and `resolution.route` (follow_resolve.go) adds those steps' channels to the route's own; `enqueue` (follow.go) caps through a new `capItems`. One sqlc query changes shape; no migration, no proto, API or SPA change.

**Spec:** backlog rows 2.145, 2.146 and 4.43 (`grep -nE "^\| (2\.14[56]|4\.43) " docs/BACKLOG.md`) with the owner's decisions of 2026-11-27: (2.145) `acknowledged` and `ack_voided` reach every route that told about the alert, the 2.144 mechanism; `snoozed`/`unsnoozed` keep matching on the live severity. (2.146) a closed event, and with 2.145 the acknowledgement events, also reach the channels of every escalation step that fired (`alert_escalations` position > 0) for each told route, resolved through `channelsOf` at the tick's instant, unioned and deduplicated with the route's own channels. (4.43) a capped batch keeps `closed` items first, for every channel kind; the 4.8 known-limit note is updated. Pattern: 2.144 (commits 104d01f9, 08f51424).

## Global Constraints
- Layers per `CLAUDE.md`. Tests: `go test -race -timeout 900s ./internal/alertroutes/...` (and `./internal/notify/...` if touched); never `go test ./...`, `make test` or `internal/e2e`. `golangci-lint run ./internal/alertroutes/...` and `gofmt -s -l` clean.
- sqlc via `PATH=$PATH:~/go/bin make sqlc`; no hand edits of `internal/db/gen`. No migration.
- TDD; Conventional Commits, footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Rulings
- **R1 One set, `toldEvents = {closed, acknowledged, ack_voided}`; `owesResolve` becomes `owesTheTold(route, subject)`.** The severity waiver, the peak breakthrough at the window gate and (R3) the step audience all key off it, so the three kinds cannot drift apart. `snoozed`/`unsnoozed` stay out (owner). — Cost if wrong: a kind added to the set by mistake widens its audience; pinned by the match table.
- **R2 The peak breakthrough applies to acknowledgements too.** An ack of a crit page that broke through at 03:00 and eased goes out at 03:10 where the page did; held instead, it would wait for the window, and an alert that closes meanwhile retires its held events unsent. — Cost if wrong: an ack message at night to people the same alert already woke.
- **R3 The step audience is read from the route's current ladder.** For a told route and a kind in `toldEvents`, every position > 0 claimed in `alert_escalations` for (alert, route) is looked up in `rt.Steps` and resolved with `channelsOf` (fixed list or rotation, at `r.now`, decision 8), the construction `fireStep` already uses. A position no longer in the ladder contributes nothing. Not read from the `escalated` event's payload: that holds who was on call when the step fired, and the owner ruled "at the tick's instant like the route's own channels". — Cost if wrong: after a ladder edit mid-incident the resolve reaches the edited step's channels.
- **R4 Steps ride on their route's match and gates.** Step channels are added only to a route that matched (enabled, covers the host, selects the event kind; the severity is waived as in 2.144), and they share that route's mute and window gate — a step has no selections or window of its own. Added whether the route matched through `owesTheTold` or on the live severity: "fired for each told route". — Cost if wrong: a route that does not select `closed` keeps its woken steps uninformed too; that is the route's own setting.
- **R5 Deduplicated twice:** per route (route channels ∪ step channels) and across routes (`Channels`), and a channel hears once per tick anyway (one batch per channel). — Cost if wrong: none; a duplicate would only repeat a channel id in one union.
- **R6 One query replaces `ToldRoutesForAlert`:** `EscalationLedgerForAlert` returns `(route_id, position)` for every row of the alert; `Store.Told` folds it into `Told` (route → fired step positions), keyed only by routes holding a base (position 0), so "told" keeps 2.144's meaning; a step row without a base (unreachable: a step is only claimed after `DueStep` saw a base) tells nobody. Read only for kinds in `toldEvents`, as today for `closed`. — Cost if wrong: none; a query rename has no stored-data effect.
- **R7 `capItems` keeps closed items first, then the rest, each in arrival order, and the kept items keep their arrival order in the message.** Every channel kind (owner). `Count` stays the number before the cut; the batch key stays over every decided event. More than 50 closed items: the first 50 closed are kept. — Cost if wrong: a reader sees a message whose 50 items lean towards resolves while newer opens are summarised as "… and N more".
- **R8 No upgrade entry.** No migration, protocol or API change; the change is who hears an acknowledgement or resolve and which items a capped message lists.

## Review Focus
- Ack after the severity eased reaches the crit route that paged; a snooze after easing does not (`TestAcknowledgementReachesTheRouteThatPagedAfterTheSeverityEased`, `TestSnoozeStillNeedsTheLiveSeverity`, follow_told_test.go; table rows in `TestMatchRoutesToldEventsReachTheRoutesThatTold`).
- An ack held behind a shut window breaks through where the page did (`TestAcknowledgementBreaksThroughWhereThePageDid`).
- Closed and ack reach a fired step's channel, not an unfired one (`TestClosedReachesTheEscalationStepsThatFired`, `TestAcknowledgementReachesTheEscalationStepsThatFired`); a step channel equal to the route's channel hears once (`TestAStepChannelThatIsAlsoTheRoutesHearsOnce`); a rotation step resolves at the tick's instant (`TestARotationStepIsResolvedAtTheTicksInstant`); a step cut from the ladder mid-incident hears nothing, a renumbered one is told at its new position (`TestAStepCutFromTheLadderMidIncidentHearsNothing`, R3).
- The store read: org-scoped, base-keyed, positions per route (`TestToldIsScopedToOrganizationAndBase`, escalate_sql_test.go, replaces `TestToldRoutesIsScopedToOrganizationAndBase`).
- The cap keeps closed items and order (`TestCapItemsKeepsClosedItemsFirst` table, internal; `TestTickCapKeepsClosedItems`, follow_test.go).

## Task 1: Acknowledgements reach the told routes (2.145)
**Files:** `internal/alertroutes/{match.go,match_test.go,follow_resolve.go}`, `internal/alertroutes/follow_told_test.go` (new; the 2.144 tests stay in follow_closed_test.go).
- [ ] Failing table test and follower tests; `toldEvents`, `owesTheTold`; `route` reads the told routes for every kind in the set. Commit `feat(alertroutes): tell the routes that paged about an acknowledgement`.

## Task 2: Escalation steps that fired hear the resolve and the acknowledgement (2.146)
**Files:** `internal/db/queries/alert_escalations.sql` (+ sqlc), `internal/alertroutes/{escalate_sql.go,escalate_sql_test.go,follow_resolve.go,follow_told_test.go}`.
- Interface: `type Told map[uuid.UUID][]int` with `Routes() []uuid.UUID`; `(*Store).Told(ctx, tx, orgID, alertID) (Told, error)` replaces `ToldRoutes`; `(*resolution).audienceOf(ctx, rt Route, fired []int) []uuid.UUID`.
- [ ] Failing store and follower tests; query, store, resolution. Commit `feat(alertroutes): tell the escalation steps that fired when their alert resolves`.

## Task 3: A capped batch keeps its closed items (4.43)
**Files:** `internal/alertroutes/{follow.go,follow_cap_test.go (new, package alertroutes),follow_test.go}`, `internal/notify/presets/pagerduty/pagerduty.go` (comment).
- [ ] Failing tests; `capItems(items []notify.AlertItem, max int) []notify.AlertItem`. Commit `feat(alertroutes): keep closed items when an alert batch is capped`.

## Task 4: Docs
- [ ] v0.4b spec "Resolution" (2.144 paragraph widened to the set and the steps); webhook-presets spec and backlog 4.8 known limit; rows 2.145, 2.146, 4.43 → done. Commit `docs: backlog 2.145, 2.146 and 4.43 done`.

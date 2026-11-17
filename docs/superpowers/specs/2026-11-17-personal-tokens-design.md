# Personal API and assistant tokens — design

Backlog 3.100. Owner's request, 2026-11-17, in the same batch as the lists
and the personal settings area
(`2026-11-17-lists-and-appearance-design.md`).

## Intent

Today every token — an ordinary API token or an assistant (MCP) token —
belongs to one user **and one organization**; it is created in that
organization's settings, and its role can never exceed the user's role
there. The owner wants every user to be able to create **their own
tokens in their personal settings**, not only per organization. A
personal token never has more rights than its user, and may have fewer.

## Decided with the owner

- Personal tokens live under the personal settings (`/account/tokens`,
  the "API tokens" item of the settings navigation from 3.99), for both
  kinds: API and assistant.
- A personal token is valid **only for the organizations chosen on the
  token**, each with its own role cap. It is **not** extended to every
  organization the user belongs to, nor to organizations joined later.
- Rights are always the user's or lower.
- Organization tokens stay exactly as they are.

- **Every member may create** a personal token for their organizations
  (within their own role) — also viewers and operators, who cannot create
  organization tokens today.
- **Per-organization switch** for admins: "Allow personal tokens" in the
  organization's settings, **on by default**. Off: no new grants for that
  organization, and existing grants stop working there at once (they come
  back if it is turned on again, unless an admin revoked them).
- **An admin's revocation sticks**: a grant an admin revoked stays blocked,
  and the block is **per person and organization**: no token of that user
  — the same token, another existing one, a new one, or one created after
  revoking the blocked token — gets a grant for that organization (409
  `grant_blocked`). Only an admin can lift the block (on the
  organization's token page, which keeps listing a blocked grant even after
  its token was revoked); lifting it lifts every blocked grant of that
  user there, after which the owner may add the organization again.
  While the block lasts, the user's other tokens' grants there stop
  working at once as well (and cannot be changed by the owner); after the
  block is lifted they work again, while the blocked token's own grant is
  gone and must be added again by the owner. A blocked grant
  counts toward the token's 50 grants. The block ends with the membership
  (rule 11).
  The organization's token page marks every entry of a blocked person
  ("Blocked (person)"; the API's `owner_blocked`), not only the grant an
  admin revoked, since all of them are refused there. Lifting the block
  works through any of the person's entries and lifts it for the person;
  through an entry of a person who is not blocked it answers 404. An entry
  blocked only through its owner can also be revoked itself, so it stays
  out after the person's block is lifted.

## Rules (designer's calls, check when reading)

1. **Grants.** A personal token carries a list of grants: organization +
   role cap (`viewer`, `operator`, `admin`, `owner` — all organization
   roles, as organization tokens accept today). The cap offered when
   creating or editing is at most the user's role in that organization at
   that moment.
2. **Effective role per request** = the lower of the grant's cap and the
   user's **current** membership role in that organization
   (`tenancy.Lower`, as for organization tokens). No grant for the
   organization, no current membership, or a deactivated user → the
   request is refused as for an invalid token (401/403 as today's token
   paths answer). Losing a membership therefore takes effect at once,
   without touching the token.
3. **Never instance admin.** Like every token today, a personal token
   never reaches instance-admin routes or credential-changing account
   routes (`requireInteractiveSession` already refuses tokens).
4. **Assistant tokens** keep their areas (backlog 2.116/2.124); for a
   personal assistant token the areas are set once on the token and apply
   in every granted organization. They are validated against the token's
   **highest** grant cap; per request the effective role decides, and the
   assistant endpoint lists, per organization, only the tools that
   organization's effective role allows (no tools that would always answer
   403 there).
11. **Membership ends → grant deleted.** Removing a member (or the member
    leaving) deletes their personal-token grants for that organization, so
    rejoining never revives one. Deactivating a user revokes their
    personal tokens like their organization tokens (`RevokeTokensForUser`
    covers both).
12. **Audit**: create, edit and revoke of a personal token are recorded in
    the user's own feed (no organization); every grant change (added, cap
    changed, removed, revoked or unblocked by an admin) is also recorded in
    the affected organization's feed, so its admins see it. Entries made
    with a personal token show the token's name in the organization's feed
    too.
13. **Live streams** (the job log stream) re-check the grant as they
    re-check tokens today, so a revoked grant, a lowered cap or the
    organization switch turned off ends a running stream.
5. **Editing**: name, expiry, the grants (add/remove organizations, change
   a cap within the user's own role) and, for assistant tokens, the areas
   — the same edit model as organization tokens since 2.124. Revoking
   ends the whole token.
6. **Organization admins** see, on their organization's token page, the
   personal tokens that hold a grant for their organization (marked
   "personal", with owner, cap, last use) and can **revoke that grant**
   for their organization — not the whole token, which stays the user's.
   They cannot create or widen grants.
7. **The assistant endpoint**: an organization assistant token keeps
   `/api/mcp`. A personal assistant token names its organization in the
   path, `/api/mcp/o/{org}`; on `/api/mcp` it is accepted only when it has
   exactly one grant, else refused with a message that names the path to
   use. The connect script (`scripts/…` added with 2.116) learns to ask
   for or take the organization.
8. **REST**: the org-scoped API (`/api/v1/orgs/{org}/…`) accepts a
   personal API token when it holds a live grant for `{org}`. The bearer
   middleware runs before the organization is known, so it only
   authenticates the token; `requireOrg` (which already lowers the role to
   a token's cap) looks up the grant for the route's organization and
   refuses without one. `TokenScope` gains what that needs (a personal
   flag and the grant lookup), keeping one organization per request.
9. **Secrets and audit** as today: shown once at creation, stored hashed,
   never in logs or audit summaries. Audit events for create, edit, revoke
   and an admin's grant revocation; the actor of a request made with a
   personal token is recorded as "token" with the token id, as today.
10. **Limits**: at most 50 personal tokens per user; at most 50 grants per
    token.

## Non-goals

- Tokens that act across all organizations implicitly.
- Instance-level (organization-less) API access by token.
- Changing organization tokens.

## Constraints

- Migrations additive; RLS as for the existing token tables (an
  organization admin's view of grants is scoped to their organization).
- The token list pages use the new list component (3.97).
- Tests: token resolution per organization (grant present/absent, cap vs.
  membership, membership lost, user deactivated), admin grant revocation,
  assistant endpoint path rules, the bearer middleware on org routes, and
  e2e for creating a personal token and using it against two
  organizations with different caps.
- User docs: personal tokens on the account pages, the organization
  token page's "personal" entries, the assistant connect instructions.

## Left to the builder

- Storage shape (separate tables for personal tokens and their grants, or
  the existing token table with a nullable organization plus a grants
  table), as long as RLS keeps an organization's view to its own grants
  and the lookup by secret stays one indexed read.
- Secret prefix (reuse today's prefixes or a distinct one for personal
  tokens).

## Notes for the builder (from the builder check)

- Row-level security only knows `pulsix.org_id`. A personal token row with
  no organization is invisible under an organization's scope; the
  organization admin's view (owner, name, last use) and the activity feed's
  token-name join need the grant rows (RLS by organization) to carry or
  expose what they show.
- `scripts/assistant-mcp.sh` stores one token per instance name; with a
  personal assistant token, one registered connection per organization
  sharing the token is fine — the builder's call.
- The organization switch is a new organization setting with its own
  admin route (like `org_assistant_settings`).

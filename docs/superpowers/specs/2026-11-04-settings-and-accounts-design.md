# Settings navigation, account administration and organizations — design

Date: 2026-11-04. Backlog 7.74 (package a). The owner took the decisions in chat on 2026-11-03; they are recorded in the backlog row and repeated here. The host view (backlog 7.65, spec `2026-11-03-host-view-design.md`) is the template: its building blocks (`DetailLayout`, `Section`, `SidebarFacts`, `ListRow`, `RowMenu`, `Switch`) are reused, not rebuilt.

## Intent

The owner tried the settings on a local stack and found them hard to use:

- the tab strip under Settings has grown to ten tabs;
- the shared resources (repositories, job templates and check definitions an instance admin defines for every organization) are edited in dialogs that are too cramped to work in;
- a member cannot be opened or edited, and an administrator has no way to help someone who forgot a password or lost a second factor;
- someone who forgot a password has nowhere to go from the login page;
- there is no way in the GUI to create a second organization, although the API allows it.

Success:

- every setting is one click away in a navigation that groups them;
- shared resources have room to work in;
- an instance admin can get a locked-out person back in without touching the database, and without ever weakening the second factor;
- a person who forgot a password can reset it themselves when mail is configured.

## Goals

### 1. Settings with a sidebar instead of tabs

- `/o/{org}/settings/*` gets a left navigation, about 220 px wide and sticky, replacing `SettingsTabs`. Below about 900 px it collapses into a select at the top. The groups, in order:
  - **Organization:** General (new, see 1a), Members, Activity, API tokens.
  - **Alerting:** Notifications, Alert routes, On-call rotations. Shown to roles with `channels.read`, as today.
  - **Instance:** Users (new, see 3), Organizations (new, see 5), Outgoing mail, Language, Backups, Shared repositories, Shared job templates, Shared check definitions. Shown to instance admins only, as today.
- Every existing settings URL keeps working. The old `/settings/shared` redirects to `/settings/shared/repos`.
- **1a. General:** the organization's name (editable with `org.manage`, through the existing `PATCH /orgs/{org}`), its slug (read-only, with a one-line explanation that it is part of every URL and API token scope), and its creation date.

### 2. Shared resources as pages

- The three kinds become three list pages (`/settings/shared/repos`, `/settings/shared/templates`, `/settings/shared/checks`). Each uses the clickable `ListRow` with a `RowMenu` (Open, Delete…).
- Creating and editing happen on a page of their own (`…/new`, `…/{id}`). The page has the form in full width, a sticky footer with Save and Cancel, and an unsaved-changes guard when leaving.
- The job template's extra-vars editor keeps today's `SchemaBuilder` for now. Backlog 7.75 replaces it with the central variable editor; this item must not build a second one.
- The backend does not change.

### 3. Users and the account view (instance admins only)

- **Who:** every account action below is for **instance admins only**. An org owner or admin cannot use them, because an account spans organizations (owner decision).
- **Users page** (`/settings/users`, Instance group): every account on the instance, searchable by name or e-mail and paged. Each row shows:
  - name and e-mail;
  - an instance-admin badge;
  - a state pill: Active, Deactivated, "Must change password", "No second factor", or "Invite pending" (an account created by an invitation that was never accepted, so it has no password);
  - the number of organizations.
- A row opens the **account page** (`/settings/users/{userId}`), built with `DetailLayout`:
  - **Sidebar:** the name, e-mail, state, instance-admin flag, created date, and the date the account last signed in, or "never". This comes from a new `users.last_sign_in_at`, set when a session becomes complete (second factor passed, or a passkey sign-in). Session rows cannot be used: the reaper deletes them a week after they expire, and a password-only session that never passed its second factor is not a sign-in.
  - **Sections:**
    1. **Organizations:** the memberships and roles. Each links to that organization's member page if the admin is a member there. This section is read-only; roles are changed per organization.
    2. **Sign-in:** the second factors (authenticator app yes/no, number of passkeys, recovery codes left) and the active session count.
    3. **Actions:** see below.
- **Member page** (`/settings/members/{userId}`, Organization group): opened by clicking a row on Members.
  - It shows the member's role in this organization. A role with `members.write` can change it or remove the member; the existing rules apply, including the last owner and "only an owner removes an owner". This replaces today's role dialog.
  - For an instance admin, the page also shows the account's Sign-in and Actions sections, the same components as on the account page.
  - Anyone else sees no account actions.
- **Account actions:** each needs a fresh second factor from the admin (the existing step-up, `requireFreshFactor`), is confirmed in a dialog, writes an audit entry (actor: the admin; object: the account; org: none), and is refused on the admin's own account. For their own account an admin uses Account security.

| Action | Effect |
|---|---|
| **Send password reset link** | Only when outbound mail is configured; otherwise the action is disabled with the reason. It e-mails the person a reset link (as in 4). The current password keeps working until the link is used. |
| **Set temporary password** | Generates a random password and shows it to the admin **once**, with a copy button and the note to pass it on by a separate channel. It sets the account to "must change password", the temporary password expires after 24 h, and every session of the account ends. |
| **Reset second factor** | Removes the authenticator secret, the recovery codes and every passkey, and ends every session. At the next sign-in the person enters the password and must then enrol a new factor (the existing enrolment flow). The confirm dialog says that until then anyone with the password can enrol a factor, so the admin should combine this with a temporary password when the password is not certain. |
| **End all sessions** | Signs the account out everywhere. API tokens are not touched; they are managed per organization. |
| **Deactivate / Reactivate** | Deactivate is the existing `POST /admin/users/{id}/deactivate` (ends sessions and revokes tokens; refuses the last active instance admin). It now also needs the step-up and refuses the admin's own account, like the other actions. No SPA code calls it yet, so tightening the route is safe. Reactivate clears `disabled_at`. Revoked tokens stay revoked. |

- **Invite pending:** an account without a password allows only Deactivate. Forgot password sends it nothing. Neither path may give a password to an account whose owner never accepted the invitation.
- When outbound mail is configured, the person gets a short notice for a password set by an admin, a reset second factor, or a deactivation. It is best-effort: a mail failure does not undo the action, and the admin sees a warning.

### 4. "Forgot password" and changing one's own password

- The login page gets a **Forgot password?** link to `/forgot-password`.
- **Mail configured:** the page asks for the e-mail address and always answers the same way: "If an account exists for this address, we sent a link". It never reveals whether an account exists or is deactivated. The request is rate-limited per client and per address by a **new limiter that counts every request**. The existing auth limiter charges only 4xx answers, and this route always answers 2xx. A deactivated account, or an account with an invite still pending, gets no mail.
- **Mail not configured:** the page says that self-service reset is not available and to ask an instance administrator. The login page can tell which case applies from a public, unauthenticated flag.
- **The link** (`/reset-password#token=…`):
  - The token is in the URL **fragment**, which the browser never sends to the server, so the request logger (chi's `middleware.Logger`, `internal/api/api.go`) never sees it. The page reads it from the fragment and sends it in the request body.
  - It is single-use, expires after 60 minutes, and is stored only as a hash. A newer link voids older ones; a completed reset voids all of them.
  - The page asks for the new password twice and requires at least 12 characters. That floor is one shared constant for every new password: the setup wizard, reset, change, the temporary password, and invite acceptance, which allows 8 today (`minInvitePasswordLen`, `internal/tenancy/service.go`) and moves to 12.
  - The reset sets the password, clears "must change password", and ends every session. It does **not** sign the person in. They go to the login page and sign in with the new password **and their second factor**: a reset never skips it (owner decision).
- **Must change password:**
  - It is a flag on the **account**, checked on every request, and it fails closed. It applies however the person signed in, a passkey included, so a temporary password never stays live beside a session that skipped it.
  - After a sign-in with a temporary password and a completed second factor (or a completed enrolment, if the factor was reset too), the session may only change the password, read `/auth/me` and log out. Everything else answers 403 with a distinct code.
  - The SPA routes such a session to a change-password screen without the app shell, the way it handles enrolment today.
  - The change needs the temporary password and the new one. The new password must differ from the temporary one.
- **Change password on Account security:** a new Password card that takes the current and the new password and needs a fresh second factor. All of the account's other sessions end; the current one stays.

### 5. Organizations

- **Who:** only instance admins may create an organization (owner decision 2026-11-04). `POST /orgs` today admits every MFA-complete user. It now requires `requireInstanceAdmin` and answers 403 to everyone else, with an API test. The creator still becomes the owner.
- **Create:** for instance admins, "+ New organization" at the end of the org switcher opens a small dialog (Name, and Slug via the existing `SlugField`) that calls `POST /orgs`. Afterwards the SPA switches to the new organization's hosts page. Everyone else does not see the entry.
- **Instance › Organizations** (`/settings/organizations`): for instance admins, every organization on the instance with its slug, member count and creation date, plus the same create button.
  - An admin who is not a member of an organization sees it listed but cannot open it. This item adds no "join as admin".

## Non-goals

- Moving other views to new layouts beyond the settings, shared resources and member/account pages.
- The variable editor (backlog 7.75); the shared template page keeps today's `SchemaBuilder`.
- Listing or ending one's **own** individual sessions on Account security (only the admin's "end all").
- Deleting accounts or organizations.
- Showing instance-level audit entries (org: none) in the GUI. They are recorded and reachable through the database and the backup, as today.
- Changing a person's e-mail address or display name as an admin.
- SSO or external identity providers.

## Anti-goals

- Any path that signs someone in without their second factor: not a reset link, a temporary password, or an admin action.
- A forgot-password answer that reveals whether an address has an account.
- A password, temporary password or reset token in a log line, an audit entry, an error message, or any URL the server receives. That is why the token sits in the fragment (see 4).
- An org owner or admin resetting the password or second factor of someone in their organization.
- Clipped cards or tab strips that wrap. The `Card` fix from 7.65 applies.

## Constraints

- **Layering and process:**
  - CLAUDE.md layering: handler → `internal/auth` or `internal/tenancy` service → store, with interfaces and fakes.
  - Additive migrations only: e.g. `users.must_change_password`, `users.temp_password_expires_at`, and a `password_reset_tokens` table (`id`, `user_id`, `token_hash`, `created_at`, `expires_at`, `used_at`).
  - TDD; Go coverage for auth ≥ 90 %.
- **Routes:**
  - The new admin routes live under `/api/v1/admin/users…`, are cookie-session only (no bearer token, like the deactivate route), and need `requireInstanceAdmin` plus the step-up for actions.
  - The public routes (`/auth/password/forgot`, `/auth/password/reset`, and the mail-available flag) are unauthenticated and answer in constant shape. Forgot uses the new per-client and per-address limiter. Reset uses the existing auth limiter, since a bad token answers 4xx.
- **Responses and logging:**
  - The temporary password is returned only in that one response, with `Cache-Control: no-store`.
- **Mails:** use the existing `mail` sender and the instance's public URL (`PULSIX_PUBLIC_URL`) for links. Each mail has an en and de text, chosen by the person's effective locale.
- **Copy and docs:**
  - en and de i18n for every new string.
  - End-user docs in noix-docs (branch `pulsix`): settings navigation, users and account actions (admin guide), forgot or change password (user guide), and organizations.
- **Coordination with 7.75:** the shared job template page leaves a clearly bounded slot for the variable editor.

## Owner decisions (2026-11-03, verbatim in substance)

1. Account actions are for **instance admins only**.
2. Password reset in **both** ways: e-mail link and temporary password that must be changed.
3. "Forgot password" on the login page. Without mail configured, the page tells the person to ask an admin.
4. A reset never skips the second factor. A lost factor is recovered with the recovery codes; otherwise an instance admin resets it and the person enrols again at the next sign-in.
5. Shared resources are created and edited on pages, not in dialogs.
6. Settings get a sidebar instead of tabs.
7. (2026-11-04) Only instance admins create organizations.

## Controller's calls (the owner may overturn them)

- An instance-wide **Users** page and an **Organizations** list. Without them, an instance admin could only reach accounts in organizations they belong to.
- An org **General** page (rename), since the sidebar makes room for it and the API already exists.
- Temporary password lifetime 24 h; reset link 60 min.
- A password floor of 12 characters everywhere, invite acceptance included (it was 8).
- The reset token goes in the URL fragment.
- "Must change password" is a flag on the account and applies to passkey sign-ins too.
- A new `users.last_sign_in_at` column.
- Accounts with an invite pending: Deactivate is their only action, and forgot password sends them nothing.
- The deactivate route gets the step-up and refuses one's own account.
- Account actions are refused on one's own account.
- "End all sessions" leaves API tokens alone.
- Notice mails to the person for admin actions, best-effort.
- The member role dialog is replaced by the member page.

## Left to the builder

- Component names; the exact layout of the account page within `DetailLayout`; the breakpoints.
- Whether the mail-available flag is its own route or part of an existing public status route.
- Where the "must change password" check sits (middleware, or the shared `require*` helpers), as long as it reads the account flag, fails closed, and is tested at the HTTP layer.
- The invite link carries its token in the path (`/invite/{token}`), which the request logger records. That is a separate finding for the backlog, not part of this item.
- The order of tasks, as long as the backend for a screen lands before or with that screen.

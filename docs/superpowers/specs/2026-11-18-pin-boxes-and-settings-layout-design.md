# Four-digit PIN boxes and one settings layout — design

Backlog 3.106 (PIN) and 3.107 (settings layout). Owner's request,
2026-11-18, approved on the mockup
https://claude.ai/artifact/8DGeKhsJQH1yNom6MPezKL ("ja gerne so machen").

## Intent

1. The lock PIN should feel like a classic PIN: always four digits, four
   boxes side by side, both where it is entered (lock screen) and where it
   is set (first sign-in prompt, first lock, account security).
2. The settings pages are "too chaotic and unclear": two-column grids of
   cards with uneven heights, buttons in every position and colour, help
   text in varying places, and two different navigations (the account area
   switches the main sidebar, organization settings add a second nav column
   inside the page). They get one layout pattern and one navigation.

## PIN

Decided with the owner (mockup):

- **New PINs are exactly 4 digits.** The server refuses anything else when a
  PIN is set (`^[0-9]{4}$`; today `^[0-9]{4,8}$`).
- **PIN input = four boxes.** The digits show as dots; the active box has the
  accent ring and a caret. Typing advances, Backspace goes back, pasting
  fills, non-digits are dropped.
- **Lock screen:** four boxes, checked as soon as the fourth digit is in (no
  Unlock button). A wrong PIN: the boxes shake briefly (none with reduced
  motion), clear, focus returns to the first box, and the message names the
  attempts left (as today). Sign out stays.
- **Setting a PIN:** two rows, "New PIN" and "Repeat PIN". When the first row
  is full the focus moves to the second. When the second row is full and
  matches, the PIN is saved; when it does not match, the second row clears,
  shakes, and says so. No separate submit button; Cancel / Later stay where
  they are today. The same form serves the first sign-in prompt
  (`/account/lock-pin`), the dialog at the first lock, and the account page.
- **PINs set under v0.1.0-rc.4** may have 5 to 8 digits. They keep working:
  the server records the length of every PIN set from now on, and a PIN set
  before (no recorded length) is "legacy". For a legacy PIN the lock screen
  shows today's single field with an Unlock button and the note that the PIN
  is from an older version and a new 4-digit one can be set under Security.
  The account security page marks a legacy PIN ("older PIN — set a new
  4-digit one").

Builder's calls:

- One real `<input>` (type password, `inputmode="numeric"`,
  `autocomplete="off"` on unlock, `new-password` when setting, `maxlength=4`)
  lies transparent over the four boxes, so paste, password managers, the
  label and screen readers behave as for one field. The boxes are drawn from
  its value. A shared presentational component in `web/src/ui`.
- Storage: an additive column `users.lock_pin_digits smallint NULL`, set to 4
  by every PIN set from now on, cleared with the PIN. `/auth/me` gains
  `lock_pin_legacy` (true when a PIN is set and `lock_pin_digits` is null).
  Unlock keeps accepting 4 to 8 digits, so a legacy PIN still unlocks.
- The setup hint says "4 digits. Avoid 1234 or 0000." The server does not
  refuse weak PINs (the lock gives five tries, then ends the session).

## Settings layout

Decided with the owner (mockup):

- **One navigation for all settings.** The account area's way of switching
  the main sidebar is used for every settings page: "← Back to <org>", then
  the groups **My account** (Security, Appearance, Profile, API tokens, My
  activity), **Organization · <name>** (General, Members, Activity, API
  tokens, Secrets), **Alerting** (Notifications, Alert routes, Rotations;
  operator and above, as today), **Instance** (Users, Organizations, Email,
  Language, Backups, Shared repositories, Shared job templates, Shared check
  definitions; instance admins, as today). The second nav column inside
  organization settings goes away. URLs stay as they are.
- **Card pages are one column**, at most 880 px wide. No grid of cards.
- **Every card** has a header (title, one sentence, optional status badge on
  the right) and then either rows or a free body. A **row** has the label and
  its help on the left and the control on the right; on narrow screens it
  stacks.
- **Saving:**
  - A switch that stands alone applies immediately, with a short toast.
  - A form saves through the card's **footer bar**: Save on the right,
    disabled until something changed. While there are changes the bar shows
    "Unsaved changes" and a Discard button, and leaving the page asks first
    (the existing unsaved-changes guard).
  - A switch inside a form is a form field (it saves with the form).
- **Bigger flows open a dialog** instead of growing the page: change
  password, set up the authenticator app, regenerate recovery codes, add a
  passkey, set or change the PIN.
- **At most one primary (accent) button per card**; everything else is the
  default or ghost button.
- **Page header** (title, one sentence, optional badge and actions) is the
  same component on every settings page, list pages included. List pages
  keep their full width.

Scope (every page under the settings navigation):

- Card pages, reworked: account Security, Appearance, Profile; organization
  General (name/slug, monitoring, assistant access, personal tokens);
  Notifications' digest card; instance Email, Language, Backups; the member
  page and the instance account page (their detail layout with a sidebar
  becomes one column of cards).
- List pages: only the shared page header.
- The shared resource editors (form pages) stay as they are.

Builder's calls:

- New presentational primitives in `web/src/ui`: page header, settings card
  (keeps `data-card`, which the e2e layout check reads), settings row, card
  save bar. The shared `Card` stays for other pages.
- Backups' one form across two cards becomes a save bar per card. Each card
  saves its own fields on top of the last saved values of the other card, so
  an unsaved draft in one card is not sent by the other.
- Second-factor enrolment (`/account/enrol`, which reuses the security page)
  keeps working: it shows only the sign-in card, and its flows open the same
  dialogs.
- On the account pages the organization groups link to the organization the
  person came from (the same one "Back" goes to).

## Non-goals

- New settings, new features, other pages outside the settings navigation.
- A light theme, new colours or fonts.
- Changing the shared resource editors or the DataList.

## Constraints

- Migrations additive. The backup round-trip fixture covers the new column
  (run `internal/backup` tests).
- Every changed page keeps its tests' intent; tests move to the new save model
  (Save disabled until dirty, Discard).
- e2e: settings navigation, unsaved guard, PIN set/lock/unlock with boxes,
  `expectNoClippedCards`.
- User docs: the lock PIN (4 digits, boxes, older PINs), the settings
  navigation and the save model.

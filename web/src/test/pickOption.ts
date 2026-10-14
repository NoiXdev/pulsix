import userEvent from '@testing-library/user-event';
import type { screen as rtlScreen, within as rtlWithin } from '@testing-library/react';

type Scope = typeof rtlScreen | ReturnType<typeof rtlWithin>;

/**
 * Chooses an option from a ui/Select (v0.4g). It replaced
 * `userEvent.selectOptions`, which drives a native select element and
 * stopped working when the last one was removed.
 *
 * `option` takes either the visible label or `{ value }`. The value form
 * exists because every call this helper replaced passed an option's value,
 * and because two options may legitimately share a label — two credentials
 * named "deploy" of different kinds, two hosts of the same name in
 * different zones.
 *
 * This sets up its own `userEvent` instance rather than taking one as a
 * parameter. Many call sites already hold a `user` from their own
 * `userEvent.setup()` for other interactions in the same test; a second
 * instance alongside it was checked deliberately (pickOption.test.tsx's
 * "coexists with a call site's own userEvent instance" case) rather than
 * assumed safe, since two trackers of keyboard/pointer state sharing one
 * document is exactly the kind of thing that works until it doesn't. It
 * holds up: each click here is a full press-and-release against a
 * specific target, so there is no drag or held key for a second tracker to
 * lose track of, and the outer instance's own typing keeps working
 * untouched after this one runs.
 */
export async function pickOption(scope: Scope, label: string, option: string | { value: string }): Promise<void> {
  const user = userEvent.setup();
  await user.click(scope.getByLabelText(label));
  const rows: HTMLElement[] = scope.getAllByRole('option');
  const wanted =
    typeof option === 'string'
      ? rows.find((r) => r.textContent?.includes(option))
      : rows.find((r) => r.getAttribute('data-value') === option.value);
  if (!wanted) {
    const seen = rows.map((r) => `${r.getAttribute('data-value')}=${r.textContent}`).join(', ');
    throw new Error(
      `pickOption: no option ${JSON.stringify(option)} in "${label}". The list held: ${seen || '(nothing)'}`,
    );
  }
  await user.click(wanted);
}

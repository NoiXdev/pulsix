import { screen, within } from '@testing-library/react';
import type { UserEvent } from '@testing-library/user-event';

/**
 * pickDateTime drives a DateTimePicker the way a person does: opens the
 * calendar behind the labelled field, picks a day of the month shown (the
 * 15th by default, which never doubles as an outside day) and types the
 * time. The month shown is the current one, so tests that assert a full
 * value pin the clock first. The time box is found by its own name, which
 * a DateTimePicker with an id takes from the field's label: "Until Time".
 */
export async function pickDateTime(
  user: UserEvent,
  scope: ReturnType<typeof within> | typeof screen,
  label: string,
  time: string,
  dayOfMonth = 15,
): Promise<void> {
  const trigger = scope.getByLabelText(label);
  await user.click(trigger);
  const cell = within(screen.getByRole('grid')).getByText(String(dayOfMonth));
  await user.click(cell.closest('button') ?? cell);
  const field = scope.getByRole('textbox', { name: `${label} Time` });
  await user.clear(field);
  await user.type(field, time);
  await user.tab();
}

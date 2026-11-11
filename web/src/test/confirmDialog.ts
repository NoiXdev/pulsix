import { screen, within } from '@testing-library/react';
import type { UserEvent } from '@testing-library/user-event';

/** The confirmation dialog, once it is on screen (default title). */
export function findConfirmDialog(): Promise<HTMLElement> {
  return screen.findByRole('dialog', { name: 'Are you sure?' });
}

/** Waits for the confirmation dialog and presses its confirm button, whatever
 * its label (it is always the last button). Resolves with the question asked. */
export async function acceptConfirm(user: UserEvent): Promise<string> {
  const dialog = await findConfirmDialog();
  const message = dialog.querySelector('p')?.textContent ?? '';
  const buttons = within(dialog).getAllByRole('button');
  await user.click(buttons[buttons.length - 1]);
  return message;
}

/** Waits for the confirmation dialog and presses Cancel. Resolves with the
 * question asked. */
export async function declineConfirm(user: UserEvent): Promise<string> {
  const dialog = await findConfirmDialog();
  const message = dialog.querySelector('p')?.textContent ?? '';
  await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));
  return message;
}

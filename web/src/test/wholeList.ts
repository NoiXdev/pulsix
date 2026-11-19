import { screen, within } from '@testing-library/react';
import type { UserEvent } from '@testing-library/user-event';

/** The first cell's text of every body row of the shown list, in order. */
export function firstCells(): string[] {
  return Array.from(document.querySelectorAll('[data-list-row]')).map((row) => row.querySelector('[role="cell"]')?.textContent ?? '');
}

/** Fixture names `<prefix>1` … `<prefix>n`, in the server's descending order, so a sort has work to do. */
export function numbered(prefix: string, n: number): string[] {
  return Array.from({ length: n }, (_, i) => `${prefix}${n - i}`);
}

/** Presses a sortable column header: once for ascending. */
export async function sortBy(user: UserEvent, label: string): Promise<void> {
  await user.click(await screen.findByRole('button', { name: label }));
}

/** Hides one column through the Columns menu. */
export async function hideColumn(user: UserEvent, label: string): Promise<void> {
  await user.click(screen.getByRole('button', { name: /^Columns/ }));
  await user.click(within(await screen.findByRole('dialog', { name: 'Columns' })).getByRole('checkbox', { name: label }));
}

/** Opens the "…" menu of the row named `name`. */
export async function openRowMenu(user: UserEvent, name: string): Promise<void> {
  await user.click(await screen.findByRole('button', { name: `Actions for ${name}` }));
}

/** Opens the "…" menu of the row named `name` and picks one entry. */
export async function chooseRowAction(user: UserEvent, name: string, action: string): Promise<void> {
  await openRowMenu(user, name);
  await user.click(await screen.findByRole('menuitem', { name: action }));
}

/** Opens the row named `name` through its name cell's button (a row whose
 * item opens in a dialog). */
export async function openRow(user: UserEvent, name: string): Promise<void> {
  await user.click(await screen.findByRole('button', { name }));
}

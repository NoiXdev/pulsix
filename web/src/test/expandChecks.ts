import { screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

/**
 * expandChecks opens every collapsed row of the host page's Checks table
 * once the checks have loaded, so a test can look at the readings and their
 * actions without clicking each row first. A table of rows that are all
 * collapsed is what the page shows; tests about the collapsed state do not
 * call this.
 *
 * The table is expected by default and its absence fails the test. A test
 * whose host has no checks (or no access, or a failed read) passes
 * `{ expectTable: false }`: then it opens whatever table is there, if any,
 * without waiting for one.
 */
export async function expandChecks({ expectTable = true }: { expectTable?: boolean } = {}): Promise<void> {
  const table = expectTable ? await screen.findByRole('table') : screen.queryByRole('table');
  if (!table) return;
  const user = userEvent.setup();
  for (const button of within(table).queryAllByRole('button', { expanded: false })) {
    await user.click(button);
  }
}

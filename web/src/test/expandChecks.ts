import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

/**
 * expandChecks opens every collapsed row of the host page's Checks table
 * once the checks have loaded, so a test can look at the readings and their
 * actions without clicking each row first. A table of rows that are all
 * collapsed is what the page shows; tests about the collapsed state do not
 * call this.
 */
export async function expandChecks(): Promise<void> {
  // No table means no checks (or no access, or a failed read): nothing to open.
  try {
    await screen.findByRole('table', {}, { timeout: 800 });
  } catch {
    return;
  }
  const user = userEvent.setup();
  for (const button of screen.queryAllByRole('button', { expanded: false })) {
    if (button.getAttribute('aria-controls')?.startsWith('check-detail-')) await user.click(button);
  }
}

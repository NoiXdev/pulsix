import { describe, expect, it } from 'vitest';
import { useState } from 'react';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Dialog } from './dialog.tsx';

/**
 * v0.4g Task 2: quick-create opens a create dialog on top of the dialog
 * whose picker asked for it. The HTML specification keeps modal dialogs on
 * a stack, so this is supposed to work — but eleven call sites depend on
 * it, so it is pinned rather than assumed.
 *
 * jsdom does no layout, so this proves only the state machine — both
 * dialogs' native `open` attribute, and that closing the inner one leaves
 * the outer one untouched — never that the inner dialog actually paints
 * above the outer one on screen. Visual stacking is what the Playwright
 * suite (web/e2e/*.spec.ts) is for; it runs against a real browser.
 *
 * The assertions check the `open` attribute directly rather than relying on
 * `toBeVisible()` alone: `Dialog` never unmounts its content (`open` only
 * drives `showModal()`/`close()`), so a visibility check that happened to be
 * satisfied vacuously — say, if a future change made `toBeVisible()` ignore
 * `<dialog>` — would still read green. The attribute is what the component
 * actually controls.
 */
describe('a dialog opened from inside a dialog', () => {
  function Nested() {
    const [inner, setInner] = useState(false);
    return (
      <Dialog open onClose={() => {}} title="Outer">
        <button type="button" onClick={() => setInner(true)}>Open inner</button>
        <Dialog open={inner} onClose={() => setInner(false)} title="Inner">
          <button type="button" onClick={() => setInner(false)}>Close inner</button>
        </Dialog>
      </Dialog>
    );
  }

  it('shows both open, and closing the inner one leaves the outer one open', async () => {
    const user = userEvent.setup();
    render(<Nested />);

    const outer = screen.getByRole('dialog', { name: 'Outer' });
    expect(outer).toHaveAttribute('open');
    expect(outer).toBeVisible();

    await user.click(screen.getByRole('button', { name: 'Open inner' }));
    const inner = screen.getByRole('dialog', { name: 'Inner' });
    expect(inner).toHaveAttribute('open');
    expect(inner).toBeVisible();
    // The outer dialog is still up underneath — opening the inner one must
    // not have closed it.
    expect(outer).toHaveAttribute('open');
    expect(outer).toBeVisible();

    await user.click(screen.getByRole('button', { name: 'Close inner' }));
    expect(inner).not.toHaveAttribute('open');
    expect(inner).not.toBeVisible();
    expect(outer).toHaveAttribute('open');
    expect(outer).toBeVisible();
  });
});

import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Dialog } from './dialog.tsx';

/**
 * Backlog 1.46. A native <dialog> answers one Escape with TWO events:
 * `cancel`, and then `close` once the cancel is not prevented. Dialog
 * wired the same handler to both, so a single key press called the call
 * site's closer twice.
 *
 * Nothing broke, because every closer in the app happens to be
 * idempotent — `setOpen(false)` and the like. That is exactly why it
 * needs a test rather than a comment: the first call site to put a real
 * side effect in its closer, an API call or a toast, would have
 * double-fired silently and the bug would have surfaced as a duplicate
 * request nobody could place.
 */
describe('Escape on a dialog', () => {
  it('calls the close handler exactly once', async () => {
    const onClose = vi.fn();
    render(
      <Dialog open onClose={onClose} title="Add credential">
        <p>body</p>
      </Dialog>,
    );
    await userEvent.keyboard('{Escape}');
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('still closes when the parent drives it, not only on Escape', async () => {
    // `close` is kept rather than `cancel` because it is the event the
    // programmatic el.close() fires too; `cancel` is Escape's alone. A
    // parent that stops passing `open` must still reach the handler.
    const onClose = vi.fn();
    const { rerender } = render(
      <Dialog open onClose={onClose} title="Add credential">
        <p>body</p>
      </Dialog>,
    );
    expect(screen.getByRole('dialog')).toHaveAttribute('open');
    rerender(
      <Dialog open={false} onClose={onClose} title="Add credential">
        <p>body</p>
      </Dialog>,
    );
    expect(screen.getByRole('dialog', { hidden: true })).not.toHaveAttribute('open');
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  // Backlog 7.74: a dialog whose request is in flight must not be
  // dismissed, or the answer (a one-time password) is lost unseen.
  it('stays open and does not call the close handler while not dismissible', async () => {
    const onClose = vi.fn();
    const { rerender } = render(
      <Dialog open onClose={onClose} title="Add credential" dismissible={false}>
        <p>body</p>
      </Dialog>,
    );
    await userEvent.keyboard('{Escape}');
    expect(screen.getByRole('dialog')).toHaveAttribute('open');
    expect(onClose).not.toHaveBeenCalled();

    rerender(
      <Dialog open onClose={onClose} title="Add credential">
        <p>body</p>
      </Dialog>,
    );
    await userEvent.keyboard('{Escape}');
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  // A browser may still close a dialog that refuses Escape (a repeated
  // Escape without user activation). A persistent one is still meant to be
  // open, so it comes straight back instead of being left closed under `open`.
  it('re-opens itself when the browser closes it while persistent', async () => {
    const onClose = vi.fn();
    const { rerender } = render(
      <Dialog open onClose={onClose} title="Recovery codes" persistent>
        <p>codes</p>
      </Dialog>,
    );
    const el = screen.getByRole('dialog') as HTMLDialogElement;
    await userEvent.keyboard('{Escape}');
    expect(el.open).toBe(true);
    el.close();
    expect(el.open).toBe(true);
    expect(onClose).not.toHaveBeenCalled();

    // The owner closing it on purpose still closes it.
    rerender(
      <Dialog open={false} onClose={onClose} title="Recovery codes" persistent>
        <p>codes</p>
      </Dialog>,
    );
    expect(el.open).toBe(false);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  // Unlike a persistent one, a dialog that only refuses Escape while a
  // request runs is let go when the browser closes it anyway: its owner
  // keeps the request (features/users/useAccountActionRunner.ts).
  it('lets a merely non-dismissible dialog close when the browser closes it', () => {
    const onClose = vi.fn();
    render(
      <Dialog open onClose={onClose} title="Add credential" dismissible={false}>
        <p>body</p>
      </Dialog>,
    );
    const el = screen.getByRole('dialog') as HTMLDialogElement;
    el.close();
    expect(el.open).toBe(false);
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});

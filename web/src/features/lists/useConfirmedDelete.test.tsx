import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ConfirmProvider } from '../../ui/confirm.tsx';
import { ToastProvider } from '../../ui/toast.tsx';
import { acceptConfirm, declineConfirm } from '../../test/confirmDialog.ts';
import { useConfirmedDelete } from './useConfirmedDelete.ts';

interface Row {
  id: string;
  name: string;
}
const ROWS: Row[] = [
  { id: 'a', name: 'alpha' },
  { id: 'b', name: 'beta' },
];

function Harness({ remove, onDone }: { remove: (id: string) => Promise<unknown>; onDone?: (row: Row) => void }) {
  const del = useConfirmedDelete<Row>({ message: (r) => `Delete ${r.name}?`, failed: 'Fallback failure', remove, onDone });
  return (
    <ul>
      {ROWS.map((r) => (
        <li key={r.id}>
          <button type="button" disabled={del.pending.has(r.id)} onClick={() => void del.run(r)}>
            {`delete ${r.name}`}
          </button>
        </li>
      ))}
    </ul>
  );
}

function setup(remove: (id: string) => Promise<unknown>, onDone?: (row: Row) => void) {
  render(
    <ToastProvider>
      <ConfirmProvider>
        <Harness remove={remove} onDone={onDone} />
      </ConfirmProvider>
    </ToastProvider>,
  );
}

describe('useConfirmedDelete', () => {
  it('sends no request when the confirmation is cancelled', async () => {
    const user = userEvent.setup();
    const remove = vi.fn().mockResolvedValue(undefined);
    setup(remove);
    await user.click(screen.getByRole('button', { name: 'delete alpha' }));
    expect(await declineConfirm(user)).toBe('Delete alpha?');
    expect(remove).not.toHaveBeenCalled();
  });

  it('deletes the confirmed row by id', async () => {
    const user = userEvent.setup();
    const remove = vi.fn().mockResolvedValue(undefined);
    setup(remove);
    await user.click(screen.getByRole('button', { name: 'delete beta' }));
    await acceptConfirm(user);
    await waitFor(() => expect(remove).toHaveBeenCalledWith('b'));
  });

  it('reports the deleted row once the request succeeded', async () => {
    const user = userEvent.setup();
    const onDone = vi.fn();
    setup(vi.fn().mockResolvedValue(undefined), onDone);
    await user.click(screen.getByRole('button', { name: 'delete beta' }));
    await acceptConfirm(user);
    await waitFor(() => expect(onDone).toHaveBeenCalledWith(ROWS[1]));
  });

  it('does not report a row whose request failed', async () => {
    const user = userEvent.setup();
    const onDone = vi.fn();
    setup(vi.fn().mockRejectedValue(new Error('nope')), onDone);
    await user.click(screen.getByRole('button', { name: 'delete alpha' }));
    await acceptConfirm(user);
    expect(await screen.findByText('nope')).toBeInTheDocument();
    expect(onDone).not.toHaveBeenCalled();
  });

  it("toasts the server's message when the request fails", async () => {
    const user = userEvent.setup();
    setup(vi.fn().mockRejectedValue(new Error('still in use by 2 checks')));
    await user.click(screen.getByRole('button', { name: 'delete alpha' }));
    await acceptConfirm(user);
    expect(await screen.findByText('still in use by 2 checks')).toBeInTheDocument();
  });

  it('toasts the fallback when the failure carries no message', async () => {
    const user = userEvent.setup();
    setup(vi.fn().mockRejectedValue('boom'));
    await user.click(screen.getByRole('button', { name: 'delete alpha' }));
    await acceptConfirm(user);
    expect(await screen.findByText('Fallback failure')).toBeInTheDocument();
  });

  it("disables only the pending row's entry while its request runs", async () => {
    const user = userEvent.setup();
    let finish: () => void = () => undefined;
    setup(() => new Promise<void>((resolve) => (finish = resolve)));
    await user.click(screen.getByRole('button', { name: 'delete alpha' }));
    await acceptConfirm(user);
    await waitFor(() => expect(screen.getByRole('button', { name: 'delete alpha' })).toBeDisabled());
    expect(screen.getByRole('button', { name: 'delete beta' })).toBeEnabled();
    finish();
    await waitFor(() => expect(screen.getByRole('button', { name: 'delete alpha' })).toBeEnabled());
  });
});

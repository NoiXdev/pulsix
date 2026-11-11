import { describe, expect, it } from 'vitest';
import { useState } from 'react';
import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, Link, RouterProvider } from '@tanstack/react-router';
import { ConfirmProvider } from './confirm.tsx';
import { useConfirm, type ConfirmOptions } from './confirmContext.ts';
import { Dialog } from './dialog.tsx';
import { FormPage } from './form-page.tsx';

/** A button that asks and prints the answer. */
function Asker({ options, name = 'Ask' }: { options: ConfirmOptions; name?: string }) {
  const confirm = useConfirm();
  const [answer, setAnswer] = useState('none');
  return (
    <>
      <button type="button" onClick={() => void confirm(options).then((a) => setAnswer(String(a)))}>
        {name}
      </button>
      <output aria-label={`${name} answer`}>{answer}</output>
    </>
  );
}

function renderAsker(options: ConfirmOptions = { message: 'Delete it?' }) {
  return render(
    <ConfirmProvider>
      <Asker options={options} />
    </ConfirmProvider>,
  );
}

describe('useConfirm', () => {
  it('shows the message under the default title', async () => {
    renderAsker();
    await userEvent.click(screen.getByRole('button', { name: 'Ask' }));
    const dialog = screen.getByRole('dialog', { name: 'Are you sure?' });
    expect(dialog).toHaveAttribute('open');
    expect(dialog).toHaveTextContent('Delete it?');
  });

  it('takes a title and a confirm label', async () => {
    renderAsker({ message: 'm', title: 'Delete host', confirmLabel: 'Delete' });
    await userEvent.click(screen.getByRole('button', { name: 'Ask' }));
    expect(screen.getByRole('dialog', { name: 'Delete host' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Delete' })).toBeInTheDocument();
  });

  it('resolves true on Confirm', async () => {
    renderAsker();
    await userEvent.click(screen.getByRole('button', { name: 'Ask' }));
    await userEvent.click(screen.getByRole('button', { name: 'Confirm' }));
    expect(await screen.findByText('true')).toBeInTheDocument();
    expect(screen.getByRole('dialog', { hidden: true })).not.toHaveAttribute('open');
  });

  it('resolves false on Cancel', async () => {
    renderAsker();
    await userEvent.click(screen.getByRole('button', { name: 'Ask' }));
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(await screen.findByText('false')).toBeInTheDocument();
  });

  it('resolves false on Escape', async () => {
    renderAsker();
    await userEvent.click(screen.getByRole('button', { name: 'Ask' }));
    await userEvent.keyboard('{Escape}');
    expect(await screen.findByText('false')).toBeInTheDocument();
    expect(screen.getByRole('dialog', { hidden: true })).not.toHaveAttribute('open');
  });

  it('puts focus on Cancel, so Enter does not confirm, and gives it back to the opener', async () => {
    renderAsker({ message: 'm', destructive: true });
    const opener = screen.getByRole('button', { name: 'Ask' });
    await userEvent.click(opener);
    expect(screen.getByRole('button', { name: 'Cancel' })).toHaveFocus();
    await userEvent.keyboard('{Enter}');
    expect(await screen.findByText('false')).toBeInTheDocument();
    expect(opener).toHaveFocus();
  });

  it('gives the focus back after Confirm too', async () => {
    renderAsker();
    const opener = screen.getByRole('button', { name: 'Ask' });
    await userEvent.click(opener);
    await userEvent.click(screen.getByRole('button', { name: 'Confirm' }));
    expect(opener).toHaveFocus();
  });

  it('uses the destructive variant for a destructive confirm and primary otherwise', async () => {
    const { unmount } = renderAsker({ message: 'm', destructive: true });
    await userEvent.click(screen.getByRole('button', { name: 'Ask' }));
    expect(screen.getByRole('button', { name: 'Confirm' }).className).toContain('text-destructive');
    unmount();
    renderAsker({ message: 'm' });
    await userEvent.click(screen.getByRole('button', { name: 'Ask' }));
    const confirm = screen.getByRole('button', { name: 'Confirm' });
    expect(confirm.className).toContain('bg-primary');
    expect(confirm.className).not.toContain('text-destructive');
  });

  it('queues a second question behind the first and resolves both', async () => {
    function Two() {
      const confirm = useConfirm();
      const [out, setOut] = useState<string[]>([]);
      return (
        <>
          <button
            type="button"
            onClick={() => {
              void confirm({ message: 'first' }).then((a) => setOut((o) => [...o, `1:${a}`]));
              void confirm({ message: 'second' }).then((a) => setOut((o) => [...o, `2:${a}`]));
            }}
          >
            Ask two
          </button>
          <output aria-label="out">{out.join(',')}</output>
        </>
      );
    }
    render(
      <ConfirmProvider>
        <Two />
      </ConfirmProvider>,
    );
    await userEvent.click(screen.getByRole('button', { name: 'Ask two' }));
    expect(screen.getByRole('dialog')).toHaveTextContent('first');
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(screen.getByRole('dialog')).toHaveAttribute('open');
    expect(screen.getByRole('dialog')).toHaveTextContent('second');
    expect(screen.getByRole('button', { name: 'Cancel' })).toHaveFocus();
    await userEvent.click(screen.getByRole('button', { name: 'Confirm' }));
    expect(screen.getByLabelText('out')).toHaveTextContent('1:false,2:true');
  });

  it('does not let the close of an answered question settle the next one', async () => {
    function Twice() {
      const confirm = useConfirm();
      const [out, setOut] = useState<string[]>([]);
      return (
        <>
          <button
            type="button"
            onClick={() => {
              void (async () => {
                const first = await confirm({ message: 'first' });
                setOut((o) => [...o, `1:${first}`]);
                // Asked straight from the continuation, before the previous
                // dialog's asynchronous `close` event has arrived.
                const second = await confirm({ message: 'second' });
                setOut((o) => [...o, `2:${second}`]);
              })();
            }}
          >
            Ask twice
          </button>
          <output aria-label="out">{out.join(',')}</output>
        </>
      );
    }
    render(
      <ConfirmProvider>
        <Twice />
      </ConfirmProvider>,
    );
    await userEvent.click(screen.getByRole('button', { name: 'Ask twice' }));
    await userEvent.click(screen.getByRole('button', { name: 'Confirm' }));
    // Let any stale close event land.
    await act(async () => {
      await new Promise((r) => setTimeout(r, 20));
    });
    expect(screen.getByLabelText('out')).toHaveTextContent('1:true');
    expect(screen.getByLabelText('out')).not.toHaveTextContent('2:');
    expect(screen.getByRole('dialog')).toHaveAttribute('open');
    expect(screen.getByRole('dialog')).toHaveTextContent('second');
    await userEvent.click(screen.getByRole('button', { name: 'Confirm' }));
    expect(screen.getByLabelText('out')).toHaveTextContent('1:true,2:true');
  });

  it('gives the focus back to the first opener after a queue of questions', async () => {
    function Two() {
      const confirm = useConfirm();
      return (
        <>
          <button type="button" onClick={() => void confirm({ message: 'first' })}>
            Ask first
          </button>
          <button type="button" onClick={() => void confirm({ message: 'second' })}>
            Ask second
          </button>
        </>
      );
    }
    render(
      <ConfirmProvider>
        <Two />
      </ConfirmProvider>,
    );
    const opener = screen.getByRole('button', { name: 'Ask first' });
    await userEvent.click(opener);
    // Queued from outside, while the first dialog is open: its opener is
    // whatever has focus then, i.e. the first dialog's Cancel.
    act(() => screen.getByRole('button', { name: 'Ask second', hidden: true }).click());
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(opener).toHaveFocus();
  });

  it('resolves pending questions with false when the provider unmounts', async () => {
    let result: Promise<boolean> | undefined;
    function Grab() {
      const confirm = useConfirm();
      return (
        <button type="button" onClick={() => (result = confirm({ message: 'm' }))}>
          Ask
        </button>
      );
    }
    const { unmount } = render(
      <ConfirmProvider>
        <Grab />
      </ConfirmProvider>,
    );
    await userEvent.click(screen.getByRole('button', { name: 'Ask' }));
    unmount();
    await expect(result).resolves.toBe(false);
  });

  it('opens above a dialog and Escape closes only the confirm', async () => {
    render(
      <ConfirmProvider>
        <Dialog open onClose={() => {}} title="Outer">
          <Asker options={{ message: 'Really?' }} />
        </Dialog>
      </ConfirmProvider>,
    );
    await userEvent.click(screen.getByRole('button', { name: 'Ask' }));
    expect(screen.getByRole('dialog', { name: 'Are you sure?' })).toHaveAttribute('open');
    await userEvent.keyboard('{Escape}');
    expect(await screen.findByText('false')).toBeInTheDocument();
    expect(screen.getByRole('dialog', { name: 'Outer' })).toHaveAttribute('open');
    // The confirm is the only dialog that is closed now.
    expect(screen.getAllByRole('dialog', { hidden: true }).filter((d) => !d.hasAttribute('open'))).toHaveLength(1);
  });

  it('throws without a provider', () => {
    expect(() => render(<Asker options={{ message: 'm' }} />)).toThrow(/ConfirmProvider/);
  });
});

describe('useUnsavedGuard through the confirm dialog', () => {
  function renderForm(dirty: boolean) {
    const rootRoute = createRootRoute();
    const other = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org/settings/shared/repos', component: () => <div>the other page</div> });
    const form = createRoute({
      getParentRoute: () => rootRoute,
      path: '/o/$org/settings/shared/repos/new',
      component: () => (
        <FormPage title="Form" backTo={{ to: '/o/$org/settings/shared/repos', params: { org: 'acme' } }} onSubmit={() => true} submitting={false} dirty={dirty}>
          <Link to="/o/$org/settings/shared/repos" params={{ org: 'acme' }}>Go elsewhere</Link>
        </FormPage>
      ),
    });
    const router = createRouter({
      routeTree: rootRoute.addChildren([other, form]),
      history: createMemoryHistory({ initialEntries: ['/o/acme/settings/shared/repos/new'] }),
    });
    render(
      <ConfirmProvider>
        <RouterProvider router={router} />
      </ConfirmProvider>,
    );
    return router;
  }

  it('asks in the dialog; Cancel stays on the page', async () => {
    const router = renderForm(true);
    await userEvent.click(await screen.findByRole('link', { name: 'Go elsewhere' }));
    expect(await screen.findByRole('dialog', { name: 'Are you sure?' })).toHaveTextContent('Leave this page?');
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    await waitFor(() => expect(screen.getByRole('dialog', { hidden: true })).not.toHaveAttribute('open'));
    await act(async () => {
      await new Promise((r) => setTimeout(r, 20));
    });
    expect(router.state.location.pathname).toBe('/o/acme/settings/shared/repos/new');
    expect(screen.queryByText('the other page')).not.toBeInTheDocument();
  });

  it('asks in the dialog; Confirm leaves for the intended target', async () => {
    const router = renderForm(true);
    await userEvent.click(await screen.findByRole('link', { name: 'Go elsewhere' }));
    await userEvent.click(await screen.findByRole('button', { name: 'Confirm' }));
    expect(await screen.findByText('the other page')).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/o/acme/settings/shared/repos');
  });

  it('does not ask for a clean form', async () => {
    renderForm(false);
    await userEvent.click(await screen.findByRole('link', { name: 'Go elsewhere' }));
    expect(await screen.findByText('the other page')).toBeInTheDocument();
  });
});

import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ListRow, ListRowName, RowMenu, RowMenuItem, RowMenuLink } from './list-row.tsx';

function renderRow(onSelect = vi.fn(), onOther = vi.fn()) {
  const rootRoute = createRootRoute();
  const list = createRoute({
    getParentRoute: () => rootRoute,
    path: '/hosts',
    component: () => (
      <ListRow
        to="/o/$org/hosts/$id"
        params={{ org: 'acme', id: 'h1' }}
        columns="1fr 1fr 40px"
        label="Open web-01"
        menu={
          <RowMenu label="Actions for web-01">
            <RowMenuItem onSelect={onSelect}>Restart</RowMenuItem>
            <RowMenuItem onSelect={onOther} destructive>
              Delete
            </RowMenuItem>
          </RowMenu>
        }
      >
        <span>web-01</span>
        <span>10.0.0.1 online</span>
        <span />
      </ListRow>
    ),
  });
  const detail = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org/hosts/$id', component: () => <div>detail page</div> });
  const router = createRouter({
    routeTree: rootRoute.addChildren([list, detail]),
    history: createMemoryHistory({ initialEntries: ['/hosts'] }),
  });
  render(<RouterProvider router={router} />);
  return router;
}

describe('ListRow', () => {
  it('is a link to the given href, named by its label', async () => {
    renderRow();
    const link = await screen.findByRole('link', { name: 'Open web-01' });
    expect(link).toHaveAttribute('href', '/o/acme/hosts/h1');
    expect(link).toHaveTextContent('web-01');
    expect(link).toHaveTextContent('online');
    expect(link).toHaveStyle({ gridTemplateColumns: '1fr 1fr 40px' });
  });

  it('carries a hash into its href', async () => {
    const rootRoute = createRootRoute();
    const list = createRoute({
      getParentRoute: () => rootRoute,
      path: '/',
      component: () => (
        <ListRow to="/o/$org/hosts/$id" params={{ org: 'acme', id: 'h1' }} hash="connection" columns="1fr" label="Open web-01">
          <span>web-01</span>
        </ListRow>
      ),
    });
    const detail = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org/hosts/$id', component: () => <div /> });
    render(<RouterProvider router={createRouter({ routeTree: rootRoute.addChildren([list, detail]), history: createMemoryHistory() })} />);
    expect((await screen.findByRole('link')).getAttribute('href')).toMatch(/#connection$/);
  });

  it('is named by a visually hidden label and described by its cells', async () => {
    renderRow();
    const link = await screen.findByRole('link', { name: 'Open web-01', description: /10\.0\.0\.1/ });
    expect(link).not.toHaveAttribute('aria-label');
    expect(link.querySelector('.sr-only')).toHaveTextContent('Open web-01');
  });

  // The label already names the link; the visible name cell must not be read
  // a second time as part of the description.
  it('leaves the name cell out of the description', async () => {
    const rootRoute = createRootRoute();
    const list = createRoute({
      getParentRoute: () => rootRoute,
      path: '/',
      component: () => (
        <ListRow to="/o/$org/hosts/$id" params={{ org: 'acme', id: 'h1' }} columns="1fr 1fr" label="web-01">
          <ListRowName className="font-medium">web-01</ListRowName>
          <span>10.0.0.1</span>
        </ListRow>
      ),
    });
    const detail = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org/hosts/$id', component: () => <div /> });
    render(<RouterProvider router={createRouter({ routeTree: rootRoute.addChildren([list, detail]), history: createMemoryHistory() })} />);

    const link = await screen.findByRole('link', { name: 'web-01' });
    expect(link).toHaveAccessibleDescription('10.0.0.1');
    expect(link).toHaveTextContent('web-01');
    expect(within(link).getByText('web-01', { selector: 'span.font-medium' })).toHaveAttribute('aria-hidden', 'true');
  });

  it('highlights on hover with the page background so it shows inside a Card', async () => {
    renderRow();
    const row = (await screen.findByRole('link', { name: 'Open web-01' })).parentElement as HTMLElement;
    expect(row.className).toContain('hover:bg-background');
    expect(row.className).toContain('focus-within:bg-background');
    expect(row.className).not.toContain('hover:bg-card');
    expect(row.className).not.toContain('focus-within:bg-card');
  });

  it('keeps the menu outside the anchor', async () => {
    renderRow();
    const link = await screen.findByRole('link', { name: 'Open web-01' });
    const trigger = screen.getByRole('button', { name: 'Actions for web-01' });
    expect(link).not.toContainElement(trigger);
  });

  it('runs a menu item without navigating', async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    const router = renderRow(onSelect);
    await user.click(await screen.findByRole('button', { name: 'Actions for web-01' }));
    expect(router.state.location.pathname).toBe('/hosts');
    await user.click(screen.getByRole('menuitem', { name: 'Restart' }));
    expect(onSelect).toHaveBeenCalledTimes(1);
    expect(router.state.location.pathname).toBe('/hosts');
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
  });

  it('navigates when the row itself is clicked', async () => {
    const user = userEvent.setup();
    const router = renderRow();
    await user.click(await screen.findByRole('link', { name: 'Open web-01' }));
    expect(router.state.location.pathname).toBe('/o/acme/hosts/h1');
  });
});

describe('RowMenu', () => {
  it('renders the menu in a portal outside the row, and still closes on Escape and outside click', async () => {
    const user = userEvent.setup();
    renderRow();
    const row = (await screen.findByRole('link', { name: 'Open web-01' })).parentElement as HTMLElement;
    const trigger = screen.getByRole('button', { name: 'Actions for web-01' });
    await user.click(trigger);
    const menu = screen.getByRole('menu');
    expect(row).not.toContainElement(menu);
    expect(menu.parentElement).toBe(document.body);
    expect(menu).toHaveStyle({ position: 'fixed' });
    expect(within(menu).getAllByRole('menuitem')).toHaveLength(2);

    await user.keyboard('{Escape}');
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();

    await user.click(trigger);
    expect(screen.getByRole('menu')).toBeInTheDocument();
    await user.click(document.body);
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
  });

  it('does not close on a mousedown inside the portalled menu', async () => {
    const user = userEvent.setup();
    renderRow();
    await user.click(await screen.findByRole('button', { name: 'Actions for web-01' }));
    fireEvent.mouseDown(screen.getByRole('menu'));
    expect(screen.getByRole('menu')).toBeInTheDocument();
  });

  /** mockTriggerAt makes every element but the menu report `top` as its
   * position, so a test can move the trigger as a scroll would. */
  function mockTriggerAt(top: { current: number }) {
    return vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
      const r =
        this.getAttribute('role') === 'menu'
          ? { top: 0, bottom: 80, left: 0, right: 160, width: 160, height: 80 }
          : { top: top.current, bottom: top.current + 20, left: 600, right: 628, width: 28, height: 20 };
      return { x: r.left, y: r.top, toJSON: () => r, ...r } as DOMRect;
    });
  }

  it('closes on a scroll that moves its trigger, and on resize', async () => {
    const user = userEvent.setup();
    const top = { current: 100 };
    const spy = mockTriggerAt(top);
    try {
      renderRow();
      const trigger = await screen.findByRole('button', { name: 'Actions for web-01' });
      await user.click(trigger);
      top.current = 60;
      fireEvent.scroll(document);
      expect(screen.queryByRole('menu')).not.toBeInTheDocument();
      await user.click(trigger);
      fireEvent(window, new Event('resize'));
      expect(screen.queryByRole('menu')).not.toBeInTheDocument();
    } finally {
      spy.mockRestore();
    }
  });

  // A browser delivers scroll events on its next frame, so the scroll that
  // brought the trigger into view (a click near the viewport's edge, or the
  // end of a wheel scroll) can arrive after the click has opened the menu.
  // The trigger has not moved since, so the menu is still where it belongs.
  it('stays open on a scroll that leaves its trigger where it was', async () => {
    const user = userEvent.setup();
    const top = { current: 100 };
    const spy = mockTriggerAt(top);
    try {
      renderRow();
      await user.click(await screen.findByRole('button', { name: 'Actions for web-01' }));
      fireEvent.scroll(document);
      expect(screen.getByRole('menu')).toBeInTheDocument();
      await user.click(screen.getByRole('menuitem', { name: 'Restart' }));
      expect(screen.queryByRole('menu')).not.toBeInTheDocument();
    } finally {
      spy.mockRestore();
    }
  });

  it('opens below the trigger, and flips above it near the viewport bottom', async () => {
    const user = userEvent.setup();
    let triggerRect = { top: 100, bottom: 120, left: 600, right: 628 };
    const spy = vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
      const r = this.getAttribute('role') === 'menu' ? { top: 0, bottom: 80, left: 0, right: 160, width: 160, height: 80 } : { width: 28, height: 20, ...triggerRect };
      return { x: r.left, y: r.top, toJSON: () => r, ...r } as DOMRect;
    });
    try {
      renderRow();
      const trigger = await screen.findByRole('button', { name: 'Actions for web-01' });
      await user.click(trigger);
      // Right-aligned to the trigger, 4px under it.
      expect(screen.getByRole('menu')).toHaveStyle({ top: '124px', left: '468px' });
      await user.keyboard('{Escape}');

      triggerRect = { top: window.innerHeight - 30, bottom: window.innerHeight - 10, left: 600, right: 628 };
      await user.click(trigger);
      expect(screen.getByRole('menu')).toHaveStyle({ top: `${window.innerHeight - 30 - 4 - 80}px` });
    } finally {
      spy.mockRestore();
    }
  });

  it('returns focus to the trigger after an item is selected', async () => {
    const user = userEvent.setup();
    renderRow();
    const trigger = await screen.findByRole('button', { name: 'Actions for web-01' });
    await user.click(trigger);
    await user.click(screen.getByRole('menuitem', { name: 'Restart' }));
    expect(trigger).toHaveFocus();
  });

  it('opens as a menu and exposes its state on the trigger', async () => {
    const user = userEvent.setup();
    renderRow();
    const trigger = await screen.findByRole('button', { name: 'Actions for web-01' });
    expect(trigger).toHaveAttribute('aria-haspopup', 'menu');
    expect(trigger).toHaveAttribute('aria-expanded', 'false');
    await user.click(trigger);
    expect(trigger).toHaveAttribute('aria-expanded', 'true');
    expect(screen.getByRole('menu')).toBeInTheDocument();
    expect(screen.getAllByRole('menuitem')).toHaveLength(2);
  });

  it('closes on Escape and returns focus to the trigger', async () => {
    const user = userEvent.setup();
    renderRow();
    const trigger = await screen.findByRole('button', { name: 'Actions for web-01' });
    await user.click(trigger);
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
  });

  it('closes on Tab and returns focus to the trigger, not to the end of the page', async () => {
    const user = userEvent.setup();
    renderRow();
    const trigger = await screen.findByRole('button', { name: 'Actions for web-01' });
    await user.click(trigger);
    expect(screen.getAllByRole('menuitem')[0]).toHaveFocus();
    await user.tab();
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
  });

  it('closes on an outside click', async () => {
    const user = userEvent.setup();
    renderRow();
    await user.click(await screen.findByRole('button', { name: 'Actions for web-01' }));
    await user.click(document.body);
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
  });

  it('toggles closed when the trigger is clicked again', async () => {
    const user = userEvent.setup();
    renderRow();
    const trigger = await screen.findByRole('button', { name: 'Actions for web-01' });
    await user.click(trigger);
    await user.click(trigger);
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
  });

  it('focuses the first item and moves with the arrow keys, wrapping', async () => {
    const user = userEvent.setup();
    renderRow();
    await user.click(await screen.findByRole('button', { name: 'Actions for web-01' }));
    const [restart, del] = screen.getAllByRole('menuitem');
    expect(restart).toHaveFocus();
    await user.keyboard('{ArrowDown}');
    expect(del).toHaveFocus();
    await user.keyboard('{ArrowDown}');
    expect(restart).toHaveFocus();
    await user.keyboard('{ArrowUp}');
    expect(del).toHaveFocus();
    await user.keyboard('{Home}');
    expect(restart).toHaveFocus();
    await user.keyboard('{End}');
    expect(del).toHaveFocus();
  });

  it('opens with the keyboard and selects with Enter', async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    const router = renderRow(onSelect);
    const trigger = await screen.findByRole('button', { name: 'Actions for web-01' });
    trigger.focus();
    await user.keyboard('{Enter}');
    expect(screen.getByRole('menu')).toBeInTheDocument();
    await user.keyboard('{Enter}');
    expect(onSelect).toHaveBeenCalledTimes(1);
    expect(router.state.location.pathname).toBe('/hosts');
  });
});

describe('RowMenuItem disabled', () => {
  function renderMenu(onSelect = vi.fn()) {
    render(
      <RowMenu label="Actions">
        <RowMenuItem onSelect={vi.fn()}>First</RowMenuItem>
        <RowMenuItem onSelect={onSelect} disabled>
          Busy
        </RowMenuItem>
        <RowMenuItem onSelect={vi.fn()}>Last</RowMenuItem>
      </RowMenu>,
    );
  }

  it('is announced as disabled and ignores a click, leaving the menu open', async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    renderMenu(onSelect);
    await user.click(screen.getByRole('button', { name: 'Actions' }));
    const busy = screen.getByRole('menuitem', { name: 'Busy' });
    expect(busy).toHaveAttribute('aria-disabled', 'true');
    await user.click(busy);
    expect(onSelect).not.toHaveBeenCalled();
    expect(screen.getByRole('menu')).toBeInTheDocument();
  });

  it('stays reachable by the arrow keys and ignores Enter', async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    renderMenu(onSelect);
    await user.click(screen.getByRole('button', { name: 'Actions' }));
    await user.keyboard('{ArrowDown}');
    expect(screen.getByRole('menuitem', { name: 'Busy' })).toHaveFocus();
    await user.keyboard('{Enter}');
    expect(onSelect).not.toHaveBeenCalled();
    await user.keyboard('{ArrowDown}');
    expect(screen.getByRole('menuitem', { name: 'Last' })).toHaveFocus();
  });

  it('an enabled item has no aria-disabled', async () => {
    const user = userEvent.setup();
    renderMenu();
    await user.click(screen.getByRole('button', { name: 'Actions' }));
    expect(screen.getByRole('menuitem', { name: 'First' })).not.toHaveAttribute('aria-disabled');
  });
});

describe('RowMenuLink', () => {
  it('is a menu item that keeps its href and download, and closes the menu when picked', async () => {
    const user = userEvent.setup();
    // jsdom does not navigate; keep it from logging that it cannot. Capture
    // phase: the link's own handler stops the event's propagation.
    const stopNavigation = (e: MouseEvent) => e.preventDefault();
    document.addEventListener('click', stopNavigation, true);
    try {
      render(
        <RowMenu label="Actions">
          <RowMenuLink href="/api/v1/backups/b1.tar/download" download="b1.tar">
            Download
          </RowMenuLink>
          <RowMenuItem onSelect={vi.fn()}>Delete…</RowMenuItem>
        </RowMenu>,
      );
      const trigger = screen.getByRole('button', { name: 'Actions' });
      await user.click(trigger);
      const link = screen.getByRole('menuitem', { name: 'Download' });
      expect(link.tagName).toBe('A');
      expect(link).toHaveAttribute('href', '/api/v1/backups/b1.tar/download');
      expect(link).toHaveAttribute('download', 'b1.tar');
      await user.click(link);
      expect(screen.queryByRole('menu')).not.toBeInTheDocument();
      expect(trigger).toHaveFocus();
    } finally {
      document.removeEventListener('click', stopNavigation, true);
    }
  });
});

describe('RowMenu and the browser menu', () => {
  // RowMenuLink exists so the browser treats a download as a link: its
  // own right-click menu ("Save link as…") must stay reachable.
  it('leaves a right-click on a RowMenuLink to the browser', async () => {
    render(
      <RowMenu label="Actions">
        <RowMenuLink href="/api/v1/backups/b1.tar/download" download="b1.tar">
          Download
        </RowMenuLink>
      </RowMenu>,
    );
    await userEvent.setup().click(screen.getByRole('button', { name: 'Actions' }));
    expect(fireEvent.contextMenu(screen.getByRole('menuitem', { name: 'Download' }), { button: 2 })).toBe(true);
  });

  it('leaves a right-click on an item of a "…" menu to the browser', async () => {
    render(
      <RowMenu label="Actions">
        <RowMenuItem onSelect={vi.fn()}>Restart</RowMenuItem>
      </RowMenu>,
    );
    await userEvent.setup().click(screen.getByRole('button', { name: 'Actions' }));
    expect(fireEvent.contextMenu(screen.getByRole('menuitem', { name: 'Restart' }), { button: 2 })).toBe(true);
  });
});

describe('RowMenuItem hint', () => {
  it('shows the hint in the item and describes the item by it, leaving the name alone', async () => {
    const user = userEvent.setup();
    render(
      <RowMenu label="Actions">
        <RowMenuItem onSelect={vi.fn()} disabled hint="Expired tokens can't be edited">
          Edit
        </RowMenuItem>
        <RowMenuItem onSelect={vi.fn()}>Revoke</RowMenuItem>
      </RowMenu>,
    );
    await user.click(screen.getByRole('button', { name: 'Actions' }));
    const item = screen.getByRole('menuitem', { name: 'Edit' });
    expect(screen.getByText("Expired tokens can't be edited")).toBeVisible();
    expect(item).toHaveAccessibleDescription("Expired tokens can't be edited");
    expect(screen.getByRole('menuitem', { name: 'Revoke' })).not.toHaveAttribute('aria-describedby');
  });
});

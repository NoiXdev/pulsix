import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ListRow, ListRowName } from './list-row.tsx';

describe('ListRowName', () => {
  it('ListRowName passes its style', async () => {
    const rootRoute = createRootRoute();
    const list = createRoute({
      getParentRoute: () => rootRoute,
      path: '/',
      component: () => (
        <ListRow to="/o/$org/hosts/$id" params={{ org: 'acme', id: 'h1' }} columns="1fr" label="web-01">
          <ListRowName style={{ paddingLeft: 16 }}>web-01</ListRowName>
        </ListRow>
      ),
    });
    const detail = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org/hosts/$id', component: () => <div /> });
    render(<RouterProvider router={createRouter({ routeTree: rootRoute.addChildren([list, detail]), history: createMemoryHistory() })} />);

    const link = await screen.findByRole('link', { name: 'web-01' });
    expect(within(link).getByText('web-01', { selector: '[aria-hidden="true"]' })).toHaveStyle({ paddingLeft: '16px' });
  });
});

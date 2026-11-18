import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ToastProvider } from './toast.tsx';
import { JsonViewer, type JsonViewerProps } from './json-viewer.tsx';

const DOC = {
  all: { children: ['web', 'db'] },
  web: { vars: { env: 'prod', port: 8080, tls: true, proxy: null, db_password: '********' } },
  empty: {},
};

const masked = (v: unknown) => v === '********';

function renderViewer(props: Partial<JsonViewerProps> = {}) {
  return render(
    <ToastProvider>
      <JsonViewer value={DOC} label="Inventory JSON" isMasked={masked} {...props} />
    </ToastProvider>,
  );
}

const region = () => screen.getByRole('region', { name: 'Inventory JSON' });
const token = (text: string) => within(region()).getByText(text, { selector: '[data-token]' });

describe('JsonViewer', () => {
  it('colours keys, strings, numbers, booleans, null and masked secrets', () => {
    renderViewer();
    expect(token('"env"')).toHaveAttribute('data-token', 'key');
    expect(token('"prod"')).toHaveAttribute('data-token', 'string');
    expect(token('8080')).toHaveAttribute('data-token', 'number');
    expect(token('true')).toHaveAttribute('data-token', 'boolean');
    expect(token('null')).toHaveAttribute('data-token', 'null');
    expect(token('"********"')).toHaveAttribute('data-token', 'masked');
    expect(token('"********"')).toHaveClass('text-warn');
    expect(token('"********"')).toHaveAttribute('title', 'masked secret');
    // Each kind has a colour of its own.
    const classes = ['"env"', '"prod"', '8080', 'true', 'null', '"********"'].map((t) => token(t).className);
    expect(new Set(classes).size).toBe(classes.length);
  });

  it('numbers the lines', () => {
    const { container } = renderViewer();
    const numbers = [...container.querySelectorAll('[data-line-number]')].map((n) => n.textContent);
    // The pretty-printed document has as many lines as the viewer shows.
    expect(numbers).toEqual(JSON.stringify(DOC, null, 2).split('\n').map((_, i) => String(i + 1)));
    expect(container.querySelector('[data-line-number]')).toHaveAttribute('aria-hidden', 'true');
  });

  it('collapses and expands an object and an array, saying how much is inside', async () => {
    const user = userEvent.setup();
    renderViewer();
    const vars = screen.getByRole('button', { name: 'Expand or collapse web.vars' });
    expect(vars).toHaveAttribute('aria-expanded', 'true');
    await user.click(vars);
    expect(vars).toHaveAttribute('aria-expanded', 'false');
    expect(within(region()).queryByText('"env"')).not.toBeInTheDocument();
    expect(within(region()).getByText('5 keys')).toBeInTheDocument();
    expect(within(region()).getByText('{ … }')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Expand or collapse all.children' }));
    expect(within(region()).getByText('2 items')).toBeInTheDocument();
    expect(within(region()).getByText('[ … ]')).toBeInTheDocument();
    expect(within(region()).queryByText('"db"')).not.toBeInTheDocument();

    await user.click(vars);
    expect(token('"env"')).toBeInTheDocument();
    // An empty object has nothing to fold.
    expect(screen.queryByRole('button', { name: 'Expand or collapse empty' })).not.toBeInTheDocument();
  });

  it('starts with the given paths collapsed', () => {
    renderViewer({ initiallyCollapsed: ['web'] });
    expect(screen.getByRole('button', { name: 'Expand or collapse web' })).toHaveAttribute('aria-expanded', 'false');
    expect(within(region()).getByText('1 key')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Expand or collapse web.vars' })).not.toBeInTheDocument();
  });

  it('collapses everything below the top level and expands it all again', async () => {
    const user = userEvent.setup();
    renderViewer();
    await user.click(screen.getByRole('button', { name: 'Collapse all' }));
    expect(screen.getByRole('button', { name: 'Expand or collapse document' })).toHaveAttribute('aria-expanded', 'true');
    expect(screen.getByRole('button', { name: 'Expand or collapse all' })).toHaveAttribute('aria-expanded', 'false');
    expect(screen.getByRole('button', { name: 'Expand or collapse web' })).toHaveAttribute('aria-expanded', 'false');
    expect(within(region()).queryByText('"vars"')).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Expand all' }));
    for (const b of within(region()).getAllByRole('button', { name: /^Expand or collapse/ })) expect(b).toHaveAttribute('aria-expanded', 'true');
    expect(token('"env"')).toBeInTheDocument();
  });

  it('copies the pretty-printed JSON, masked values as shown', async () => {
    const user = userEvent.setup();
    renderViewer({ initiallyCollapsed: ['web'] });
    await user.click(screen.getByRole('button', { name: 'Copy' }));
    // Collapsing is only a view: the whole document is copied.
    expect(await navigator.clipboard.readText()).toBe(JSON.stringify(DOC, null, 2));
    expect(await screen.findByText('JSON copied')).toBeInTheDocument();
  });

  it('marks values masked by path and copies them as shown', async () => {
    const user = userEvent.setup();
    render(
      <ToastProvider>
        <JsonViewer value={{ token: 'abc', nested: { token: 'def' } }} label="Doc" isMasked={(_, path) => path.endsWith('token')} />
      </ToastProvider>,
    );
    const shown = within(screen.getByRole('region', { name: 'Doc' }));
    expect(shown.getAllByText('"abc"', { selector: '[data-token="masked"]' })).toHaveLength(1);
    await user.click(screen.getByRole('button', { name: 'Copy' }));
    expect(JSON.parse(await navigator.clipboard.readText())).toEqual({ token: 'abc', nested: { token: 'def' } });
  });

  it('renders a scalar document and an empty one', () => {
    const { rerender } = render(
      <ToastProvider>
        <JsonViewer value="plain" label="Doc" />
      </ToastProvider>,
    );
    expect(screen.getByText('"plain"', { selector: '[data-token]' })).toHaveAttribute('data-token', 'string');
    rerender(
      <ToastProvider>
        <JsonViewer value={[]} label="Doc" />
      </ToastProvider>,
    );
    expect(screen.getByText('[]')).toBeInTheDocument();
  });
});

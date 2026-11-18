import { describe, expect, it, vi } from 'vitest';
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

  // The dotted path of the props is ambiguous ("" is the document and an
  // empty key; "a.b" a dotted key and a nested one): lines and folds are
  // told apart by their key segments instead.
  it('keeps an empty key and a dotted key apart from the paths they spell', async () => {
    const user = userEvent.setup();
    const errors = vi.spyOn(console, 'error').mockImplementation(() => {});
    const doc = { '': { x: 1 }, 'a.b': { c: 1 }, a: { b: { c: 2 } } };
    render(
      <ToastProvider>
        <JsonViewer value={doc} label="Doc" />
      </ToastProvider>,
    );
    const shown = within(screen.getByRole('region', { name: 'Doc' }));
    expect(shown.getAllByText('"c"', { selector: '[data-token]' })).toHaveLength(2);
    // Folding the empty key leaves the document open; a key a dotted path
    // cannot spell is named in brackets.
    const empty = shown.getByRole('button', { name: 'Expand or collapse [""]' });
    await user.click(empty);
    expect(empty).toHaveAttribute('aria-expanded', 'false');
    expect(shown.getByRole('button', { name: 'Expand or collapse document' })).toHaveAttribute('aria-expanded', 'true');
    // Folding the dotted key leaves the nested a.b open.
    const dotted = shown.getByRole('button', { name: 'Expand or collapse ["a.b"]' });
    const nested = shown.getByRole('button', { name: 'Expand or collapse a.b' });
    await user.click(dotted);
    expect(dotted).toHaveAttribute('aria-expanded', 'false');
    expect(nested).toHaveAttribute('aria-expanded', 'true');
    expect(shown.getByText('2', { selector: '[data-token]' })).toBeInTheDocument();
    expect(errors).not.toHaveBeenCalled();
    errors.mockRestore();
  });

  it('renders {"": 1} as one key', () => {
    const errors = vi.spyOn(console, 'error').mockImplementation(() => {});
    render(
      <ToastProvider>
        <JsonViewer value={{ '': 1, 'a.b': 1, a: { b: 2 } }} label="Doc" />
      </ToastProvider>,
    );
    const shown = within(screen.getByRole('region', { name: 'Doc' }));
    expect(shown.getByText('""', { selector: '[data-token]' })).toHaveAttribute('data-token', 'key');
    expect(shown.getByText('"a.b"', { selector: '[data-token]' })).toBeInTheDocument();
    expect(shown.getByText('2', { selector: '[data-token]' })).toBeInTheDocument();
    expect(errors).not.toHaveBeenCalled();
    errors.mockRestore();
  });

  it('has no toolbar role: its buttons are plain tab stops', () => {
    renderViewer();
    expect(screen.queryByRole('toolbar')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Collapse all' })).toBeInTheDocument();
  });
});

import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { AssertionTable } from './AssertionTable.tsx';

const COLUMNS = { name: 'Name', expression: 'Assertion', severity: 'Severity', status: 'Status', actual: 'Seen', age: 'Last result' };

describe('AssertionTable', () => {
  it('renders one row per assertion, cells as worded', () => {
    render(
      <AssertionTable
        label="Assertions of web"
        columns={COLUMNS}
        rows={[
          { key: 'ok', name: 'ok', expression: 'status in 200..299', severity: 'crit', status: { label: 'pass', tone: 'ok' }, actual: '200', age: 'now' },
          { key: 'ct', name: 'ct', expression: 'header("X") exists', severity: 'warn', status: { label: 'warn', tone: 'warn' }, actual: 'absent', age: '1 minute ago' },
        ]}
      />,
    );
    const table = screen.getByRole('table', { name: 'Assertions of web' });
    expect(within(table).getAllByRole('columnheader').map((c) => c.textContent)).toEqual(Object.values(COLUMNS));
    const rows = within(table).getAllByRole('row').slice(1).map((r) => within(r).getAllByRole('cell').map((c) => c.textContent));
    expect(rows).toEqual([
      ['ok', 'status in 200..299', 'crit', 'pass', '200', 'now'],
      ['ct', 'header("X") exists', 'warn', 'warn', 'absent', '1 minute ago'],
    ]);
  });

  const base = { expression: 'e', severity: 'crit', status: { label: 'pass', tone: 'ok' as const }, actual: '1', age: 'now' };

  it('adds an actions column and a detail row only when a row has them', () => {
    render(
      <AssertionTable
        label="A"
        columns={{ ...COLUMNS, actions: 'Actions' }}
        rows={[
          { ...base, key: 'a', name: 'a', actions: <button type="button">Ignore a</button>, detail: <p>why a</p> },
          { ...base, key: 'b', name: 'b' },
        ]}
      />,
    );
    const table = screen.getByRole('table', { name: 'A' });
    expect(within(table).getAllByRole('columnheader').map((c) => c.textContent)).toEqual([...Object.values(COLUMNS), 'Actions']);
    expect(within(table).getByRole('button', { name: 'Ignore a' })).toBeInTheDocument();
    expect(within(table).getByText('why a')).toBeInTheDocument();
  });

  it('has no actions column when no row has actions', () => {
    render(<AssertionTable label="A" columns={{ ...COLUMNS, actions: 'Actions' }} rows={[{ ...base, key: 'a', name: 'a' }]} />);
    expect(screen.queryByRole('columnheader', { name: 'Actions' })).not.toBeInTheDocument();
  });
});

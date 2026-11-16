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
});

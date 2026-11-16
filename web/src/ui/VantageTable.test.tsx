import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { VantageTable } from './VantageTable.tsx';

describe('VantageTable', () => {
  it('renders one row per probing point under the column names', () => {
    render(
      <VantageTable
        label="Probing points of web"
        columns={{ name: 'Point', status: 'Status', response: 'Time', age: 'Age' }}
        rows={[
          { key: 'manager', name: 'Pulsix server', status: { label: 'up', tone: 'ok' }, response: '12 ms', age: 'now' },
          { key: 'a', name: 'edge-1', status: { label: 'no result', tone: 'unknown' }, response: '—', age: '—' },
        ]}
      />,
    );
    const table = screen.getByRole('table', { name: 'Probing points of web' });
    expect(within(table).getAllByRole('columnheader').map((c) => c.textContent)).toEqual(['Point', 'Status', 'Time', 'Age']);
    expect(within(table).getAllByRole('row').slice(1).map((r) => r.textContent)).toEqual(['Pulsix serverup12 msnow', 'edge-1no result——']);
  });
});

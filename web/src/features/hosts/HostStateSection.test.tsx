import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, screen, waitFor, within } from '@testing-library/react';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { check, NOW, reading, renderAt } from '../../test/hostDetail.tsx';
import { HostStateSection } from './HostStateSection.tsx';

function checksRoute(checks: unknown[]) {
  return { host_id: 'h1', monitored: true, checks };
}

function renderSection() {
  return renderAt(() => <HostStateSection org="acme" hostId="h1" canRead />);
}

describe('HostStateSection', () => {
  it('fullest disk tile picks the highest used_pct item', async () => {
    const asked: string[] = [];
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': checksRoute([
        check('disk', 'disk', [reading('disk.used_pct', 41, { item: '/' }), reading('disk.used_pct', 87, { item: '/var', state: 'warn' })]),
      ]),
      'GET /api/v1/orgs/acme/hosts/h1/metrics': (req: MockRequest) => {
        asked.push(`${req.search.get('check')} ${req.search.get('item')} ${req.search.get('metric')} ${req.search.get('range')}`);
        return { points: [{ at: NOW, value: 80 }, { at: NOW, value: 87 }] };
      },
    });
    renderSection();

    const tile = (await screen.findByText('Disk /var')).closest('[data-tile]') as HTMLElement;
    expect(within(tile).getByText('87 %')).toBeInTheDocument();
    expect(screen.queryByText('Disk /')).not.toBeInTheDocument();
    await waitFor(() => expect(asked).toEqual(['disk /var disk.used_pct 24h']));
    expect(within(tile).getByRole('img', { name: 'Disk /var, last 24 h' })).toBeInTheDocument();
    // The tile takes its colour from the reading's own state.
    expect(tile).toHaveAttribute('data-tone', 'warn');
  });

  it('wraps a long mount path instead of cutting it off', async () => {
    const mount = '/var/lib/containers/storage/overlay/very-long-mount-path';
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': checksRoute([check('disk', 'disk', [reading('disk.used_pct', 41, { item: mount })])]),
      'GET /api/v1/orgs/acme/hosts/h1/metrics': { points: [] },
    });
    renderSection();

    const label = await screen.findByText(`Disk ${mount}`);
    expect(label).not.toHaveClass('truncate');
    expect(label).toHaveClass('break-all');
  });

  it('shows CPU, RAM and load with their current values, and asks one series per tile', async () => {
    const asked: string[] = [];
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': checksRoute([
        check('cpu', 'cpu', [reading('cpu.used_pct', 23.4), reading('cpu.load1', 0.84)]),
        check('memory', 'memory', [reading('mem.used_pct', 61)]),
      ]),
      'GET /api/v1/orgs/acme/hosts/h1/metrics': (req: MockRequest) => {
        asked.push(`${req.search.get('check')}:${req.search.get('metric')}:${req.search.has('item')}`);
        return { points: [] };
      },
    });
    renderSection();

    expect(await screen.findByText('23 %')).toBeInTheDocument();
    expect(screen.getByText('CPU')).toBeInTheDocument();
    expect(screen.getByText('RAM')).toBeInTheDocument();
    expect(screen.getByText('61 %')).toBeInTheDocument();
    expect(screen.getByText('Load')).toBeInTheDocument();
    expect(screen.getByText('0.84')).toBeInTheDocument();
    // A whole-host metric omits `item` rather than sending it empty.
    await waitFor(() => expect([...asked].sort()).toEqual(['cpu:cpu.load1:false', 'cpu:cpu.used_pct:false', 'memory:mem.used_pct:false']));
  });

  it('monitored host without readings says so', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': checksRoute([check('cpu', 'cpu', []), check('memory', 'memory', [])]),
    });
    renderSection();

    expect(await screen.findByText('No readings yet')).toBeInTheDocument();
    expect(screen.queryByText('CPU')).not.toBeInTheDocument();
  });

  it('reports a failed read instead of claiming there are no readings', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': () => new Response(JSON.stringify({ detail: 'store down' }), { status: 500 }),
    });
    renderSection();

    expect(await screen.findByRole('alert')).toHaveTextContent('store down');
    expect(screen.queryByText('No readings yet')).not.toBeInTheDocument();
  });

  describe('polling', () => {
    afterEach(() => {
      vi.useRealTimers();
    });

    it('asks for a tile again after 60 s, not after 30 s', async () => {
      // The clock keeps running on its own so the page can render; the
      // advances below are what move it across the poll interval.
      vi.useFakeTimers({ shouldAdvanceTime: true });
      let served = 0;
      mockApi({
        'GET /api/v1/orgs/acme/hosts/h1/checks': checksRoute([check('memory', 'memory', [reading('mem.used_pct', 61)])]),
        'GET /api/v1/orgs/acme/hosts/h1/metrics': () => {
          served += 1;
          return { points: [{ at: NOW, value: 61 }] };
        },
      });
      renderSection();
      await screen.findByText('61 %');
      await waitFor(() => expect(served).toBe(1));

      await act(async () => {
        await vi.advanceTimersByTimeAsync(31_000);
      });
      expect(served).toBe(1);

      await act(async () => {
        await vi.advanceTimersByTimeAsync(30_000);
      });
      expect(served).toBe(2);
    });
  });
});

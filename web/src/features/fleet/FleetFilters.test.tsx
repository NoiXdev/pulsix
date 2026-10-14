import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { pickOption } from '../../test/pickOption.ts';
import { FleetFilters } from './FleetFilters.tsx';

const ZONES = [
  { id: 'z1', name: 'Frankfurt', slug: 'fra' },
  { id: 'z2', name: 'Amsterdam', slug: 'ams' },
];
const GROUPS = [{ id: 'g1', name: 'Web tier', selector: 'role=web', priority: 0, created_at: '', updated_at: '' }];

describe('FleetFilters', () => {
  it('reports a zone selection', async () => {
    const onChange = vi.fn();
    render(<FleetFilters zones={ZONES} groups={GROUPS} value={{}} onChange={onChange} />);

    await pickOption(screen, 'Zone', { value: 'z2' });
    expect(onChange).toHaveBeenCalledWith({ zone: 'z2' });
  });

  it('clears the zone filter when "All zones" is chosen again', async () => {
    const onChange = vi.fn();
    render(<FleetFilters zones={ZONES} groups={GROUPS} value={{ zone: 'z1' }} onChange={onChange} />);

    await pickOption(screen, 'Zone', { value: '' });
    expect(onChange).toHaveBeenCalledWith({ zone: undefined });
  });

  it('reports a monitoring group selection by name', async () => {
    const onChange = vi.fn();
    render(<FleetFilters zones={ZONES} groups={GROUPS} value={{}} onChange={onChange} />);

    // pickOption itself only finds a row by its exact label, so reaching
    // "Web tier" here already proves the option exists under that name —
    // the separate getByRole('option') the native-<select> version of
    // this test ran afterwards doesn't carry over: Select's rows exist in
    // the DOM only while the list is open (Select.tsx), and picking one
    // closes it, unlike a native <select>'s <option>s, which stay queryable
    // whether the control is open or not.
    await pickOption(screen, 'Monitoring group', 'Web tier');
    expect(onChange).toHaveBeenCalledWith({ group: 'g1' });
  });

  it('reports a tag as a single key or key=value token, never building a selector', () => {
    const onChange = vi.fn();
    render(<FleetFilters zones={ZONES} groups={GROUPS} value={{}} onChange={onChange} />);

    // A plain text field, not a selector builder: there is nowhere here
    // that offers "!=" or "in(...)" — the API refuses those with a 422
    // (v0.3b-2 controller decision). A single fireEvent.change stands in
    // for a whole typed value: this component holds no state of its own,
    // so simulating keystroke-by-keystroke against a `value` prop that
    // never advances (this render is not re-wired to `onChange`) would
    // only exercise this test's own harness, not the component.
    const input = screen.getByLabelText('Tag');
    expect(input).toHaveAttribute('type', 'text');
    fireEvent.change(input, { target: { value: 'role=db' } });
    expect(onChange).toHaveBeenCalledWith({ tag: 'role=db' });
  });

  it('surfaces the API’s own refusal of an unsupported tag expression', () => {
    render(<FleetFilters zones={ZONES} groups={GROUPS} value={{ tag: '!=web' }} onChange={() => {}} tagError="tag must be a single key or key=value token" />);

    expect(screen.getByRole('alert')).toHaveTextContent('tag must be a single key or key=value token');
  });
});

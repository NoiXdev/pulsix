import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { NavGroup } from './NavGroup.tsx';

/** What `may(role, 'checks.read')` leaves behind when the role may not:
 * the literal `false`, as a child of the group. Typed rather than
 * inlined so the expression is not a constant the linter can fold. */
const denied: boolean = false;

describe('NavGroup', () => {
  it('labels the group it wraps, so the heading names its own links', () => {
    render(
      <NavGroup label="Monitoring">
        <a href="/fleet">Fleet</a>
        <a href="/checks">Checks</a>
      </NavGroup>,
    );
    const group = screen.getByRole('group', { name: 'Monitoring' });
    expect(group).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Fleet' })).toBeInTheDocument();
  });

  /**
   * The load-bearing case. Three sidebar entries are permission-gated, so
   * a group can lose every one of its links to a role. A heading left
   * standing over nothing is worse than a missing entry: it claims there
   * is something there that the reader then cannot find. Nothing renders
   * — not the heading, not the wrapper.
   */
  it('renders nothing at all when every child is gated away', () => {
    const { container } = render(
      <NavGroup label="Monitoring">
        {denied && <a href="/fleet">Fleet</a>}
        {null}
      </NavGroup>,
    );
    expect(container).toBeEmptyDOMElement();
    expect(screen.queryByText('Monitoring')).not.toBeInTheDocument();
  });

  it('keeps the group when at least one child survives the gate', () => {
    render(
      <NavGroup label="Monitoring">
        {denied && <a href="/fleet">Fleet</a>}
        <a href="/maintenance">Maintenance</a>
      </NavGroup>,
    );
    expect(screen.getByRole('group', { name: 'Monitoring' })).toBeInTheDocument();
  });
});

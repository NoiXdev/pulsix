import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Select } from '../ui/Select.tsx';
import { pickOption } from './pickOption.ts';

const OPTS = [
  { value: 'c1', label: 'acme-deploy' },
  { value: 'c2', label: 'acme-vault' },
  { value: 'dup', label: 'deploy' },
  { value: 'dup2', label: 'deploy' },
];

// Select.tsx renders a row's label and its hint as two concatenated
// <span>s, so an <li>'s textContent is both together. These fixtures carry
// hints precisely to exercise that: 'db-01's hint happens to be the exact
// string that is 'zone-blue's own label, so a helper that matches
// textContent (hint included) rather than the bare label would pick the
// wrong row whenever a search term for one option's label is buried inside
// another option's hint.
const OPTS_WITH_HINT = [
  { value: 'h1', label: 'db-01', hint: 'zone-blue' },
  { value: 'h2', label: 'zone-blue', hint: 'db-02' },
];

describe('pickOption', () => {
  it('picks by visible label', async () => {
    const onChange = vi.fn();
    render(<Select aria-label="Credential" options={OPTS} value="" onChange={onChange} />);
    await pickOption(screen, 'Credential', 'acme-vault');
    expect(onChange).toHaveBeenCalledWith('c2');
  });

  it('picks by value, which is what the old selectOptions calls passed', async () => {
    const onChange = vi.fn();
    render(<Select aria-label="Credential" options={OPTS} value="" onChange={onChange} />);
    await pickOption(screen, 'Credential', { value: 'c1' });
    expect(onChange).toHaveBeenCalledWith('c1');
  });

  it('picks the right one of two options sharing a label', async () => {
    const onChange = vi.fn();
    render(<Select aria-label="Credential" options={OPTS} value="" onChange={onChange} />);
    await pickOption(screen, 'Credential', { value: 'dup2' });
    expect(onChange).toHaveBeenCalledWith('dup2');
  });

  it('fails loudly when nothing matches, naming what it looked for', async () => {
    render(<Select aria-label="Credential" options={OPTS} value="" onChange={vi.fn()} />);
    await expect(pickOption(screen, 'Credential', 'nope')).rejects.toThrow(/nope/);
  });

  it('does not match a label search against another row\'s hint', async () => {
    const onChange = vi.fn();
    render(<Select aria-label="Host" options={OPTS_WITH_HINT} value="" onChange={onChange} />);
    // 'zone-blue' is h1's hint, not its label — only h2 is actually labelled
    // 'zone-blue'. A helper reading the row's whole textContent would find
    // h1 first (it comes first in the list) and stop there.
    await pickOption(screen, 'Host', 'zone-blue');
    expect(onChange).toHaveBeenCalledWith('h2');
  });

  it('picks the option whose label matches, not the first row whose text merely contains it', async () => {
    const onChange = vi.fn();
    render(<Select aria-label="Credential" options={OPTS} value="" onChange={onChange} />);
    // 'acme-deploy' (c1) comes first in the list and its text contains
    // "deploy" as a substring. Asking for the label "deploy" must still
    // land on the row actually labelled "deploy" (dup), not on c1.
    await pickOption(screen, 'Credential', 'deploy');
    expect(onChange).toHaveBeenCalledWith('dup');
  });

  it('picks a hinted row by its bare label', async () => {
    const onChange = vi.fn();
    render(<Select aria-label="Host" options={OPTS_WITH_HINT} value="" onChange={onChange} />);
    await pickOption(screen, 'Host', 'db-01');
    expect(onChange).toHaveBeenCalledWith('h1');
  });

  it('fails loudly for a hinted list too, naming the field, the search and the options on offer', async () => {
    render(<Select aria-label="Host" options={OPTS_WITH_HINT} value="" onChange={vi.fn()} />);
    let threw = false;
    try {
      await pickOption(screen, 'Host', 'nope');
    } catch (err) {
      threw = true;
      const message = err instanceof Error ? err.message : String(err);
      expect(message).toContain('Host');
      expect(message).toContain('nope');
      expect(message).toContain('db-01');
      expect(message).toContain('zone-blue');
    }
    expect(threw).toBe(true);
  });

  // pickOption calls userEvent.setup() internally. Most of the 72 Vitest
  // call sites it replaces already hold their own `user` from an earlier
  // setup() in the same test, used for the form's other fields (ReposPage.
  // test.tsx:76-94 is typical: type a name, type a URL, then the old
  // selectOptions call). This pins that a second, independent instance
  // alongside the caller's own does not desync — no stuck pointer/keyboard
  // state, no clipboard clash — so pickOption does not need to take a
  // `user` parameter.
  it('coexists with a call site\'s own userEvent instance', async () => {
    const onChange = vi.fn();
    render(
      <div>
        <label htmlFor="name">Name</label>
        <input id="name" />
        <Select aria-label="Credential" options={OPTS} value="" onChange={onChange} />
      </div>,
    );
    const user = userEvent.setup();
    const nameField = screen.getByLabelText('Name');
    await user.type(nameField, 'hello');
    await pickOption(screen, 'Credential', { value: 'c2' });
    await user.type(nameField, ' world');
    expect(nameField).toHaveValue('hello world');
    expect(onChange).toHaveBeenCalledWith('c2');
  });
});

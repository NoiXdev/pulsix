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

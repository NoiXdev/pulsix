import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Select, type SelectOption } from './Select.tsx';

const OPTS: SelectOption[] = [
  { value: 'c1', label: 'acme-deploy', hint: 'SSH key' },
  { value: 'c2', label: 'acme-vault', hint: 'Vault password' },
  { value: 'c3', label: 'beta-deploy', hint: 'SSH key' },
];

function renderSelect(over: Partial<React.ComponentProps<typeof Select>> = {}) {
  const onChange = vi.fn();
  render(<Select aria-label="Credential" options={OPTS} value="" onChange={onChange} {...over} />);
  return { onChange };
}

describe('Select', () => {
  it('opens on click and lists every option', async () => {
    const user = userEvent.setup();
    renderSelect();
    await user.click(screen.getByRole('combobox', { name: 'Credential' }));
    expect(screen.getAllByRole('option')).toHaveLength(3);
  });

  it('narrows the list as you type, case-insensitively', async () => {
    const user = userEvent.setup();
    renderSelect();
    await user.click(screen.getByRole('combobox', { name: 'Credential' }));
    await user.keyboard('ACME');
    expect(screen.getAllByRole('option').map((o) => o.textContent)).toEqual([
      expect.stringContaining('acme-deploy'),
      expect.stringContaining('acme-vault'),
    ]);
  });

  it('matches the hint as well as the label', async () => {
    const user = userEvent.setup();
    renderSelect();
    await user.click(screen.getByRole('combobox', { name: 'Credential' }));
    await user.keyboard('vault');
    expect(screen.getAllByRole('option')).toHaveLength(1);
  });

  it('commits the highlighted option on Enter', async () => {
    const user = userEvent.setup();
    const { onChange } = renderSelect();
    await user.click(screen.getByRole('combobox', { name: 'Credential' }));
    await user.keyboard('beta{Enter}');
    expect(onChange).toHaveBeenCalledWith('c3');
  });

  // Review Focus 5: labels are not unique. The committed value is the
  // option's own, never its text.
  it('commits the right value when two options share a label', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(
      <Select
        aria-label="Credential"
        value=""
        onChange={onChange}
        options={[
          { value: 'k1', label: 'deploy', hint: 'SSH key' },
          { value: 'k2', label: 'deploy', hint: 'Git token' },
        ]}
      />,
    );
    await user.click(screen.getByRole('combobox', { name: 'Credential' }));
    await user.click(screen.getAllByRole('option')[1]);
    expect(onChange).toHaveBeenCalledWith('k2');
  });

  // Review Focus 1: an unresolved query is not an empty world.
  it('says the list is empty rather than showing an empty popup', async () => {
    const user = userEvent.setup();
    renderSelect({ options: [] });
    await user.click(screen.getByRole('combobox', { name: 'Credential' }));
    expect(screen.getByText('No options')).toBeInTheDocument();
    expect(screen.queryAllByRole('option')).toHaveLength(0);
  });

  it('says so when the filter matches nothing', async () => {
    const user = userEvent.setup();
    renderSelect();
    await user.click(screen.getByRole('combobox', { name: 'Credential' }));
    await user.keyboard('zzz');
    expect(screen.getByText('No match')).toBeInTheDocument();
  });

  // Review Focus 2: a credential someone deleted is still referenced by
  // the host that used it. A native select rendered this blank, which is
  // how it stayed wrong for so long.
  it('shows a value that is missing from the options, and keeps it', async () => {
    const onChange = vi.fn();
    render(<Select aria-label="Credential" options={OPTS} value="gone" onChange={onChange} />);
    expect(screen.getByRole('combobox', { name: 'Credential' })).toHaveValue('gone (unavailable)');
    expect(onChange).not.toHaveBeenCalled();
  });

  // Review Focus 3: half-typed text is not a value.
  it('restores the selected label when it closes without a choice', async () => {
    const user = userEvent.setup();
    renderSelect({ value: 'c1' });
    const box = screen.getByRole('combobox', { name: 'Credential' });
    expect(box).toHaveValue('acme-deploy');
    await user.click(box);
    await user.keyboard('zz');
    expect(box).toHaveValue('zz');
    await user.tab();
    expect(box).toHaveValue('acme-deploy');
  });

  it('offers the placeholder as a way back to no value', async () => {
    const user = userEvent.setup();
    const { onChange } = renderSelect({ value: 'c1', placeholder: 'none' });
    await user.click(screen.getByRole('combobox', { name: 'Credential' }));
    await user.click(screen.getByRole('option', { name: 'none' }));
    expect(onChange).toHaveBeenCalledWith('');
  });

  it('does not open when disabled', async () => {
    const user = userEvent.setup();
    renderSelect({ disabled: true });
    await user.click(screen.getByRole('combobox', { name: 'Credential' }));
    expect(screen.queryAllByRole('option')).toHaveLength(0);
  });

  it('skips a disabled option', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(
      <Select
        aria-label="Credential"
        value=""
        onChange={onChange}
        options={[{ value: 'a', label: 'Allowed' }, { value: 'b', label: 'Blocked', disabled: true }]}
      />,
    );
    await user.click(screen.getByRole('combobox', { name: 'Credential' }));
    await user.click(screen.getByRole('option', { name: 'Blocked' }));
    expect(onChange).not.toHaveBeenCalled();
  });

  it('carries a data-value on every row, for the test helper to aim at', async () => {
    const user = userEvent.setup();
    renderSelect();
    await user.click(screen.getByRole('combobox', { name: 'Credential' }));
    expect(screen.getAllByRole('option')[0]).toHaveAttribute('data-value', 'c1');
  });
});

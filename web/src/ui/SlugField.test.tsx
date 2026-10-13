import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { SlugField } from './SlugField.tsx';

function setup(initial = '', source = 'Acme GmbH') {
  const write = vi.fn();
  render(
    <SlugField
      id="s"
      inputProps={{ defaultValue: initial, name: 's' }}
      readSource={() => source}
      write={write}
    />,
  );
  return { write, input: screen.getByRole('textbox') };
}

describe('SlugField', () => {
  it('fills an empty slug on blur', async () => {
    const { write, input } = setup('');
    await userEvent.click(input);
    await userEvent.tab();
    expect(write).toHaveBeenCalledWith('acme-gmbh');
  });

  it('leaves a slug that already has something alone', async () => {
    // The field following the name around as it is edited is the
    // behaviour people learn to fight; a slug someone typed is a choice.
    const { write, input } = setup('chosen-by-hand');
    await userEvent.click(input);
    await userEvent.tab();
    expect(write).not.toHaveBeenCalled();
  });

  it('overwrites on the button, which is the only way back', async () => {
    const { write } = setup('chosen-by-hand');
    await userEvent.click(screen.getByRole('button', { name: /generate slug/i }));
    expect(write).toHaveBeenCalledWith('acme-gmbh');
  });

  it('does nothing rather than clearing when the name yields no slug', async () => {
    const { write } = setup('keep-me', '!!! ???');
    await userEvent.click(screen.getByRole('button', { name: /generate slug/i }));
    expect(write).not.toHaveBeenCalled();
  });
});

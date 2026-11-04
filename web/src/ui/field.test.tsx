import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { Field } from './field.tsx';
import { Input } from './input.tsx';

describe('Field', () => {
  it('links the hint to the control', () => {
    render(
      <Field id="name" label="Name" hint="Shown to everyone">
        <Input id="name" />
      </Field>,
    );
    const input = screen.getByLabelText('Name');
    expect(input).toHaveAccessibleDescription('Shown to everyone');
    expect(input).not.toHaveAttribute('aria-invalid');
  });

  it('links the error to the control, marks it invalid and announces it', () => {
    render(
      <Field id="name" label="Name" hint="Shown to everyone" error="required">
        <Input id="name" />
      </Field>,
    );
    const input = screen.getByLabelText('Name');
    expect(input).toHaveAttribute('aria-invalid', 'true');
    expect(input).toHaveAccessibleDescription('required');
    expect(screen.getByRole('alert')).toHaveTextContent('required');
  });

  it('leaves a control that carries its own aria-describedby alone', () => {
    render(
      <Field id="name" label="Name">
        <Input id="name" aria-describedby="own" />
        <span id="own">own text</span>
      </Field>,
    );
    expect(screen.getByLabelText('Name')).toHaveAccessibleDescription('own text');
  });
});

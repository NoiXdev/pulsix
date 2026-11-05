import { screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { FieldSource, VarSchema } from './types.ts';

/** Queries and steps the VariableEditor tests share (EditorHarness.tsx
 * renders the editor they drive). Test-only. */

export const schemaNow = () => JSON.parse(screen.getByTestId('schema').textContent ?? '{}') as VarSchema;
export const dialog = (name: string) => screen.getByRole('dialog', { name: `Field ${name}` });
export const subField = (name: string) => screen.getByRole('group', { name: `Sub-field ${name}` });
export const dataRows = () => within(screen.getByRole('table', { name: 'Fields' })).getAllByRole('row').slice(1);
export const openField = (name: string) => userEvent.setup().click(screen.getByRole('button', { name: new RegExp(`^${name}\\b`) }));
export const done = (name: string) => userEvent.setup().click(within(dialog(name)).getByRole('button', { name: 'Done' }));

export const src: FieldSource = { kind: 'argument_spec', role: 'web', file: 'roles/web/meta/argument_specs.yml', line: 3 };

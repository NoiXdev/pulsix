import { useState } from 'react';
import { SchemaForm } from './SchemaForm.tsx';
import { membershipFields } from './paths.ts';
import type { VarSchema, Vars } from './types.ts';
import { useT } from '../../i18n/useT.ts';

/** AssignmentPreview is the form an operator fills in when a host joins an
 * inventory group, generated live from the schema being edited. */
export function AssignmentPreview({ schema }: { schema: VarSchema }) {
  const t = useT();
  const [vars, setVars] = useState<Vars>({});
  return (
    <section aria-label={t.variables.builder.previewLabel} className="flex flex-col gap-3 rounded-[10px] border border-border bg-card p-4">
      <h3 className="text-[13px] font-semibold">{t.variables.builder.previewHeading}</h3>
      <p className="text-[11px] text-muted-foreground">{t.variables.builder.previewHint}</p>
      <SchemaForm fields={membershipFields(schema)} value={vars} onChange={setVars} idPrefix="preview" />
    </section>
  );
}

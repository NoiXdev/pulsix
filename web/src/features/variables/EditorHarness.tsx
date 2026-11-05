import { useState } from 'react';
import { VariableEditor, type VariableEditorProps } from './VariableEditor.tsx';
import type { FieldUsage } from './usage.ts';
import type { EditorVariant, Problem, VarSchema } from './types.ts';

/**
 * EditorHarness holds a VariableEditor's value the way a page does and
 * prints it, so the editor's tests (VariableEditor*.test.tsx, helpers in
 * editorTestKit.ts) can read what an interaction produced. Test-only.
 */
export function EditorHarness({
  initial = { fields: [] },
  problems,
  variant = 'group',
  usage,
  usageLinks,
}: {
  initial?: VarSchema;
  problems?: Problem[];
  variant?: EditorVariant;
  usage?: FieldUsage;
  usageLinks?: VariableEditorProps['usageLinks'];
}) {
  const [value, setValue] = useState<VarSchema>(initial);
  return (
    <>
      <VariableEditor value={value} onChange={setValue} problems={problems} variant={variant} usage={usage} usageLinks={usageLinks} />
      <pre data-testid="schema">{JSON.stringify(value)}</pre>
    </>
  );
}

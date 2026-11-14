import { useState, type FormEvent } from 'react';
import { Plus, Trash2 } from 'lucide-react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Field } from '../../ui/field.tsx';
import { Input } from '../../ui/input.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { usePutSecret } from '../../api/hooks/useSecrets.ts';
import type { OrgSecret } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { fieldsOf, MAX_FIELDS, validateSecret, type FieldRow, type SecretErrors } from './secretForm.ts';

export interface SecretDialogProps {
  org: string;
  /** The secret to replace; undefined creates one. */
  secret?: OrgSecret;
  /** The names in use, which a create may not take. */
  taken: readonly string[];
  onClose: () => void;
}

/**
 * SecretDialog creates a secret or replaces one whole. The API never
 * returns a value, so replacing prefills the field names only: every value
 * is entered again, and the PUT stores exactly the rows sent. The values
 * live in this component's state alone — it is mounted only while open —
 * and the mutation is reset as soon as the call settles, so nothing keeps
 * them once the dialog is gone.
 */
export function SecretDialog({ org, secret, taken, onClose }: SecretDialogProps) {
  const t = useT();
  const s = t.secrets;
  const d = s.dialog;
  const toast = useToast();
  const put = usePutSecret(org);
  const [name, setName] = useState(secret?.name ?? '');
  const [rows, setRows] = useState<FieldRow[]>(() =>
    secret?.field_names?.length ? secret.field_names.map((n) => ({ name: n, value: '' })) : [{ name: '', value: '' }],
  );
  const [errors, setErrors] = useState<SecretErrors | null>(null);
  const [failure, setFailure] = useState<string | null>(null);

  function setRow(i: number, patch: Partial<FieldRow>) {
    setRows((all) => all.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setFailure(null);
    const found = validateSecret(t, name, rows, secret ? null : taken);
    setErrors(found);
    if (found) return;
    try {
      const res = await put.mutateAsync({ name, fields: fieldsOf(rows) });
      toast.show(s.saved(res.secret.name));
      onClose();
    } catch (err) {
      setFailure(err instanceof Error ? err.message : s.saveFailed);
    } finally {
      put.reset();
    }
  }

  return (
    <Dialog open onClose={onClose} title={secret ? d.replaceTitle(secret.name) : d.newTitle} size="wide">
      <form className="flex flex-col gap-4" noValidate onSubmit={(e) => void submit(e)}>
        <p className="text-xs text-muted-foreground">
          {secret && <>{d.replaceNote} </>}
          {d.neverShown}
        </p>
        {!secret && (
          <Field id="secret-name" label={d.name} hint={d.nameHint} error={errors?.name}>
            <Input
              id="secret-name"
              className="font-mono"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="switch-core"
              autoComplete="off"
              maxLength={64}
            />
          </Field>
        )}
        <div className="flex flex-col gap-2">
          <span className="text-xs font-medium">{d.fields}</span>
          <span className="text-[11px] text-muted-foreground">{d.fieldsHint}</span>
          {rows.map((row, i) => {
            const re = errors?.rows[i];
            const nameErr = `secret-field-${i}-name-error`;
            const valueErr = `secret-field-${i}-value-error`;
            return (
              <div key={i} className="flex flex-col gap-1">
                <div className="grid grid-cols-[minmax(0,0.8fr)_minmax(0,1fr)_32px] gap-2">
                  <Input
                    aria-label={d.fieldName(i + 1)}
                    className="font-mono"
                    value={row.name}
                    onChange={(e) => setRow(i, { name: e.target.value })}
                    autoComplete="off"
                    maxLength={64}
                    aria-invalid={re?.name ? true : undefined}
                    aria-describedby={re?.name ? nameErr : undefined}
                  />
                  <Input
                    aria-label={d.fieldValue(i + 1)}
                    type="password"
                    value={row.value}
                    onChange={(e) => setRow(i, { value: e.target.value })}
                    autoComplete="new-password"
                    aria-invalid={re?.value ? true : undefined}
                    aria-describedby={re?.value ? valueErr : undefined}
                  />
                  <Button
                    type="button"
                    variant="ghost"
                    aria-label={d.removeField(i + 1)}
                    disabled={rows.length <= 1}
                    onClick={() => {
                      setRows((all) => all.filter((_, j) => j !== i));
                      setErrors(null);
                    }}
                  >
                    <Trash2 size={13} />
                  </Button>
                </div>
                {re?.name && (
                  <span id={nameErr} className="text-[11px] text-destructive">
                    {d.fieldName(i + 1)} — {re.name}
                  </span>
                )}
                {re?.value && (
                  <span id={valueErr} className="text-[11px] text-destructive">
                    {d.fieldValue(i + 1)} — {re.value}
                  </span>
                )}
              </div>
            );
          })}
          <Button
            type="button"
            variant="ghost"
            className="self-start"
            disabled={rows.length >= MAX_FIELDS}
            onClick={() => setRows((all) => [...all, { name: '', value: '' }])}
          >
            <Plus size={13} /> {d.addField}
          </Button>
          {errors?.form && <span className="text-[11px] text-destructive">{errors.form}</span>}
        </div>
        {failure && (
          <p role="alert" className="text-xs text-[var(--px-crit)]">
            {failure}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={onClose}>
            {t.common.cancel}
          </Button>
          <Button type="submit" variant="primary" disabled={put.isPending}>
            {t.common.save}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

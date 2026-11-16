import { Label } from '../../ui/label.tsx';
import { MultiSelect } from '../../ui/MultiSelect.tsx';
import { useTemplates } from '../../api/hooks/useTemplates.ts';
import { useT } from '../../i18n/useT.ts';

/** MAX_FOLLOW_UPS is the most follow-ups a template or one run may have. */
export const MAX_FOLLOW_UPS = 10;

export interface FollowUpsFieldProps {
  org: string;
  id: string;
  /** The chosen template ids, in the order they start. */
  value: string[];
  onChange: (value: string[]) => void;
  /** The template being edited: it cannot follow itself. */
  excludeId?: string;
  invalid?: boolean;
  describedBy?: string;
}

/**
 * FollowUpsField picks the templates started on the same hosts after a
 * successful run: a MultiSelect of the organization's templates (minus
 * excludeId), with its label and help text. The chosen order is the start
 * order.
 */
export function FollowUpsField({ org, id, value, onChange, excludeId, invalid, describedBy }: FollowUpsFieldProps) {
  const t = useT();
  const templates = useTemplates(org);
  const options = (templates.data?.templates ?? []).filter((tpl) => tpl.id !== excludeId).map((tpl) => ({ value: tpl.id, label: tpl.name }));
  const helpId = `${id}-help`;
  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor={id}>{t.templates.followUps.label}</Label>
      <MultiSelect
        id={id}
        listLabel={t.templates.followUps.chosen}
        placeholder={t.templates.followUps.placeholder}
        options={options}
        value={value}
        onChange={onChange}
        max={MAX_FOLLOW_UPS}
        invalid={invalid}
        aria-describedby={describedBy ? `${helpId} ${describedBy}` : helpId}
      />
      <span id={helpId} className="text-[11px] text-muted-foreground">
        {t.templates.followUps.help}
      </span>
    </div>
  );
}

import { useId } from 'react';
import type { ReactNode } from 'react';
import { useT } from '../i18n/useT.ts';
import { cn } from '../lib/utils.ts';
import { Button } from './button.tsx';

/** PageHeader — h1 with an optional badge beside it, a muted description
 * below and actions on the right; wraps on narrow screens. */
export function PageHeader({
  title,
  description,
  badge,
  actions,
}: {
  title: ReactNode;
  description?: ReactNode;
  badge?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-3">
      <div className="min-w-0">
        <div className="flex flex-wrap items-center gap-2.5">
          <h1 className="text-xl font-semibold">{title}</h1>
          {badge}
        </div>
        {description ? <p className="mt-1 text-[13px] text-muted-foreground">{description}</p> : null}
      </div>
      {actions ? <div className="flex flex-wrap items-center gap-2">{actions}</div> : null}
    </div>
  );
}

/** SettingsPage — the header plus one column of section cards. */
export function SettingsPage({
  title,
  description,
  badge,
  actions,
  children,
}: {
  title: ReactNode;
  description?: ReactNode;
  badge?: ReactNode;
  actions?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <div className="flex flex-col gap-5">
      <PageHeader title={title} description={description} badge={badge} actions={actions} />
      <div className="flex max-w-[880px] flex-col gap-5">{children}</div>
    </div>
  );
}

/** SettingsCard — a titled section: header, rows, optional footer (save bar). */
export function SettingsCard({
  title,
  description,
  badge,
  children,
  footer,
  headingId,
}: {
  title: ReactNode;
  description?: ReactNode;
  badge?: ReactNode;
  children?: ReactNode;
  footer?: ReactNode;
  headingId?: string;
}) {
  const generated = useId();
  const id = headingId ?? generated;
  return (
    // Same surface as Card (table.tsx), as a labelled section; keep data-card
    // (the e2e clipped-card check selects it) and the overflow-clip/shrink-0 rationale.
    <section data-card="" aria-labelledby={id} className="flex shrink-0 flex-col overflow-clip rounded-xl border border-border bg-card">
      <div className="flex items-start justify-between gap-3 px-5 pb-3.5 pt-[18px]">
        <div className="min-w-0">
          <h2 id={id} className="text-[14.5px] font-semibold">
            {title}
          </h2>
          {description ? <p className="mt-0.5 text-[12.5px] text-muted-foreground">{description}</p> : null}
        </div>
        {badge ? <div className="shrink-0">{badge}</div> : null}
      </div>
      {children}
      {footer}
    </section>
  );
}

/** SettingsRow — label and help on the left, the control on the right; one
 * column below `sm`. `indent` marks a sub-setting; `stack` puts the control
 * under the label at every width (wide controls). */
export function SettingsRow({
  label,
  htmlFor,
  labelId,
  help,
  children,
  indent,
  stack,
}: {
  label: ReactNode;
  htmlFor?: string;
  labelId?: string;
  help?: ReactNode;
  children?: ReactNode;
  indent?: boolean;
  stack?: boolean;
}) {
  const labelClass = 'text-[13.5px] font-medium';
  return (
    <div
      className={cn(
        'grid grid-cols-1 items-center gap-x-6 gap-y-2 border-t border-border px-5 py-3.5',
        !stack && 'sm:grid-cols-[minmax(0,1fr)_auto]',
        indent && 'bg-background/40 pl-11',
      )}
    >
      <div className="min-w-0">
        {htmlFor ? (
          <label htmlFor={htmlFor} id={labelId} className={cn('block', labelClass)}>
            {label}
          </label>
        ) : (
          <div id={labelId} className={labelClass}>
            {label}
          </div>
        )}
        {help ? <p className="mt-0.5 text-[12.5px] text-muted-foreground">{help}</p> : null}
      </div>
      {children ? <div className="min-w-0">{children}</div> : null}
    </div>
  );
}

/** CardSaveBar — the card footer of a form: unsaved hint and Discard while
 * dirty, a Save that submits the card's form. */
export function CardSaveBar({
  dirty,
  saving,
  onDiscard,
  saveLabel,
  formId,
}: {
  dirty: boolean;
  saving: boolean;
  onDiscard: () => void;
  saveLabel?: string;
  formId?: string;
}) {
  const t = useT();
  return (
    <div className="flex items-center justify-between gap-3 border-t border-border bg-background/40 px-5 py-3">
      <span className="flex items-center gap-2 text-[12.5px] text-muted-foreground">
        {dirty ? (
          <>
            <span aria-hidden="true" className="h-1.5 w-1.5 rounded-full" style={{ backgroundColor: 'var(--px-warn)' }} />
            {t.ui.settings.unsaved}
          </>
        ) : null}
      </span>
      <div className="flex items-center gap-2">
        {dirty ? (
          <Button type="button" variant="ghost" onClick={onDiscard} disabled={saving}>
            {t.ui.settings.discard}
          </Button>
        ) : null}
        <Button type="submit" variant="primary" form={formId} disabled={!dirty || saving}>
          {saveLabel ?? t.ui.settings.save}
        </Button>
      </div>
    </div>
  );
}

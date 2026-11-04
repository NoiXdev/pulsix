import { useEffect, useRef, type FormEvent, type ReactNode } from 'react';
import { Link, useBlocker, useNavigate, type LinkProps } from '@tanstack/react-router';
import { ArrowLeft } from 'lucide-react';
import { Button } from './button.tsx';
import { buttonClass } from './buttonStyles.ts';
import { useT } from '../i18n/useT.ts';

export interface FormPageProps {
  title: string;
  /** The list this form belongs to: where Cancel and the back link go, and
   * where a successful save lands. */
  backTo: { to: LinkProps['to']; params?: LinkProps['params'] };
  children: ReactNode;
  /** Runs on Save. Resolve `true` once the changes are stored: the page then
   * leaves for `backTo` without the unsaved-changes question. Resolve
   * `false` (a validation error, a refused write) to stay on the form. */
  onSubmit: () => Promise<boolean> | boolean;
  submitting: boolean;
  /** Whether the form holds changes that are not stored yet. */
  dirty: boolean;
  /** The Save button's label; "Save" by default. */
  submitLabel?: string;
}

/**
 * FormPage is the frame of a create/edit form that has a page of its own
 * instead of a dialog: a back link and title, the form at full width, and a
 * footer with Save and Cancel that sticks to the bottom of the scrolling
 * area so it stays reachable on a long form.
 *
 * Leaving with unsaved changes (Cancel, the back link, a nav item, the
 * browser's back button) asks first through the router's blocker, and a
 * closing or reloading tab gets the browser's own prompt. The navigation
 * that a successful save triggers is the one exception: it is not blocked.
 * Presentational: the page owns the values, the validation and the write.
 */
export function FormPage({ title, backTo, children, onSubmit, submitting, dirty, submitLabel }: FormPageProps) {
  const t = useT();
  const navigate = useNavigate();
  // Refs, so the blocker always sees the latest state without
  // re-registering and the save's own navigation can switch it off
  // synchronously, before React has rendered the page as clean.
  const dirtyRef = useRef(dirty);
  useEffect(() => {
    dirtyRef.current = dirty;
  }, [dirty]);
  const leaving = useRef(false);

  useBlocker({
    shouldBlockFn: () => {
      if (leaving.current || !dirtyRef.current) return false;
      return !window.confirm(t.ui.formPage.unsaved);
    },
    enableBeforeUnload: () => dirtyRef.current && !leaving.current,
  });

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!(await onSubmit())) return;
    leaving.current = true;
    void navigate({ to: backTo.to, params: backTo.params });
  }

  return (
    <form onSubmit={submit} className="flex min-w-0 flex-col gap-5">
      <div className="flex flex-col gap-2">
        <Link
          to={backTo.to}
          params={backTo.params}
          className="inline-flex w-fit items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
        >
          <ArrowLeft size={13} /> {t.common.back}
        </Link>
        <h1 className="text-xl font-semibold">{title}</h1>
      </div>
      <div className="flex w-full min-w-0 flex-col gap-5">{children}</div>
      <div
        role="group"
        aria-label={t.ui.formPage.actions}
        className="sticky bottom-0 z-10 -mx-1 flex justify-end gap-2 border-t border-border bg-background px-1 py-3"
      >
        <Link
          to={backTo.to}
          params={backTo.params}
          className={buttonClass('default', 'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary')}
        >
          {t.common.cancel}
        </Link>
        <Button type="submit" variant="primary" disabled={submitting}>
          {submitLabel ?? t.common.save}
        </Button>
      </div>
    </form>
  );
}

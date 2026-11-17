import type { ReactNode } from 'react';
import { Link } from '@tanstack/react-router';
import { Plus } from 'lucide-react';
import { buttonClass } from '../../../ui/buttonStyles.ts';

/**
 * SharedListFrame is what the three shared-resource lists have in common:
 * the title, what projecting a definition means, an Add link to the
 * new-definition page, and the list itself as the children.
 */
export function SharedListFrame({
  title,
  hint,
  addLabel,
  addTo,
  org,
  children,
}: {
  title: string;
  hint: string;
  addLabel: string;
  addTo: '/o/$org/settings/shared/repos/new' | '/o/$org/settings/shared/templates/new' | '/o/$org/settings/shared/checks/new';
  org: string;
  children: ReactNode;
}) {
  return (
    <>
      <h1 className="text-xl font-semibold">{title}</h1>
      <div className="flex items-center justify-between gap-3">
        <span className="text-[13px] text-muted-foreground">{hint}</span>
        <Link to={addTo} params={{ org }} className={buttonClass('primary')}>
          <Plus size={15} /> {addLabel}
        </Link>
      </div>
      {children}
    </>
  );
}

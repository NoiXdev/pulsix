import type { ReactNode } from 'react';
import { Link } from '@tanstack/react-router';
import { Plus } from 'lucide-react';
import { buttonClass } from '../../../ui/buttonStyles.ts';
import { PageHeader } from '../../../ui/settings.tsx';

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
      <PageHeader
        title={title}
        description={hint}
        actions={
          <Link to={addTo} params={{ org }} className={buttonClass('primary')}>
            <Plus size={15} /> {addLabel}
          </Link>
        }
      />
      {children}
    </>
  );
}

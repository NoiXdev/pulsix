import type { ReactNode } from 'react';
import { Link, useNavigate, type LinkProps } from '@tanstack/react-router';
import { Plus } from 'lucide-react';
import { ListRow, RowMenu, RowMenuItem } from '../../../ui/list-row.tsx';
import { GridHeader, Card } from '../../../ui/table.tsx';
import { buttonClass } from '../../../ui/buttonStyles.ts';
import { useT } from '../../../i18n/useT.ts';

/** The last column of every list: the row menu's 40px. */
export const MENU_COLUMN = '40px';

/**
 * SharedListFrame is what the three shared-resource lists have in common:
 * the title, the count, an Add link to the new-definition page, and the card
 * with its column headings, rows and empty state. The rows are SharedRows.
 */
export function SharedListFrame({
  title,
  count,
  addLabel,
  addTo,
  org,
  columns,
  headings,
  empty,
  isEmpty,
  children,
}: {
  title: string;
  count: string;
  addLabel: string;
  addTo: '/o/$org/settings/shared/repos/new' | '/o/$org/settings/shared/templates/new' | '/o/$org/settings/shared/checks/new';
  org: string;
  /** The grid columns without the menu column, which is appended. */
  columns: string;
  headings: string[];
  empty: string;
  isEmpty: boolean;
  children: ReactNode;
}) {
  const t = useT();
  const grid = `${columns} ${MENU_COLUMN}`;
  return (
    <>
      <h1 className="text-xl font-semibold">{title}</h1>
      <div className="flex items-center justify-between gap-3">
        <span className="text-[13px] text-muted-foreground">{count}</span>
        <Link to={addTo} params={{ org }} className={buttonClass('primary')}>
          <Plus size={15} /> {addLabel}
        </Link>
      </div>
      <Card>
        <GridHeader columns={grid}>
          {headings.map((h) => (
            <span key={h}>{h}</span>
          ))}
          <span className="sr-only">{t.instance.shared.actions}</span>
        </GridHeader>
        {children}
        {isEmpty && <p className="px-4 py-6 text-center text-xs text-muted-foreground">{empty}</p>}
      </Card>
    </>
  );
}

/**
 * SharedRow is one row of a shared-resource list: the whole row opens the
 * definition's form page, and the menu offers Open and Delete…. The cells
 * go in as children, in the order of the list's columns; the menu column is
 * added here.
 */
export function SharedRow({
  to,
  params,
  columns,
  name,
  onDelete,
  children,
}: {
  to: LinkProps['to'];
  params: LinkProps['params'];
  columns: string;
  name: string;
  onDelete: () => void;
  children: ReactNode;
}) {
  const t = useT();
  const navigate = useNavigate();
  return (
    <ListRow
      to={to}
      params={params}
      columns={`${columns} ${MENU_COLUMN}`}
      label={name}
      menu={
        <RowMenu label={t.instance.shared.rowActions(name)}>
          <RowMenuItem onSelect={() => void navigate({ to, params })}>{t.instance.shared.open}</RowMenuItem>
          <RowMenuItem onSelect={onDelete} destructive>
            {t.instance.shared.deleteItem}
          </RowMenuItem>
        </RowMenu>
      }
    >
      {children}
      <span />
    </ListRow>
  );
}

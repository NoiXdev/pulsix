import type { RefObject } from 'react';
import { useRouter } from '@tanstack/react-router';
import { useT } from '../../i18n/useT.ts';
import { RowMenu, RowMenuItem, RowMenuLink } from '../list-row.tsx';
import { MenuPopover, MenuSeparator } from '../menu-popover.tsx';
import type { MenuPoint } from '../menuPlacement.ts';
import { useCopy } from '../useCopy.ts';
import type { RowAction, RowLink } from './types.ts';

/** A row's actions as menu items, in the order given. */
function ActionItems({ actions }: { actions: RowAction[] }) {
  return (
    <>
      {actions.map((a) =>
        a.kind === 'link' ? (
          <RowMenuLink key={a.id} href={a.href} download={a.download}>
            {a.label}
          </RowMenuLink>
        ) : (
          <RowMenuItem key={a.id} onSelect={a.onSelect} destructive={a.destructive} disabled={a.disabled}>
            {a.label}
          </RowMenuItem>
        ),
      )}
    </>
  );
}

/** The "…" button of a row and its menu. */
export function RowActionsButton({ label, actions }: { label: string; actions: RowAction[] }) {
  return (
    <RowMenu label={label}>
      <ActionItems actions={actions} />
    </RowMenu>
  );
}

/**
 * RowContextMenu is a row's menu opened by a right-click, the ContextMenu
 * key or Shift+F10: the same actions as the "…" menu, and before them, when
 * the row opens a page, "Open in new tab" and "Copy link".
 */
export function RowContextMenu({
  label,
  anchor,
  point,
  link,
  actions,
  onClose,
}: {
  label: string;
  anchor: RefObject<HTMLElement | null>;
  point: MenuPoint;
  link?: RowLink;
  actions: RowAction[];
  onClose: (restoreFocus: boolean) => void;
}) {
  const t = useT();
  const router = useRouter();
  const copy = useCopy();
  const href = link ? router.buildLocation({ to: link.to, params: link.params, search: link.search, hash: link.hash } as Parameters<typeof router.buildLocation>[0]).href : null;
  return (
    <MenuPopover label={label} anchor={anchor} point={point} onClose={onClose}>
      {href !== null && (
        <>
          <RowMenuItem onSelect={() => window.open(href, '_blank', 'noopener,noreferrer')}>{t.ui.dataList.openInNewTab}</RowMenuItem>
          <RowMenuItem onSelect={() => void copy(new URL(href, window.location.href).href, t.ui.dataList.linkCopied)}>{t.ui.dataList.copyLink}</RowMenuItem>
          {actions.length > 0 && <MenuSeparator />}
        </>
      )}
      <ActionItems actions={actions} />
    </MenuPopover>
  );
}

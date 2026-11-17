import { createContext, useContext } from 'react';

export interface MenuContextValue {
  /** Closes the menu and puts focus back where it came from. */
  select: () => void;
}

/** Set by MenuPopover for its items (RowMenuItem, RowMenuLink). Its own file
 * because a module of components may export only components. */
export const MenuContext = createContext<MenuContextValue | null>(null);

export function useMenu(): MenuContextValue | null {
  return useContext(MenuContext);
}

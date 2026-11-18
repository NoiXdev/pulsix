import { Braces, Hash, KeyRound, List, ListChecks, Percent, Rows3, ToggleLeft, Type, type LucideIcon } from 'lucide-react';
import type { FieldType } from '../../variables/types.ts';

/** The icon of each field type, the same in the row's type badge, the type
 * menu and the type cards. */
export const TYPE_ICONS: Record<FieldType, LucideIcon> = {
  string: Type,
  int: Hash,
  float: Percent,
  bool: ToggleLeft,
  enum: ListChecks,
  secret: KeyRound,
  list: List,
  repeater: Rows3,
  object: Braces,
};

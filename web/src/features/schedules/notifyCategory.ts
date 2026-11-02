import type { Tone } from '../../ui/badge.tsx';
import type { NotifyRuleInput } from '../../api/hooks/useNotifications.ts';

export type Category = NotifyRuleInput['on'];

/** CATEGORIES is the canonical order the server itself reports notify
 * categories in (notify.Categories()) — failure, change, success, skipped. */
export const CATEGORIES: Category[] = ['failure', 'change', 'success', 'skipped'];

export const CATEGORY_TONE: Record<Category, Tone> = { failure: 'crit', change: 'warn', success: 'ok', skipped: 'unknown' };

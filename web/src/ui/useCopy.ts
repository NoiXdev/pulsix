import { useCallback } from 'react';
import { copyText } from '../lib/clipboard.ts';
import { useT } from '../i18n/useT.ts';
import { useToast } from './toastContext.ts';

/**
 * Returns a copy function that copies `text` and toasts the outcome: the
 * given message (or a generic "Copied") on success, and a destructive hint to
 * select the text and press Ctrl/Cmd+C when the browser refuses.
 */
export function useCopy(): (text: string, copiedMessage?: string) => Promise<boolean> {
  const t = useT();
  const toast = useToast();
  return useCallback(
    async (text, copiedMessage) => {
      const ok = await copyText(text);
      if (ok) toast.show(copiedMessage ?? t.common.copied);
      else toast.show(t.common.copyUnavailable, 'destructive');
      return ok;
    },
    [t, toast],
  );
}

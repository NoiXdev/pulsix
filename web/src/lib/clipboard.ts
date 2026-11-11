/**
 * Copies text to the clipboard and reports whether it worked.
 *
 * `navigator.clipboard` exists only in a secure context, and a compose
 * install is often reached over plain http://host:port. Without it (or when
 * it refuses) a hidden textarea and `execCommand('copy')` are the fallback.
 * Never throws; callers decide what to tell the user on `false`.
 */
export async function copyText(text: string): Promise<boolean> {
  if (typeof navigator !== 'undefined' && navigator.clipboard) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // Permission denied or document not focused: try the fallback.
    }
  }
  return execCommandCopy(text);
}

function execCommandCopy(text: string): boolean {
  const area = document.createElement('textarea');
  area.value = text;
  area.readOnly = true;
  area.setAttribute('aria-hidden', 'true');
  area.style.position = 'fixed';
  area.style.top = '0';
  area.style.left = '-9999px';
  area.style.opacity = '0';
  const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  document.body.appendChild(area);
  try {
    area.focus();
    area.select();
    return document.execCommand('copy');
  } catch {
    return false;
  } finally {
    area.remove();
    previous?.focus();
  }
}

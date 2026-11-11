import { afterEach, describe, expect, it, vi } from 'vitest';
import { copyText } from './clipboard.ts';

const original = Object.getOwnPropertyDescriptor(navigator, 'clipboard');

function setClipboard(value: unknown) {
  Object.defineProperty(navigator, 'clipboard', { value, configurable: true });
}

function stubExecCommand(result: boolean | Error) {
  const fn = vi.fn(() => {
    if (result instanceof Error) throw result;
    return result;
  });
  Object.defineProperty(document, 'execCommand', { value: fn, configurable: true });
  return fn;
}

afterEach(() => {
  if (original) Object.defineProperty(navigator, 'clipboard', original);
  else delete (navigator as { clipboard?: unknown }).clipboard;
  delete (document as { execCommand?: unknown }).execCommand;
  vi.restoreAllMocks();
});

describe('copyText', () => {
  it('uses the Clipboard API when it exists', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    setClipboard({ writeText });
    const exec = stubExecCommand(true);
    expect(await copyText('secret')).toBe(true);
    expect(writeText).toHaveBeenCalledWith('secret');
    expect(exec).not.toHaveBeenCalled();
  });

  it('falls back to execCommand when navigator.clipboard is undefined', async () => {
    setClipboard(undefined);
    let copied = '';
    const exec = vi.fn(() => {
      copied = (document.activeElement as HTMLTextAreaElement).value;
      return true;
    });
    Object.defineProperty(document, 'execCommand', { value: exec, configurable: true });
    expect(await copyText('secret')).toBe(true);
    expect(exec).toHaveBeenCalledWith('copy');
    expect(copied).toBe('secret');
  });

  it('falls back when the Clipboard API rejects', async () => {
    setClipboard({ writeText: vi.fn().mockRejectedValue(new DOMException('denied', 'NotAllowedError')) });
    const exec = stubExecCommand(true);
    expect(await copyText('x')).toBe(true);
    expect(exec).toHaveBeenCalledWith('copy');
  });

  it('returns false without throwing when both paths fail', async () => {
    setClipboard(undefined);
    stubExecCommand(false);
    expect(await copyText('x')).toBe(false);
    stubExecCommand(new Error('boom'));
    expect(await copyText('x')).toBe(false);
  });

  it('removes the textarea afterwards, also on failure', async () => {
    setClipboard(undefined);
    stubExecCommand(true);
    await copyText('x');
    expect(document.querySelector('textarea')).toBeNull();
    stubExecCommand(new Error('boom'));
    await copyText('x');
    expect(document.querySelector('textarea')).toBeNull();
  });
});

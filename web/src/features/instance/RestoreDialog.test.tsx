import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { RestoreDialog } from './RestoreDialog.tsx';
import { restoreCommands } from './restoreCommands.ts';

const NAME = 'pulsix-20261102T030000Z-scheduled.pxbak';

const originalClipboard = Object.getOwnPropertyDescriptor(navigator, 'clipboard');
afterEach(() => {
  vi.restoreAllMocks();
  window.getSelection()?.removeAllRanges();
  // A test may have removed the Clipboard API; put back what was there.
  if (originalClipboard) Object.defineProperty(navigator, 'clipboard', originalClipboard);
  else delete (navigator as { clipboard?: unknown }).clipboard;
});

describe('restoreCommands', () => {
  it('builds the by-name command of a folder destination, always with --replace', () => {
    expect(restoreCommands(NAME, { destination: 'folder', bucket: '', prefix: '' })).toEqual({
      byName: `docker compose run --rm cli restore --replace ${NAME}`,
      fromFile: `docker compose run --rm -T cli restore --replace --yes - < ${NAME}`,
    });
  });

  it('passes the S3 credentials through by name and never by value', () => {
    const { byName } = restoreCommands(NAME, { destination: 's3', bucket: 'firma-backups', prefix: '/pulsix/' });
    expect(byName).toBe(
      'docker compose run --rm -e PULSIX_BACKUP_S3_ENDPOINT -e PULSIX_BACKUP_S3_REGION -e PULSIX_BACKUP_S3_ACCESS_KEY ' +
        '-e PULSIX_BACKUP_S3_SECRET_KEY -e PULSIX_BACKUP_S3_PATH_STYLE cli restore --from s3://firma-backups/pulsix ' +
        `--replace ${NAME}`,
    );
    expect(byName).not.toMatch(/-e [A-Z_0-9]+=/);
  });

  it('leaves the prefix out of the URL when there is none', () => {
    const { byName } = restoreCommands(NAME, { destination: 's3', bucket: 'b', prefix: '' });
    expect(byName).toContain('--from s3://b --replace');
  });

  it('never has a --yes without --replace', () => {
    for (const dest of [
      { destination: 'folder' as const, bucket: '', prefix: '' },
      { destination: 's3' as const, bucket: 'b', prefix: 'p' },
    ]) {
      const { byName, fromFile } = restoreCommands(NAME, dest);
      for (const c of [byName, fromFile]) if (c.includes('--yes')) expect(c).toContain('--replace');
    }
  });
});

describe('RestoreDialog', () => {
  it('shows both commands, the master key note and the stop command', () => {
    render(<RestoreDialog name={NAME} destination={{ destination: 'folder', bucket: '', prefix: '' }} onClose={() => {}} />);
    const dialog = screen.getByRole('dialog', { name: 'Restore a backup' });
    expect(within(dialog).getByText(`docker compose run --rm cli restore --replace ${NAME}`)).toBeInTheDocument();
    expect(
      within(dialog).getByText(`docker compose run --rm -T cli restore --replace --yes - < ${NAME}`),
    ).toBeInTheDocument();
    expect(within(dialog).getByText(/PULSIX_MASTER_KEY/)).toBeInTheDocument();
    expect(within(dialog).getByText(/docker compose stop manager/)).toBeInTheDocument();
  });

  const folder = { destination: 'folder' as const, bucket: '', prefix: '' };
  const COMMAND = `docker compose run --rm cli restore --replace ${NAME}`;

  it('copies a command to the clipboard and says so', async () => {
    const user = userEvent.setup();
    const writeText = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue();
    render(<RestoreDialog name={NAME} destination={folder} onClose={() => {}} />);
    await user.click(screen.getAllByRole('button', { name: 'Copy command' })[0]);
    expect(writeText).toHaveBeenCalledWith(COMMAND);
    expect(await screen.findByText('Copied')).toBeInTheDocument();
    expect(screen.queryByText(/Copying is not available/)).not.toBeInTheDocument();
  });

  it('selects the command and asks for a manual copy where there is no Clipboard API (plain HTTP)', async () => {
    const user = userEvent.setup();
    Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true });
    render(<RestoreDialog name={NAME} destination={folder} onClose={() => {}} />);
    await user.click(screen.getAllByRole('button', { name: 'Copy command' })[0]);
    expect(await screen.findByText(/Copying is not available here/)).toBeInTheDocument();
    expect(window.getSelection()?.toString()).toBe(COMMAND);
    expect(screen.queryByText('Copied')).not.toBeInTheDocument();
  });

  it('falls back the same way when the browser refuses the write', async () => {
    const user = userEvent.setup();
    vi.spyOn(navigator.clipboard, 'writeText').mockRejectedValue(new DOMException('denied', 'NotAllowedError'));
    render(<RestoreDialog name={NAME} destination={folder} onClose={() => {}} />);
    await user.click(screen.getAllByRole('button', { name: 'Copy command' })[1]);
    expect(await screen.findByText(/Copying is not available here/)).toBeInTheDocument();
    expect(window.getSelection()?.toString()).toBe(`docker compose run --rm -T cli restore --replace --yes - < ${NAME}`);
    expect(screen.queryByText('Copied')).not.toBeInTheDocument();
  });
});

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
  const FOLDER = { destination: 'folder' as const, bucket: '', prefix: '', endpoint: '', region: '', pathStyle: false };
  const S3 = { ...FOLDER, destination: 's3' as const };

  it('builds the by-name command of a folder destination, always with --replace', () => {
    expect(restoreCommands(NAME, FOLDER)).toEqual({
      byName: `docker compose run --rm cli restore --replace ${NAME}`,
      fromFile: `docker compose run --rm -T cli restore --replace --yes - < ${NAME}`,
    });
  });

  it('carries the saved endpoint, region and path style, and passes only the two keys through by name', () => {
    const { byName } = restoreCommands(NAME, {
      ...S3,
      bucket: 'firma-backups',
      prefix: '/pulsix/',
      endpoint: 'https://s3.example.net',
      region: 'eu-central-1',
      pathStyle: true,
    });
    expect(byName).toBe(
      'docker compose run --rm -e PULSIX_BACKUP_S3_ENDPOINT=https://s3.example.net -e PULSIX_BACKUP_S3_REGION=eu-central-1 ' +
        '-e PULSIX_BACKUP_S3_PATH_STYLE=true -e PULSIX_BACKUP_S3_ACCESS_KEY -e PULSIX_BACKUP_S3_SECRET_KEY ' +
        `cli restore --from s3://firma-backups/pulsix --replace ${NAME}`,
    );
    expect(byName).not.toMatch(/ACCESS_KEY=|SECRET_KEY=/);
  });

  it('leaves out the endpoint, region and path style that are not set (AWS)', () => {
    const { byName } = restoreCommands(NAME, { ...S3, bucket: 'b' });
    expect(byName).toBe(
      'docker compose run --rm -e PULSIX_BACKUP_S3_ACCESS_KEY -e PULSIX_BACKUP_S3_SECRET_KEY ' +
        `cli restore --from s3://b --replace ${NAME}`,
    );
  });

  it('shell-quotes values that need it', () => {
    const { byName } = restoreCommands(NAME, {
      ...S3,
      bucket: 'b',
      prefix: "team's backups",
      endpoint: 'http://minio:9000/x y',
    });
    expect(byName).toContain(`-e PULSIX_BACKUP_S3_ENDPOINT='http://minio:9000/x y' `);
    expect(byName).toContain(String.raw`--from 's3://b/team'\''s backups' --replace`);
  });

  it('never has a --yes without --replace', () => {
    for (const dest of [FOLDER, { ...S3, bucket: 'b', prefix: 'p' }]) {
      const { byName, fromFile } = restoreCommands(NAME, dest);
      for (const c of [byName, fromFile]) if (c.includes('--yes')) expect(c).toContain('--replace');
    }
  });
});

describe('RestoreDialog', () => {
  const folder = { destination: 'folder' as const, bucket: '', prefix: '', endpoint: '', region: '', pathStyle: false };
  const COMMAND = `docker compose run --rm cli restore --replace ${NAME}`;

  it('shows both commands, the master key note and the stop command', () => {
    render(<RestoreDialog name={NAME} destination={folder} onClose={() => {}} />);
    const dialog = screen.getByRole('dialog', { name: 'Restore a backup' });
    expect(within(dialog).getByText(`docker compose run --rm cli restore --replace ${NAME}`)).toBeInTheDocument();
    expect(
      within(dialog).getByText(`docker compose run --rm -T cli restore --replace --yes - < ${NAME}`),
    ).toBeInTheDocument();
    expect(within(dialog).getByText(/PULSIX_MASTER_KEY/)).toBeInTheDocument();
    expect(within(dialog).getByText(/docker compose down/)).toBeInTheDocument();
    expect(within(dialog).queryByText(/export/i)).not.toBeInTheDocument();
  });

  it('asks to export the two S3 keys first for an S3 destination', () => {
    render(<RestoreDialog name={NAME} destination={{ ...folder, destination: 's3', bucket: 'b' }} onClose={() => {}} />);
    const dialog = screen.getByRole('dialog', { name: 'Restore a backup' });
    expect(
      within(dialog).getByText(/Export PULSIX_BACKUP_S3_ACCESS_KEY and PULSIX_BACKUP_S3_SECRET_KEY in the shell first/),
    ).toBeInTheDocument();
  });

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

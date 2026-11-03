/** Where the backups being restored live: what the S3 restore command needs. */
export interface RestoreDestination {
  destination: 'folder' | 's3';
  bucket: string;
  prefix: string;
}

/** The credentials an S3 restore reads from the operator's environment.
 * Passed through by name (`-e NAME`), never with a value, so a secret never
 * lands on the command line or in the shell history. */
const S3_ENV = [
  'PULSIX_BACKUP_S3_ENDPOINT',
  'PULSIX_BACKUP_S3_REGION',
  'PULSIX_BACKUP_S3_ACCESS_KEY',
  'PULSIX_BACKUP_S3_SECRET_KEY',
  'PULSIX_BACKUP_S3_PATH_STYLE',
];

/**
 * The commands that restore one backup (deploy/compose/README.md, "Restoring").
 * The page is only ever shown for an instance that is in use, so every
 * command replaces the database: `--replace` is always present, and the
 * downloaded-file form adds `--yes` because standard input cannot answer the
 * confirmation. `--yes` never appears without `--replace`.
 */
export function restoreCommands(name: string, dest: RestoreDestination): { byName: string; fromFile: string } {
  const fromFile = `docker compose run --rm -T cli restore --replace --yes - < ${name}`;
  if (dest.destination === 'folder') {
    return { byName: `docker compose run --rm cli restore --replace ${name}`, fromFile };
  }
  const prefix = dest.prefix.replace(/^\/+|\/+$/g, '');
  const url = prefix ? `s3://${dest.bucket}/${prefix}` : `s3://${dest.bucket}`;
  const env = S3_ENV.map((e) => `-e ${e}`).join(' ');
  return { byName: `docker compose run --rm ${env} cli restore --from ${url} --replace ${name}`, fromFile };
}

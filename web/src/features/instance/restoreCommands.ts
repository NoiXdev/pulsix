/** Where the backups being restored live: what the S3 restore command needs. */
export interface RestoreDestination {
  destination: 'folder' | 's3';
  bucket: string;
  prefix: string;
  /** Base URL of the S3-compatible service; empty for AWS S3. */
  endpoint: string;
  region: string;
  pathStyle: boolean;
}

/** The two secrets an S3 restore reads from the operator's shell. Passed
 * through by name (`-e NAME`), never with a value, so a secret never lands
 * on the command line or in the shell history; the dialog asks to export
 * them first. */
const S3_SECRET_ENV = ['PULSIX_BACKUP_S3_ACCESS_KEY', 'PULSIX_BACKUP_S3_SECRET_KEY'];

/** shellQuote leaves a word that needs no quoting as it is and wraps any
 * other in single quotes, so a bucket, prefix or endpoint with spaces or
 * quotes stays one argument. */
export function shellQuote(value: string): string {
  if (/^[A-Za-z0-9_@%+=:,./-]+$/.test(value)) return value;
  return `'${value.replace(/'/g, `'\\''`)}'`;
}

/**
 * The commands that restore one backup (deploy/compose/README.md, "Restoring").
 * The page is only ever shown for an instance that is in use, so every
 * command replaces the database: `--replace` is always present, and the
 * downloaded-file form adds `--yes` because standard input cannot answer the
 * confirmation. `--yes` never appears without `--replace`.
 *
 * An S3 restore carries the saved endpoint, region and path style as values
 * (they are not secret, and without them a self-hosted service would be
 * looked for at AWS) and passes only the two keys through by name.
 */
export function restoreCommands(name: string, dest: RestoreDestination): { byName: string; fromFile: string } {
  const file = shellQuote(name);
  const fromFile = `docker compose run --rm -T cli restore --replace --yes - < ${file}`;
  if (dest.destination === 'folder') {
    return { byName: `docker compose run --rm cli restore --replace ${file}`, fromFile };
  }
  const prefix = dest.prefix.replace(/^\/+|\/+$/g, '');
  const url = prefix ? `s3://${dest.bucket}/${prefix}` : `s3://${dest.bucket}`;
  const env: string[] = [];
  if (dest.endpoint) env.push(`-e PULSIX_BACKUP_S3_ENDPOINT=${shellQuote(dest.endpoint)}`);
  if (dest.region) env.push(`-e PULSIX_BACKUP_S3_REGION=${shellQuote(dest.region)}`);
  if (dest.pathStyle) env.push('-e PULSIX_BACKUP_S3_PATH_STYLE=true');
  for (const e of S3_SECRET_ENV) env.push(`-e ${e}`);
  return {
    byName: `docker compose run --rm ${env.join(' ')} cli restore --from ${shellQuote(url)} --replace ${file}`,
    fromFile,
  };
}

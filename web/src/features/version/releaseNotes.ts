const RELEASE_VERSION = /^\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?$/;

/** The release-notes page for a version, or null for one that is not a
 * published release (`dev`, `local-…`). */
export function releaseNotesUrl(version: string): string | null {
  return RELEASE_VERSION.test(version) ? `https://github.com/NoiXdev/pulsix/releases/tag/v${version}` : null;
}

/**
 * slugify turns a human name into a URL slug.
 *
 * It targets the INTERSECTION of the two patterns the server enforces,
 * because one generator feeds both forms:
 *
 *   organization (internal/setup/service.go)
 *     ^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$   — at most 40 characters,
 *                                                tolerates "--" inside
 *   zone (internal/hosts/service.go)
 *     ^[a-z0-9]+(-[a-z0-9]+)*$                 — single hyphens only,
 *                                                no length cap
 *
 * So: lowercase alphanumerics, single hyphens between segments, trimmed
 * to 40. A generator that only satisfied the looser one would produce a
 * slug the other form silently refuses, which is worse than no generator
 * at all — the field would look filled and the save would fail.
 *
 * Diacritics are stripped rather than transliterated: "Köln" becomes
 * "koln", not "koeln". NFD splits the letter from its mark and the mark
 * is dropped, which keeps the letter — the alternative, dropping the
 * whole character, would give "kln". German transliteration (ö -> oe)
 * would be a third behaviour and is deliberately not attempted: it is
 * right for German and wrong for every other language the same field
 * will see.
 */
export function slugify(name: string): string {
  const folded = name
    .normalize('NFD')
    .replace(/[̀-ͯ]/g, '')
    .toLowerCase();
  const hyphenated = folded
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/-+/g, '-')
    .replace(/^-|-$/g, '');
  // Trimming can leave a trailing hyphen ("acme-gmbh-berlin" cut at 40
  // mid-segment), which both patterns refuse: strip it again after.
  return hyphenated.slice(0, 40).replace(/-$/, '');
}

/** ifMatch is the If-Match header of a write guarded by a revision: the
 * revision quoted, like the ETag the server sends (a stale one answers
 * 412). */
export const ifMatch = (revision: number) => ({ 'If-Match': `"${revision}"` });

/** reviewHref is the path of an organization's review request (spec
 * 2026-11-25 C2); undefined without an id. */
export function reviewHref(org: string, id: string | undefined): string | undefined {
  return id ? `/o/${org}/runs/requests/${id}` : undefined;
}

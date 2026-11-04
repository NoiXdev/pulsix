import { Fragment, type ReactNode } from 'react';
import type { UseQueryResult } from '@tanstack/react-query';
import { useQueryGuard } from '../../../ui/useQueryGuard.tsx';
import { QueryError, QueryLoading } from '../../../ui/QueryState.tsx';
import { ApiError } from '../../../api/client.ts';

/**
 * DefinitionLoader is what a shared-resource form page does before it can
 * show its form, the same for every kind: load the kind's definitions list,
 * let the query guard answer a refusal (a non-admin gets the access-denied
 * page, not a form that can only fail on save), and for `…/$id` find the
 * definition — an id that is not in the list is a 404.
 *
 * The create page loads the list too: it is the access gate, and the answer
 * is cached for the list the page goes back to. `children` gets the
 * definition to edit, or undefined on the create page, and is remounted when
 * that changes, so a form's own state never carries over from another one.
 *
 * `useDefinitions` is the kind's list hook, called here rather than by the
 * page because only this component knows when it is needed.
 */
export function DefinitionLoader<T extends { id: string }>({
  useDefinitions,
  id,
  children,
}: {
  useDefinitions: () => UseQueryResult<{ definitions?: T[] | null }>;
  id?: string;
  children: (definition: T | undefined) => ReactNode;
}) {
  const definitions = useDefinitions();
  const guard = useQueryGuard(definitions);
  if (guard) return guard;
  if (!definitions.data) return <QueryLoading />;
  const definition = id ? (definitions.data.definitions ?? []).find((d) => d.id === id) : undefined;
  if (id && !definition) return <QueryError error={new ApiError(404, null)} />;
  return <Fragment key={definition?.id ?? 'new'}>{children(definition)}</Fragment>;
}

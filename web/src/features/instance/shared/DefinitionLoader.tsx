import { Fragment, type ReactNode } from 'react';
import type { UseQueryResult } from '@tanstack/react-query';
import { useQueryGuard } from '../../../ui/useQueryGuard.tsx';
import { QueryError, QueryLoading } from '../../../ui/QueryState.tsx';
import { ApiError } from '../../../api/client.ts';
import { useMe } from '../../../api/hooks/useMe.ts';

/**
 * DefinitionLoader is what a shared-resource form page does before it can
 * show the form of an existing definition: load the kind's definitions list,
 * let the query guard answer a refusal (a non-admin gets the access-denied
 * page, not a form that can only fail on save) and find the definition — an
 * id that is not in the list is a 404.
 *
 * `children` gets the definition to edit and is remounted when it changes, so
 * a form's own state never carries over from another one. The create page
 * has no list to load: it goes through CreateGate instead.
 *
 * `useDefinitions` is the kind's list hook, called here rather than by the
 * page because only the edit page needs it.
 */
export function DefinitionLoader<T extends { id: string }>({
  useDefinitions,
  id,
  children,
}: {
  useDefinitions: () => UseQueryResult<{ definitions?: T[] | null }>;
  id: string;
  children: (definition: T) => ReactNode;
}) {
  const definitions = useDefinitions();
  const guard = useQueryGuard(definitions);
  if (guard) return guard;
  if (!definitions.data) return <QueryLoading />;
  const definition = (definitions.data.definitions ?? []).find((d) => d.id === id);
  if (!definition) return <QueryError error={new ApiError(404, null)} />;
  return <Fragment key={definition.id}>{children(definition)}</Fragment>;
}

/**
 * CreateGate is what a shared-resource create page does before it shows its
 * form: only an instance administrator may create a definition, which
 * /auth/me already says, so anyone else gets the access-denied page instead
 * of a form that can only fail on save. It loads no list, so a slow or
 * failing list neither delays nor blocks the empty form.
 */
export function CreateGate({ children }: { children: ReactNode }) {
  const me = useMe();
  const guard = useQueryGuard(me);
  if (guard) return guard;
  if (!me.data) return <QueryLoading />;
  if (!me.data.instance_admin) return <QueryError error={new ApiError(403, null)} />;
  return <>{children}</>;
}

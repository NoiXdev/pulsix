import { Fragment } from 'react';

/** RouteChain draws an SSH route as the chain it is: the worker that
 * connects, every jump host on the way, and the login it ends at. */
export function RouteChain({ worker, hops, target, label }: { worker: string; hops: readonly string[]; target: string; label?: string }) {
  const steps = [worker, ...hops, target];
  return (
    <span aria-label={label} className="inline-flex flex-wrap items-center gap-x-1.5 font-mono text-xs">
      {steps.map((step, i) => (
        <Fragment key={`${i}-${step}`}>
          {i > 0 && (
            <span aria-hidden className="text-muted-foreground">
              →
            </span>
          )}
          <span className={i === 0 ? 'text-muted-foreground' : undefined}>{step}</span>
        </Fragment>
      ))}
    </span>
  );
}

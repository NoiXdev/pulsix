import { afterEach, beforeEach, describe, expect, it, vi, type MockInstance } from 'vitest';
import { render, screen } from '@testing-library/react';
import { ErrorBoundary } from './ErrorBoundary.tsx';

/** Boom throws for as long as `explode` says so, and counts how often it
 * was rendered — the only way to tell "the boundary is still holding its
 * fallback" from "the boundary retried and caught again". */
function makeChild() {
  const renders: boolean[] = [];
  return {
    renders,
    Boom: ({ explode }: { explode: boolean }) => {
      renders.push(explode);
      if (explode) throw new Error('boom');
      return <div>child</div>;
    },
  };
}

describe('ErrorBoundary', () => {
  // React logs every caught error itself; so does ErrorBoundary's own
  // componentDidCatch. Neither is a failure, and both would bury the run.
  //
  // Restored in afterEach rather than at the end of each test body: a test
  // that fails before its last line would otherwise leave console.error
  // stubbed for the rest of the file, and the next failure would go quiet.
  // vitest.config.ts sets no restoreMocks, so nothing else undoes it.
  let errors: MockInstance<typeof console.error>;
  beforeEach(() => {
    errors = vi.spyOn(console, 'error').mockImplementation(() => {});
  });
  afterEach(() => {
    errors.mockRestore();
  });

  it('renders its children while nothing throws', () => {
    const { Boom } = makeChild();
    render(
      <ErrorBoundary fallback={<div>fallback</div>}>
        <Boom explode={false} />
      </ErrorBoundary>,
    );
    expect(screen.getByText('child')).toBeInTheDocument();
    expect(screen.queryByText('fallback')).not.toBeInTheDocument();
  });

  it('swaps in the fallback once a child throws', () => {
    const { Boom } = makeChild();
    render(
      <ErrorBoundary fallback={<div>fallback</div>}>
        <Boom explode />
      </ErrorBoundary>,
    );
    expect(screen.getByText('fallback')).toBeInTheDocument();
  });

  it('holds the fallback across re-renders while the reset key is unchanged', () => {
    const { Boom, renders } = makeChild();
    const { rerender } = render(
      <ErrorBoundary fallback={<div>fallback</div>} resetKey="cpu:1h">
        <Boom explode />
      </ErrorBoundary>,
    );
    expect(screen.getByText('fallback')).toBeInTheDocument();
    const attempts = renders.length;

    // A parent re-render with an equal key must NOT retry. This is what a
    // caller passing a freshly built object gets wrong: the key would
    // differ every render, the boundary would retry and re-throw on each
    // one, and the fallback would be the only thing that still looked
    // right.
    rerender(
      <ErrorBoundary fallback={<div>fallback</div>} resetKey="cpu:1h">
        <Boom explode />
      </ErrorBoundary>,
    );

    expect(renders).toHaveLength(attempts);
    expect(screen.getByText('fallback')).toBeInTheDocument();
  });

  it('tries again when the reset key changes', () => {
    const { Boom } = makeChild();
    const { rerender } = render(
      <ErrorBoundary fallback={<div>fallback</div>} resetKey="cpu:1h">
        <Boom explode />
      </ErrorBoundary>,
    );
    expect(screen.getByText('fallback')).toBeInTheDocument();

    rerender(
      <ErrorBoundary fallback={<div>fallback</div>} resetKey="cpu:24h">
        <Boom explode={false} />
      </ErrorBoundary>,
    );

    expect(screen.getByText('child')).toBeInTheDocument();
    expect(screen.queryByText('fallback')).not.toBeInTheDocument();
  });
});

import { Component, type ErrorInfo, type ReactNode } from 'react';

export interface ErrorBoundaryProps {
  /** What to render instead of the subtree once it has thrown. */
  fallback: ReactNode;
  /**
   * A value identifying what the subtree is currently rendering. When it
   * changes, the boundary clears its failed state and tries again.
   *
   * Compared with Object.is, so pass something stable by value — a string
   * built from the inputs, not a freshly built object, which would reset
   * on every render and defeat the boundary entirely.
   */
  resetKey?: unknown;
  children: ReactNode;
}

interface ErrorBoundaryState {
  failed: boolean;
  /** The resetKey this state was last reconciled against. */
  key: unknown;
}

/**
 * ErrorBoundary contains a render-time throw to the subtree it wraps.
 *
 * React offers no hook for this — an error boundary has to be a class
 * (`getDerivedStateFromError`), which is why this one component in `src/ui`
 * is not a function.
 *
 * It exists for the SPA's lazily loaded chunks. `React.lazy` re-throws its
 * import rejection **during render**, and with no boundary in the tree the
 * throw travels up to TanStack Router, whose default `ErrorComponent`
 * replaces the whole route — chrome included — with an unstyled "Something
 * went wrong!". The realistic trigger for a self-hosted product is an
 * operator sitting on a page across a Pulsix deploy: every build changes
 * the chunk hashes, the one the loaded page remembers 404s, and the first
 * click that needs it would otherwise blank the page they were watching.
 *
 * Scope it as tightly as the thing that can fail — around the `Suspense`
 * for one lazy component, not around a page — so a failure degrades that
 * one widget and leaves everything around it usable.
 *
 * **Tight scope is not recovery.** A boundary that never clears its failed
 * state wedges its subtree until something unmounts it, and "something
 * unmounts it" is not a guarantee: a caller whose loading state disappears
 * once its data is cached stops producing that gap, and the fallback then
 * outlives the condition that caused it. Hence `resetKey`. For the
 * chunk-404 case resetting changes nothing — `React.lazy` caches the
 * rejected promise, so the retry re-throws at once and "reload the page"
 * stays the honest advice — but for a throw from an already-mounted
 * subtree it is the difference between one bad input and a dead panel.
 */
export class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  constructor(props: ErrorBoundaryProps) {
    super(props);
    this.state = { failed: false, key: props.resetKey };
  }

  static getDerivedStateFromError(): Partial<ErrorBoundaryState> {
    return { failed: true };
  }

  /** Runs before every render, including the one that follows a caught
   * throw — where the key is unchanged, so the failed state survives. */
  static getDerivedStateFromProps(props: ErrorBoundaryProps, state: ErrorBoundaryState): ErrorBoundaryState | null {
    if (Object.is(props.resetKey, state.key)) return null;
    return { failed: false, key: props.resetKey };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // The operator sees the fallback; whoever is reading the console during
    // a deploy sees which chunk did not arrive.
    console.error('ErrorBoundary caught', error, info.componentStack);
  }

  render() {
    return this.state.failed ? this.props.fallback : this.props.children;
  }
}

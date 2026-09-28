import { Component, type ErrorInfo, type ReactNode } from 'react';

export interface ErrorBoundaryProps {
  /** What to render instead of the subtree once it has thrown. */
  fallback: ReactNode;
  children: ReactNode;
}

interface ErrorBoundaryState {
  failed: boolean;
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
 * `React.lazy` caches the rejected promise, so that state survives until a
 * reload.
 *
 * Scope it as tightly as the thing that can fail — around the `Suspense`
 * for one lazy component, not around a page — so a failure degrades that
 * one widget and leaves everything around it usable.
 */
export class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  state: ErrorBoundaryState = { failed: false };

  static getDerivedStateFromError(): ErrorBoundaryState {
    return { failed: true };
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

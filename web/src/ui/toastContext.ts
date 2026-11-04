import { createContext, useContext } from 'react';

export interface ToastContextValue {
  /** `warn` is for an action that went through with a side effect that
   * did not (a notice mail that could not be sent). */
  show: (text: string, tone?: 'default' | 'warn' | 'destructive') => void;
}

export const ToastContext = createContext<ToastContextValue | null>(null);

export function useToast(): ToastContextValue {
  const ctx = useContext(ToastContext);
  if (!ctx) throw new Error('useToast must be used within a ToastProvider');
  return ctx;
}

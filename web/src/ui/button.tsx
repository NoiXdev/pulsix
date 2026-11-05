import type { ComponentProps } from 'react';
import { buttonClass, type ButtonVariant } from './buttonStyles.ts';

export interface ButtonProps extends ComponentProps<'button'> {
  variant?: ButtonVariant;
}

/** Button — see design/mockups/build.mjs's `btn()` generator for the exact
 * shape (34px tall, 6px radius, primary = accent fill with ink text). */
export function Button({ variant = 'default', className, ...props }: ButtonProps) {
  return <button className={buttonClass(variant, className)} {...props} />;
}

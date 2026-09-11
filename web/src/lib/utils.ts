/**
 * cn joins conditional class names, dropping falsy values. This is a
 * hand-rolled stand-in for the clsx + tailwind-merge combo shadcn
 * components usually pull in — Pulsix's own primitives (src/ui) never pass
 * conflicting utilities for the same property to the same element, so
 * there is nothing for tailwind-merge to deduplicate, and pulling in two
 * more dependencies for that would not earn its keep.
 */
export function cn(...inputs: Array<string | false | null | undefined>): string {
  return inputs.filter(Boolean).join(' ');
}

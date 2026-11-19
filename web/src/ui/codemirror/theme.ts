import { EditorView } from '@codemirror/view';
import { HighlightStyle, syntaxHighlighting } from '@codemirror/language';
import { tags } from '@lezer/highlight';

/**
 * The editor's look, built only from the app's CSS variables (styles/):
 * whatever theme sets them, the editor follows without a fixed palette of
 * its own. The syntax hues are the JSON viewer's (keys in the brand
 * colour, booleans and null in the maintenance violet) with strings and
 * numbers taken from the two `--px-syntax-*` tokens; the state colours
 * stay reserved for states, except crit for what does not parse.
 */
const tint = (color: string, pct: number) => `color-mix(in srgb, ${color} ${pct}%, transparent)`;

export const editorTheme = EditorView.theme({
  '&': {
    color: 'var(--foreground)',
    backgroundColor: 'var(--background)',
    fontSize: '12px',
    height: '100%',
  },
  '&.cm-focused': { outline: 'none' },
  '.cm-scroller': { fontFamily: 'var(--px-font-mono)', lineHeight: '1.6' },
  '.cm-content': { caretColor: 'var(--px-accent)', padding: '8px 0' },
  '.cm-cursor, .cm-dropCursor': { borderLeftColor: 'var(--px-accent)' },
  '&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection': {
    backgroundColor: tint('var(--px-accent)', 22),
  },
  '.cm-activeLine': { backgroundColor: tint('var(--px-accent)', 5) },
  '.cm-activeLineGutter': { backgroundColor: tint('var(--px-accent)', 8), color: 'var(--foreground)' },
  '.cm-gutters': {
    backgroundColor: 'var(--card)',
    color: 'var(--muted-foreground)',
    borderRight: '1px solid var(--border)',
  },
  '.cm-lineNumbers .cm-gutterElement': { padding: '0 8px 0 12px' },
  '.cm-foldGutter .cm-gutterElement': { padding: '0 4px', cursor: 'pointer' },
  '.cm-foldPlaceholder': {
    backgroundColor: 'var(--card)',
    border: '1px solid var(--border)',
    color: 'var(--muted-foreground)',
  },
  '&.cm-focused .cm-matchingBracket': { backgroundColor: tint('var(--px-accent)', 25), outline: '1px solid var(--px-accent-dim)' },
  '&.cm-focused .cm-nonmatchingBracket': { backgroundColor: tint('var(--px-crit)', 25) },
  '.cm-searchMatch': { backgroundColor: tint('var(--px-warn)', 22), outline: `1px solid ${tint('var(--px-warn)', 50)}` },
  '.cm-searchMatch.cm-searchMatch-selected': { backgroundColor: tint('var(--px-warn)', 45) },
  '.cm-selectionMatch': { backgroundColor: tint('var(--px-accent)', 12) },
  '.cm-panels': { backgroundColor: 'var(--card)', color: 'var(--foreground)' },
  '.cm-panels.cm-panels-top': { borderBottom: '1px solid var(--border)' },
  '.cm-panels.cm-panels-bottom': { borderTop: '1px solid var(--border)' },
  '.cm-panel input, .cm-panel button, .cm-textfield': {
    backgroundColor: 'var(--background)',
    color: 'var(--foreground)',
    border: '1px solid var(--border)',
    borderRadius: '4px',
    fontSize: '11px',
  },
  // CodeMirror's base theme paints its buttons with a fixed light (or dark)
  // gradient that sits on top of any background colour: switched off, or
  // the search panel's buttons are light text on light grey.
  '.cm-button, .cm-panel button': {
    backgroundImage: 'none',
    '&:active': { backgroundImage: 'none', backgroundColor: tint('var(--px-accent)', 20) },
  },
  '.cm-panel button:hover': { borderColor: 'var(--px-accent)' },
  '.cm-tooltip': {
    backgroundColor: 'var(--popover)',
    color: 'var(--popover-foreground)',
    border: '1px solid var(--border)',
    borderRadius: '6px',
  },
  '.cm-tooltip-section:not(:first-child)': { borderTop: '1px solid var(--border)' },
  '.cm-specialChar': { color: 'var(--px-crit)' },
  // The lint layer's own colours are fixed hex values; every one the editor
  // can show (an error is the only severity it is given) comes from tokens.
  '.cm-diagnostic-error': { borderLeftColor: 'var(--px-crit)' },
  '.cm-lintRange-error': {
    backgroundImage: 'none',
    textDecoration: 'underline wavy var(--px-crit)',
    textUnderlineOffset: '3px',
  },
  '.cm-lintRange-active': { backgroundColor: tint('var(--px-crit)', 15) },
  '.cm-lintPoint-error:after, .cm-lintPoint:after': { borderBottomColor: 'var(--px-crit)' },
  '.cm-lint-marker-error': {
    content: 'normal',
    boxSizing: 'border-box',
    width: '0.6em',
    height: '0.6em',
    margin: '0.2em',
    borderRadius: '50%',
    backgroundColor: 'var(--px-crit)',
  },
  '.cm-placeholder': { color: 'var(--muted-foreground)' },
});

const highlightStyle = HighlightStyle.define([
  { tag: [tags.propertyName, tags.definition(tags.propertyName)], color: 'var(--px-accent)' },
  { tag: [tags.string, tags.special(tags.string)], color: 'var(--px-syntax-string)' },
  { tag: [tags.number, tags.integer, tags.float], color: 'var(--px-syntax-number)' },
  { tag: [tags.bool, tags.null, tags.atom, tags.keyword], color: 'var(--px-maintenance)' },
  { tag: [tags.labelName, tags.typeName, tags.tagName, tags.special(tags.name)], color: 'var(--px-syntax-number)' },
  { tag: [tags.comment, tags.lineComment, tags.blockComment], color: 'var(--muted-foreground)', fontStyle: 'italic' },
  { tag: [tags.punctuation, tags.separator, tags.bracket, tags.squareBracket, tags.brace, tags.meta, tags.operator], color: 'var(--muted-foreground)' },
  { tag: tags.invalid, color: 'var(--px-crit)' },
]);

export const editorHighlighting = syntaxHighlighting(highlightStyle);

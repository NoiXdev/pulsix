import opentype from 'opentype.js';
import fs from 'node:fs';
import path from 'node:path';

const OUT = process.argv[2];
const font = opentype.loadSync(path.join(process.argv[3], 'fonts/ttf/JetBrainsMono-ExtraBold.ttf'));

// ---------- tokens ----------
const T = {
  ink: '#0E1117', surface: '#161B26', border: '#1E2336',
  text: '#F2F4F8', muted: '#6B7280', paper: '#F6F4EF',
  accent: '#00E5FF', accentDim: '#009CB2',
  ok: '#34D399', warn: '#FBBF24', crit: '#F87171', unknown: '#6B7280',
};

// ---------- mark (64 grid) ----------
const HEX = 'M32 5 L55 18.5 V45.5 L32 59 L9 45.5 V18.5 Z';
const PULSE = 'M14 32 H23 L28 20 L35 44 L40 32 H50';
const mark = (fg, ac, {x = 0, y = 0, s = 1} = {}) =>
  `<g transform="translate(${x} ${y}) scale(${s})" fill="none">` +
  `<path d="${HEX}" stroke="${fg}" stroke-width="4" stroke-linejoin="round"/>` +
  `<path d="${PULSE}" stroke="${ac}" stroke-width="4.5" stroke-linecap="round" stroke-linejoin="round"/></g>`;

// ---------- wordmark as outlines ----------
const SIZE = 100;                     // font size units
const TRACK = -0.045 * SIZE;          // letter-spacing
const scale = SIZE / font.unitsPerEm;
function layout(text) {
  let x = 0; const glyphs = [];
  for (const ch of text) {
    const g = font.charToGlyph(ch);
    glyphs.push({ ch, x, path: g.getPath(x, 0, SIZE).toPathData(2) });
    x += g.advanceWidth * scale + TRACK;
  }
  return { glyphs, width: x - TRACK };
}
const wm = layout('pulsix');
const capH = font.tables.os2.sCapHeight * scale;        // cap height
const xH = font.tables.os2.sxHeight * scale;
const asc = 0.80 * SIZE;                                 // 'l' ascender approx (measured below)
const lBox = font.charToGlyph('l').getPath(0, 0, SIZE).getBoundingBox();
const pBox = font.charToGlyph('p').getPath(0, 0, SIZE).getBoundingBox();
const top = lBox.y1, bottom = pBox.y2;                   // negative top (y up), positive descender
const wordmarkPaths = (fg, ac) =>
  wm.glyphs.map(g => `<path d="${g.path}" fill="${g.ch === 'x' ? ac : fg}"/>`).join('\n  ');
// trim: measure actual ink bounds
const first = font.charToGlyph('p').getPath(0, 0, SIZE).getBoundingBox().x1;
const lastG = wm.glyphs[wm.glyphs.length - 1];
const lastBox = font.charToGlyph('x').getPath(lastG.x, 0, SIZE).getBoundingBox();
const inkW = lastBox.x2 - first;
const inkH = bottom - top;

function svg(w, h, body, extra = '') {
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${w} ${h}" width="${w}" height="${h}"${extra}>\n${body}\n</svg>\n`;
}
const r2 = n => Math.round(n * 100) / 100;

// wordmark file: viewBox tight to ink, padding 4
const PAD = 4;
const wordmark = (fg, ac) => svg(r2(inkW + 2 * PAD), r2(inkH + 2 * PAD),
  `  <!-- Pulsix wordmark · JetBrains Mono ExtraBold, outlined, tracking -0.045em -->\n` +
  `  <g transform="translate(${r2(PAD - first)} ${r2(PAD - top)})">\n  ${wordmarkPaths(fg, ac)}\n  </g>`);

// lockup: mark spans the full ink height of the wordmark (ascender to descender)
const mS = inkH / 54;                          // hex spans y 5..59 = 54 units
const markW = 64 * mS;
const gap = 0.22 * SIZE;
const lockW = markW + gap + inkW, lockH = inkH;
const lockup = (fg, ac) => svg(r2(lockW + 2 * PAD), r2(lockH + 2 * PAD),
  `  <!-- Pulsix lockup: mark + wordmark, horizontal -->\n` +
  `  ${mark(fg, ac, { x: r2(PAD), y: r2(PAD - 5 * mS), s: r2(mS) })}\n` +
  `  <g transform="translate(${r2(PAD + markW + gap - first)} ${r2(PAD - top)})">\n  ${wordmarkPaths(fg, ac)}\n  </g>`);

// banner 1000x320 like the org wordmark
const banner = () => {
  const bs = 1.42;                       // wordmark scale
  const bx = 86;                         // left edge of the mark
  const bTop = 78;                       // top of ink
  const bh = inkH * bs, bms = bh / 54;   // mark scale for full ink height
  const bmw = 64 * bms;
  const wx = bx + bmw + 0.22 * SIZE * bs;
  const baseline = bTop - top * bs;
  return svg(1000, 320,
    `  <rect width="1000" height="320" fill="${T.ink}"/>\n` +
    `  <rect x="48" y="${bTop}" width="6" height="${r2(bh)}" fill="${T.accent}"/>\n` +
    `  ${mark(T.text, T.accent, { x: bx, y: r2(bTop - 5 * bms), s: r2(bms) })}\n` +
    `  <g transform="translate(${r2(wx - first * bs)} ${r2(baseline)}) scale(${bs})">\n  ${wordmarkPaths(T.text, T.accent)}\n  </g>\n` +
    `  <text x="${r2(wx)}" y="${r2(bTop + bh + 44)}" font-family="'JetBrains Mono','Fira Code',ui-monospace,Menlo,monospace" font-size="14" font-weight="600" fill="${T.muted}" letter-spacing="8">SERVER MANAGEMENT  ·  MONITORING</text>`);
};

// app icon 1024: cyan tile, ink mark ; avatar 1024: ink tile, light mark
const tile = (bg, fg, ac, radius) => svg(1024, 1024,
  `  <rect width="1024" height="1024" rx="${radius}" fill="${bg}"/>\n  ${mark(fg, ac, { x: 128, y: 128, s: 12 })}`);

const files = {
  'logo/pulsix-mark.svg': svg(64, 64, `  <!-- Pulsix mark · for dark backgrounds -->\n  ${mark(T.text, T.accent)}`),
  'logo/pulsix-mark-light.svg': svg(64, 64, `  <!-- Pulsix mark · for light backgrounds -->\n  ${mark(T.ink, T.accentDim)}`),
  'logo/pulsix-mark-mono.svg': svg(64, 64, `  <!-- Pulsix mark · single colour, inherits currentColor -->\n  ${mark('currentColor', 'currentColor')}`),
  'logo/pulsix-wordmark.svg': wordmark(T.text, T.accent),
  'logo/pulsix-wordmark-light.svg': wordmark(T.ink, T.accentDim),
  'logo/pulsix-lockup.svg': lockup(T.text, T.accent),
  'logo/pulsix-lockup-light.svg': lockup(T.ink, T.accentDim),
  'logo/pulsix-banner.svg': banner(),
  'logo/pulsix-app-icon.svg': tile(T.accent, T.ink, T.ink, 224),
  'logo/pulsix-avatar.svg': tile(T.ink, T.text, T.accent, 0),
  'favicon/favicon.svg': svg(64, 64, `  <rect width="64" height="64" rx="14" fill="${T.ink}"/>\n  ${mark(T.text, T.accent, { x: 6, y: 6, s: 0.8125 })}`),
};
for (const [f, c] of Object.entries(files)) fs.writeFileSync(path.join(OUT, f), c);

// tokens
fs.writeFileSync(path.join(OUT, 'tokens.json'), JSON.stringify({
  name: 'Pulsix', org: 'NoiXdev',
  color: T,
  font: { mono: "'JetBrains Mono', 'Fira Code', ui-monospace, Menlo, monospace", sans: "Inter, system-ui, sans-serif" },
  wordmark: { font: 'JetBrains Mono ExtraBold', tracking: '-0.045em', accentGlyph: 'x' },
}, null, 2) + '\n');
fs.writeFileSync(path.join(OUT, 'tokens.css'), `/* Pulsix brand tokens · dark-first */
:root {
  --px-ink: ${T.ink};
  --px-surface: ${T.surface};
  --px-border: ${T.border};
  --px-text: ${T.text};
  --px-muted: ${T.muted};
  --px-paper: ${T.paper};
  --px-accent: ${T.accent};
  --px-accent-dim: ${T.accentDim};
  --px-ok: ${T.ok};
  --px-warn: ${T.warn};
  --px-crit: ${T.crit};
  --px-unknown: ${T.unknown};
  --px-font-mono: 'JetBrains Mono', 'Fira Code', ui-monospace, Menlo, monospace;
  --px-font-sans: Inter, system-ui, sans-serif;
}
`);
console.log('wordmark ink', r2(inkW), 'x', r2(inkH), '| lockup', r2(lockW), 'x', r2(lockH));

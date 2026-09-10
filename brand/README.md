# Pulsix brand assets

Pulsix is a NoiXdev product. The brand extends the org system
(JetBrains Mono ExtraBold wordmark, cyan accent on ink) with its own mark.

## Mark

A hexagon (the host / node) crossed by a pulse line (heartbeat, monitoring).
Two strokes, two colours. Holds up down to 16 px.

| File | Use |
|------|-----|
| `logo/pulsix-mark.svg` | dark backgrounds (`#F2F4F8` + `#00E5FF`) |
| `logo/pulsix-mark-light.svg` | light backgrounds (`#0E1117` + `#009CB2`) |
| `logo/pulsix-mark-mono.svg` | single colour, inherits `currentColor` |

## Wordmark and lockups

The wordmark is JetBrains Mono ExtraBold, tracking `-0.045em`, with the
final `x` in the accent colour (mirrors the `X` in NoiXdev). Text is
outlined to paths, so no font dependency at render time.

| File | Use |
|------|-----|
| `logo/pulsix-wordmark.svg` / `-light.svg` | wordmark only |
| `logo/pulsix-lockup.svg` / `-light.svg` | mark + wordmark, horizontal |
| `logo/pulsix-banner.svg` | 1000×320 README / social banner on ink |
| `logo/pulsix-app-icon.svg` | 1024 rounded tile, ink mark on cyan |
| `logo/pulsix-avatar.svg` | 1024 square, GitHub org / Docker Hub avatar |
| `favicon/` | `favicon.svg`, 16/32 PNG, apple-touch-icon, PWA icons |

PNG exports live in `logo/png/`.

## Tokens

`tokens.css` (CSS custom properties, `--px-*`) and `tokens.json`.

| Token | Value | Role |
|-------|-------|------|
| ink | `#0E1117` | page background (dark-first) |
| surface | `#161B26` | cards, panels |
| border | `#1E2336` | hairlines |
| text | `#F2F4F8` | primary text |
| muted | `#6B7280` | secondary text |
| paper | `#F6F4EF` | light theme background |
| accent | `#00E5FF` | brand accent, primary actions, selection |
| accent-dim | `#009CB2` | accent on light backgrounds, hover |
| ok / warn / crit / unknown | `#34D399` / `#FBBF24` / `#F87171` / `#6B7280` | monitoring states only, never decorative |

Type: JetBrains Mono for wordmark, hostnames, IDs and values; Inter for UI copy.

## Regenerating

```bash
# needs JetBrains Mono ExtraBold at <fontdir>/fonts/ttf/ and opentype.js
node tools/generate-logos.mjs ./ <fontdir>
rsvg-convert -w 1200 logo/pulsix-lockup.svg -o logo/png/pulsix-lockup.png
```

import fs from 'node:fs';
const T = { ink:'#0E1117', surface:'#161B26', border:'#1E2336', text:'#F2F4F8', muted:'#6B7280', dim:'#9CA3AF',
  accent:'#00E5FF', accentDim:'#009CB2', ok:'#34D399', warn:'#FBBF24', crit:'#F87171', unknown:'#6B7280' };
const MONO = "'JetBrains Mono', ui-monospace, Menlo, monospace";

// seeded rng for plausible curves
let seed = 7; const rnd = () => { seed = (seed * 16807) % 2147483647; return (seed - 1) / 2147483646; };
function series(n, base, amp, drift = 0) { const out = []; let v = base; for (let i = 0; i < n; i++) { v += (rnd() - 0.5) * amp + drift; v = Math.max(0, Math.min(100, v)); out.push(v); } return out; }
function spike(arr, at, h, w = 4) { for (let i = 0; i < w; i++) if (arr[at + i] !== undefined) arr[at + i] = Math.min(100, arr[at + i] + h * (1 - i / w)); return arr; }
function linePath(data, w, h, max = 100) { return data.map((v, i) => `${i === 0 ? 'M' : 'L'}${(i / (data.length - 1) * w).toFixed(1)} ${(h - v / max * h).toFixed(1)}`).join(' '); }
function chart({ w, h, lines, max = 100, unit = '%', grid = 4, labels = ['-24h', '-18h', '-12h', '-6h', 'now'] }) {
  const gl = Array.from({ length: grid + 1 }, (_, i) => { const y = (i / grid * h).toFixed(1); return `<line x1="0" y1="${y}" x2="${w}" y2="${y}" stroke="${T.border}" stroke-width="1"/>`; }).join('');
  const gt = Array.from({ length: grid + 1 }, (_, i) => `<text x="-8" y="${(i / grid * h + 4).toFixed(1)}" text-anchor="end" font-size="10" fill="${T.muted}" font-family="${MONO}">${Math.round(max - i / grid * max)}${unit}</text>`).join('');
  const xl = labels.map((l, i) => `<text x="${(i / (labels.length - 1) * w).toFixed(1)}" y="${h + 16}" text-anchor="${i === 0 ? 'start' : i === labels.length - 1 ? 'end' : 'middle'}" font-size="10" fill="${T.muted}" font-family="${MONO}">${l}</text>`).join('');
  const ls = lines.map(({ data, color, fill }) => `${fill ? `<path d="${linePath(data, w, h, max)} L${w} ${h} L0 ${h} Z" fill="${color}" opacity="0.10"/>` : ''}<path d="${linePath(data, w, h, max)}" fill="none" stroke="${color}" stroke-width="1.5" stroke-linejoin="round"/>`).join('');
  return `<svg width="${w + 44}" height="${h + 24}" viewBox="-44 -4 ${w + 48} ${h + 28}" xmlns="http://www.w3.org/2000/svg" style="display: block; overflow: visible">${gl}${gt}${ls}${xl}</svg>`;
}
function spark(data, w = 96, h = 24, color = T.accent, max = 100) { return `<svg width="${w}" height="${h}" viewBox="0 0 ${w} ${h}" style="display: block"><path d="${linePath(data, w, h, max)}" fill="none" stroke="${color}" stroke-width="1.5" stroke-linejoin="round"/></svg>`; }

// icons (16px stroke)
const I = (d, s = 16) => `<svg width="${s}" height="${s}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" style="flex-shrink: 0">${d}</svg>`;
const ic = {
  grid: I('<rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/>'),
  server: I('<rect x="3" y="4" width="18" height="6" rx="1.5"/><rect x="3" y="14" width="18" height="6" rx="1.5"/><path d="M7 7h.01M7 17h.01"/>'),
  layers: I('<path d="M12 3l9 5-9 5-9-5 9-5z"/><path d="M3 13l9 5 9-5"/>'),
  play: I('<path d="M6 4l14 8-14 8V4z"/>'),
  list: I('<path d="M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01"/>'),
  activity: I('<path d="M3 12h4l3-8 4 16 3-8h4"/>'),
  bell: I('<path d="M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9"/><path d="M10 21h4"/>'),
  cpu: I('<rect x="5" y="5" width="14" height="14" rx="2"/><rect x="9" y="9" width="6" height="6"/><path d="M9 2v3M15 2v3M9 19v3M15 19v3M2 9h3M2 15h3M19 9h3M19 15h3"/>'),
  code: I('<path d="M8 7l-5 5 5 5M16 7l5 5-5 5"/>'),
  settings: I('<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z"/>'),
  search: I('<circle cx="11" cy="11" r="7"/><path d="M20 20l-3.5-3.5"/>'),
  chevron: I('<path d="M6 9l6 6 6-6"/>', 14),
  chevronR: I('<path d="M9 6l6 6-6 6"/>', 14),
  globe: I('<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3a14 14 0 0 1 0 18M12 3a14 14 0 0 0 0 18"/>'),
  check: I('<path d="M5 12l5 5 9-10"/>', 14),
  terminal: I('<path d="M4 17l6-5-6-5M12 19h8"/>'),
  key: I('<circle cx="8" cy="15" r="4"/><path d="M11 12l9-9M17 6l3 3M14 9l2 2"/>'),
  disk: I('<circle cx="12" cy="12" r="9"/><circle cx="12" cy="12" r="2"/>'),
  git: I('<circle cx="6" cy="6" r="2.5"/><circle cx="6" cy="18" r="2.5"/><circle cx="18" cy="8" r="2.5"/><path d="M6 8.5v7M18 10.5a6 6 0 0 1-6 6H8.5"/>'),
  more: I('<circle cx="5" cy="12" r="1.2"/><circle cx="12" cy="12" r="1.2"/><circle cx="19" cy="12" r="1.2"/>'),
  clock: I('<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>', 14),
  user: I('<circle cx="12" cy="8" r="4"/><path d="M4 21a8 8 0 0 1 16 0"/>'),
};
const mark = (s = 22) => `<svg width="${s}" height="${s}" viewBox="0 0 64 64" fill="none" style="flex-shrink: 0"><path d="M32 5 L55 18.5 V45.5 L32 59 L9 45.5 V18.5 Z" stroke="${T.text}" stroke-width="4" stroke-linejoin="round"/><path d="M14 32 H23 L28 20 L35 44 L40 32 H50" stroke="${T.accent}" stroke-width="4.5" stroke-linecap="round" stroke-linejoin="round"/></svg>`;

const dot = (c, s = 8) => `<span style="display: inline-block; width: ${s}px; height: ${s}px; border-radius: 50%; background: ${c}; flex-shrink: 0"></span>`;
const pill = (label, c) => `<span style="display: inline-flex; align-items: center; gap: 6px; padding: 3px 9px; border-radius: 999px; font-size: 11px; font-weight: 600; color: ${c}; background: ${c}1A; border: 1px solid ${c}33">${dot(c, 6)}${label}</span>`;
const tag = (label) => `<span style="display: inline-flex; align-items: center; padding: 2px 8px; border-radius: 4px; font-size: 11px; font-family: ${MONO}; color: ${T.dim}; background: ${T.ink}; border: 1px solid ${T.border}">${label}</span>`;
const btn = (label, primary = false, icon = '') => `<div style="display: inline-flex; align-items: center; gap: 8px; height: 34px; padding: 0 14px; border-radius: 6px; font-size: 13px; font-weight: 600; cursor: pointer; ${primary ? `background: ${T.accent}; color: ${T.ink}` : `border: 1px solid ${T.border}; color: ${T.text}; background: ${T.surface}`}">${icon}${label}</div>`;
const card = (title, body, extra = '', style = '') => `<div style="display: flex; flex-direction: column; background: ${T.surface}; border: 1px solid ${T.border}; border-radius: 10px; overflow: hidden; ${style}">
  <div style="display: flex; align-items: center; justify-content: space-between; padding: 12px 16px; border-bottom: 1px solid ${T.border}"><div style="font-size: 13px; font-weight: 600; color: ${T.text}">${title}</div><div style="display: flex; align-items: center; gap: 8px; font-size: 12px; color: ${T.muted}">${extra}</div></div>
  ${body}
</div>`;

// ---------- shell ----------
function shell({ active, crumbs, content, height = 900 }) {
  const nav = [['Dashboard', 'grid'], ['Hosts', 'server'], ['Groups', 'layers'], ['Playbooks', 'play'], ['Jobs', 'list'], ['Monitoring', 'activity'], ['Alerts', 'bell', '7'], ['Workers', 'cpu'], ['API Explorer', 'code'], ['Settings', 'settings']];
  const navHtml = nav.map(([l, i, badge]) => `<div style="display: flex; align-items: center; gap: 10px; height: 36px; padding: 0 12px; border-radius: 6px; font-size: 13px; ${l === active ? `background: ${T.ink}; color: ${T.text}; font-weight: 600; box-shadow: inset 2px 0 0 ${T.accent}` : `color: ${T.dim}; font-weight: 500`}">${ic[i]}<span style="flex-grow: 1">${l}</span>${badge ? `<span style="font-size: 11px; font-family: ${MONO}; color: ${T.crit}; background: ${T.crit}1A; padding: 1px 7px; border-radius: 999px">${badge}</span>` : ''}</div>`).join('\n      ');
  return `<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <script src="./support.js"></script>
</head>
<body>
<x-dc>
<helmet>
  <link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@500;600;700;800&family=Inter:wght@400;500;600&display=swap">
  <style>
    body { margin: 0; font-family: Inter, system-ui, sans-serif; -webkit-font-smoothing: antialiased; }
    a { color: ${T.accent}; } a:hover { color: ${T.accentDim}; }
  </style>
</helmet>
<div style="width: 1440px; height: ${height}px; display: flex; background: ${T.ink}; color: ${T.text}; font-size: 13px; overflow: hidden">
  <div style="width: 232px; flex-shrink: 0; display: flex; flex-direction: column; background: ${T.surface}; border-right: 1px solid ${T.border}">
    <div style="display: flex; align-items: center; gap: 10px; height: 56px; padding: 0 16px; border-bottom: 1px solid ${T.border}">
      ${mark(24)}<span style="font-family: ${MONO}; font-weight: 800; font-size: 17px; letter-spacing: -0.04em">pulsi<span style="color: ${T.accent}">x</span></span>
    </div>
    <div style="display: flex; align-items: center; justify-content: space-between; margin: 12px 12px 4px; padding: 8px 10px; border-radius: 6px; background: ${T.ink}; border: 1px solid ${T.border}">
      <div style="display: flex; flex-direction: column; gap: 1px"><span style="font-size: 10px; color: ${T.muted}; text-transform: uppercase; letter-spacing: 0.08em">Organization</span><span style="font-size: 13px; font-weight: 600">Acme Hosting</span></div>${ic.chevron}
    </div>
    <div style="display: flex; flex-direction: column; gap: 2px; padding: 8px 12px">
      ${navHtml}
    </div>
    <div style="margin-top: auto; display: flex; align-items: center; gap: 10px; padding: 14px 16px; border-top: 1px solid ${T.border}">
      <div style="width: 28px; height: 28px; border-radius: 50%; background: ${T.accentDim}; display: flex; align-items: center; justify-content: center; font-size: 11px; font-weight: 700; color: ${T.ink}">TR</div>
      <div style="display: flex; flex-direction: column; flex-grow: 1"><span style="font-size: 12px; font-weight: 600">Tim R.</span><span style="font-size: 11px; color: ${T.muted}">Owner · passkey</span></div>${ic.more}
    </div>
  </div>
  <div style="flex-grow: 1; display: flex; flex-direction: column; min-width: 0">
    <div style="display: flex; align-items: center; gap: 16px; height: 56px; padding: 0 24px; border-bottom: 1px solid ${T.border}; flex-shrink: 0">
      <div style="display: flex; align-items: center; gap: 8px; font-size: 13px; color: ${T.muted}">${crumbs}</div>
      <div style="margin-left: auto; display: flex; align-items: center; gap: 10px; width: 300px; height: 34px; padding: 0 12px; border-radius: 6px; background: ${T.surface}; border: 1px solid ${T.border}; color: ${T.muted}">${ic.search}<span style="flex-grow: 1">Search hosts, groups, jobs…</span><span style="font-family: ${MONO}; font-size: 11px; padding: 1px 6px; border: 1px solid ${T.border}; border-radius: 4px">⌘K</span></div>
      <div style="display: flex; align-items: center; gap: 8px; height: 34px; padding: 0 12px; border-radius: 6px; background: ${T.surface}; border: 1px solid ${T.border}; color: ${T.text}">${ic.globe}<span>All zones</span>${ic.chevron}</div>
      <div style="display: flex; align-items: center; justify-content: center; width: 34px; height: 34px; border-radius: 6px; background: ${T.surface}; border: 1px solid ${T.border}; color: ${T.dim}; position: relative">${ic.bell}<span style="position: absolute; top: 7px; right: 7px; width: 7px; height: 7px; border-radius: 50%; background: ${T.crit}; border: 2px solid ${T.surface}"></span></div>
    </div>
    <div style="flex-grow: 1; overflow: hidden; padding: 24px; display: flex; flex-direction: column; gap: 20px">
${content}
    </div>
  </div>
</div>
</x-dc>
</body>
</html>
`;
}

// ---------- DASHBOARD ----------
const fleetCpu = series(96, 32, 6); spike(fleetCpu, 60, 30, 6);
const fleetMem = series(96, 58, 3, 0.05);
const stat = (label, value, sub, color = T.text, sparkData) => `<div style="display: flex; flex-direction: column; gap: 6px; padding: 16px 18px; background: ${T.surface}; border: 1px solid ${T.border}; border-radius: 10px; min-width: 0">
  <div style="display: flex; align-items: center; justify-content: space-between"><span style="font-size: 12px; color: ${T.muted}; font-weight: 500">${label}</span>${sparkData ? spark(sparkData, 80, 22, color === T.text ? T.accent : color) : ''}</div>
  <div style="display: flex; align-items: baseline; gap: 10px"><span style="font-family: ${MONO}; font-size: 28px; font-weight: 700; letter-spacing: -0.03em; color: ${color}">${value}</span></div>
  <div style="display: flex; align-items: center; gap: 10px; font-size: 12px; color: ${T.dim}">${sub}</div>
</div>`;

// host heatmap: 128 hosts
const hosts = []; for (let i = 0; i < 128; i++) hosts.push('ok'); [7, 19, 33, 58, 91].forEach(i => hosts[i] = 'warn'); [44, 102].forEach(i => hosts[i] = 'crit'); [77, 118].forEach(i => hosts[i] = 'unknown');
const heat = `<div style="display: flex; flex-wrap: wrap; gap: 4px; padding: 16px">${hosts.map(s => `<div style="width: 14px; height: 14px; border-radius: 3px; background: ${T[s]}; opacity: ${s === 'ok' ? 0.55 : 1}"></div>`).join('')}</div>`;

const alerts = [
  ['crit', 'db-02.fra', 'Disk usage /var/lib/postgresql', '92 % ≥ 90 %', '14 min', 'fra'],
  ['crit', 'mail-01.hel', 'Service postfix', 'inactive (exited)', '41 min', 'hel'],
  ['warn', 'web-03.fra', 'CPU load 5m', '6.8 ≥ 6.0 (8 cores)', '1 h 05', 'fra'],
  ['warn', 'cache-01.fra', 'Memory usage', '84 % ≥ 80 %', '2 h 12', 'fra'],
  ['warn', 'k3s-02.hel', 'TLS certificate api.acme.io', 'expires in 12 d', '3 h', 'hel'],
  ['warn', 'pi-garage', 'Agent heartbeat', 'last seen 9 min ago', '9 min', 'home-lab'],
  ['warn', 'office-fs', 'SMART /dev/sda', 'reallocated sectors 12', '1 d', 'office-berlin'],
];
const alertRows = alerts.map(([s, h, c, v, t, z]) => `<div style="display: grid; grid-template-columns: 20px 150px minmax(0, 1fr) 170px 70px 92px; align-items: center; gap: 12px; padding: 10px 16px; border-bottom: 1px solid ${T.border}">
  ${dot(T[s])}<span style="font-family: ${MONO}; font-size: 12px; font-weight: 600">${h}</span><span style="color: ${T.text}; overflow: hidden; text-overflow: ellipsis; white-space: nowrap">${c}</span><span style="font-family: ${MONO}; font-size: 12px; color: ${T[s]}">${v}</span><span style="font-size: 12px; color: ${T.muted}; display: flex; align-items: center; gap: 4px">${ic.clock}${t}</span><div style="display: flex; justify-content: flex-end">${btn('Ack')}</div>
</div>`).join('');

const zones = [['fra', 'Hetzner Falkenstein', 2, 64, 64, '3 ms'], ['hel', 'Hetzner Helsinki', 1, 38, 37, '22 ms'], ['office-berlin', 'Office LAN', 1, 18, 18, '7 ms'], ['home-lab', 'Home lab (NAT)', 1, 8, 7, '31 ms']];
const zoneRows = zones.map(([id, name, w, a, c, l]) => `<div style="display: flex; align-items: center; gap: 12px; padding: 10px 16px; border-bottom: 1px solid ${T.border}">
  ${dot(a === c ? T.ok : T.warn)}<div style="display: flex; flex-direction: column; flex-grow: 1; min-width: 0"><span style="font-family: ${MONO}; font-size: 12px; font-weight: 600">${id}</span><span style="font-size: 11px; color: ${T.muted}">${name}</span></div>
  <div style="display: flex; flex-direction: column; align-items: flex-end"><span style="font-family: ${MONO}; font-size: 12px">${c}/${a} agents</span><span style="font-size: 11px; color: ${T.muted}">${w} worker · ${l}</span></div>
</div>`).join('');

const jobs = [['ok', 'deploy-web.yml', 'group:webserver', '3 min ago', '48 s', 'TR'], ['crit', 'patch-all.yml', 'zone:hel', '52 min ago', '4 m 12 s', 'schedule'], ['ok', 'rotate-logs.yml', 'all', '2 h ago', '1 m 03 s', 'schedule'], ['ok', 'add-monitor-user.yml', 'db-02.fra', '5 h ago', '19 s', 'DG'], ['run', 'k3s-upgrade.yml', 'group:k3s', 'running', '2 m 40 s', 'TR']];
const jobRows = jobs.map(([s, p, lim, when, dur, by]) => `<div style="display: grid; grid-template-columns: 20px minmax(0, 1fr) 84px 60px; align-items: center; gap: 10px; padding: 10px 16px; border-bottom: 1px solid ${T.border}">
  ${s === 'run' ? `<span style="display: inline-block; width: 8px; height: 8px; border-radius: 50%; border: 2px solid ${T.accent}; border-top-color: transparent"></span>` : dot(T[s])}<div style="display: flex; flex-direction: column; min-width: 0"><span style="font-family: ${MONO}; font-size: 12px; font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap">${p}</span><span style="font-size: 11px; color: ${T.muted}">limit ${lim} · ${by}</span></div>
  <span style="font-size: 11px; color: ${s === 'run' ? T.accent : T.dim}; white-space: nowrap">${when}</span><span style="font-family: ${MONO}; font-size: 11px; color: ${T.dim}; text-align: right; white-space: nowrap">${dur}</span>
</div>`).join('');

const dashboard = shell({
  active: 'Dashboard',
  crumbs: `<span style="color: ${T.text}; font-weight: 600">Dashboard</span>`,
  content: `
      <div style="display: flex; align-items: center; justify-content: space-between">
        <div style="display: flex; flex-direction: column; gap: 2px"><h1 style="margin: 0; font-size: 20px; font-weight: 600; letter-spacing: -0.01em">Good morning, Tim</h1><span style="font-size: 13px; color: ${T.muted}">128 hosts across 4 zones · 2 need attention</span></div>
        <div style="display: flex; gap: 8px">${btn('Add host', false, ic.server)}${btn('Run playbook', true, ic.play)}</div>
      </div>
      <div style="display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 16px">
        ${stat('Hosts', '128', `${pill('121 ok', T.ok)}${pill('5 warn', T.warn)}${pill('2 crit', T.crit)}`)}
        ${stat('Open alerts', '7', `<span>2 critical · 5 warning</span><span style="color: ${T.muted}">· 3 acknowledged</span>`, T.crit)}
        ${stat('Jobs · 24 h', '14', `<span>12 succeeded · 1 failed · 1 running</span>`, T.text)}
        ${stat('Fleet CPU', '34 %', `<span>mem 58 % · peak 64 % at 06:10</span>`, T.text, fleetCpu.slice(-40))}
      </div>
      <div style="display: grid; grid-template-columns: minmax(0, 1fr) 380px; gap: 16px; flex-grow: 1; min-height: 0">
        <div style="display: flex; flex-direction: column; gap: 16px; min-height: 0">
          ${card('Open alerts', `<div style="display: flex; flex-direction: column">${alertRows}</div>`, `<span>sorted by severity</span>${ic.chevronR}`)}
          ${card('Fleet load · 24 h', `<div style="padding: 16px 20px 8px">${chart({ w: 672, h: 120, lines: [{ data: fleetMem, color: T.accentDim }, { data: fleetCpu, color: T.accent, fill: true }] })}</div>`, `<span style="display: flex; align-items: center; gap: 6px">${dot(T.accent, 6)} cpu</span><span style="display: flex; align-items: center; gap: 6px">${dot(T.accentDim, 6)} memory</span>`)}
        </div>
        <div style="display: flex; flex-direction: column; gap: 16px; min-height: 0">
          ${card('Host status', heat, `<span>128 hosts</span>`)}
          ${card('Zones &amp; workers', `<div style="display: flex; flex-direction: column">${zoneRows}</div>`, `${pill('all connected', T.ok)}`)}
          ${card('Recent jobs', `<div style="display: flex; flex-direction: column">${jobRows}</div>`, `${ic.chevronR}`)}
        </div>
      </div>`,
  height: 960,
});
fs.writeFileSync('Main.dc.html', dashboard);

// ---------- HOST DETAIL ----------
const cpu = series(96, 28, 8); spike(cpu, 70, 45, 8);
const mem = series(96, 61, 2, 0.08);
const diskS = series(96, 86, 0.6, 0.06);
const netIn = series(96, 18, 10); const netOut = series(96, 9, 6);

const tab = (l, on = false) => `<div style="padding: 10px 2px; font-size: 13px; font-weight: ${on ? 600 : 500}; color: ${on ? T.text : T.muted}; border-bottom: 2px solid ${on ? T.accent : 'transparent'}">${l}</div>`;
const chartCard = (title, val, lines, extra, max = 100, unit = '%') => `<div style="display: flex; flex-direction: column; background: ${T.surface}; border: 1px solid ${T.border}; border-radius: 10px; padding: 14px 16px 10px; gap: 8px">
  <div style="display: flex; align-items: baseline; justify-content: space-between"><span style="font-size: 12px; color: ${T.muted}; font-weight: 500">${title}</span><span style="font-family: ${MONO}; font-size: 16px; font-weight: 700">${val}</span></div>
  ${chart({ w: 290, h: 90, lines, max, unit, grid: 2, labels: ['-24h', '-12h', 'now'] })}
  <div style="display: flex; gap: 12px; font-size: 11px; color: ${T.muted}">${extra}</div>
</div>`;

const checks = [
  ['crit', 'Disk usage', '/var/lib/postgresql', '92 %', '≥ 90 % crit · ≥ 80 % warn', 'agent · 60 s'],
  ['ok', 'PostgreSQL', 'localhost:5432 · secret pg_main', '41 conn · 2 slow', 'connections ≤ 180', 'agent · 30 s'],
  ['ok', 'Service', 'postgresql.service', 'active (running)', 'must be active', 'agent · 30 s'],
  ['ok', 'Service', 'pgbouncer.service', 'active (running)', 'must be active', 'agent · 30 s'],
  ['ok', 'CPU load', '5 min average', '1.9 / 8', '≥ 6.0 warn', 'agent · 30 s'],
  ['ok', 'Memory', 'incl. buffers', '61 %', '≥ 80 % warn', 'agent · 30 s'],
  ['ok', 'TCP', '5432 from worker fra-1', '2 ms', 'timeout 3 s', 'remote · 60 s'],
  ['ok', 'ICMP', 'from worker fra-1', '0.4 ms · 0 % loss', 'loss ≥ 20 % crit', 'remote · 30 s'],
  ['ok', 'SMART', '/dev/nvme0n1', 'PASSED · 38 °C', 'reallocated ≥ 1 warn', 'agent · 1 h'],
];
const checkRows = checks.map(([s, t, target, v, th, src]) => `<div style="display: grid; grid-template-columns: 20px 110px minmax(0, 1fr) 130px 170px 100px; align-items: center; gap: 12px; padding: 10px 16px; border-bottom: 1px solid ${T.border}">
  ${dot(T[s])}<span style="font-weight: 600">${t}</span><span style="font-family: ${MONO}; font-size: 12px; color: ${T.dim}; overflow: hidden; text-overflow: ellipsis; white-space: nowrap">${target}</span><span style="font-family: ${MONO}; font-size: 12px; color: ${s === 'ok' ? T.text : T[s]}">${v}</span><span style="font-size: 11px; color: ${T.muted}">${th}</span><span style="font-size: 11px; color: ${T.muted}; text-align: right">${src}</span>
</div>`).join('');

const facts = [['OS', 'Debian 13 (trixie)'], ['Kernel', '6.12.0-1-amd64'], ['CPU', '8 × AMD EPYC 7763'], ['Memory', '32 GiB'], ['Uptime', '47 d 3 h'], ['IPv4', '116.203.14.72'], ['IPv6', '2a01:4f8:1c1c:9f02::1'], ['Agent', 'v0.4.2 · mTLS ok'], ['Worker', 'fra-1 (Falkenstein)'], ['SSH', 'ansible@ :22 · key acme-deploy'], ['Jump host', 'bastion.fra (zone default)']];
const factRows = facts.map(([k, v]) => `<div style="display: flex; justify-content: space-between; gap: 12px; padding: 7px 16px; border-bottom: 1px solid ${T.border}; font-size: 12px"><span style="color: ${T.muted}">${k}</span><span style="font-family: ${MONO}; color: ${T.text}; text-align: right">${v}</span></div>`).join('');

const groups = [['postgres', 'host vars', ['pg_version = 17', 'replication_role = primary', 'data_dir = /var/lib/postgresql']], ['fra', 'zone group', ['ntp_server = ntp1.hetzner.de']], ['production', 'no host vars', []], ['debian', 'auto (facts)', []]];
const groupRows = groups.map(([g, k, vars]) => `<div style="display: flex; flex-direction: column; gap: 6px; padding: 10px 16px; border-bottom: 1px solid ${T.border}">
  <div style="display: flex; align-items: center; gap: 8px">${ic.layers}<span style="font-family: ${MONO}; font-size: 12px; font-weight: 600; flex-grow: 1">${g}</span><span style="font-size: 11px; color: ${T.muted}">${k}</span></div>
  ${vars.length ? `<div style="display: flex; flex-wrap: wrap; gap: 6px; padding-left: 24px">${vars.map(tag).join('')}</div>` : ''}
</div>`).join('');

const hostDetail = shell({
  active: 'Hosts',
  crumbs: `<span>Hosts</span>${ic.chevronR}<span>fra</span>${ic.chevronR}<span style="color: ${T.text}; font-weight: 600; font-family: ${MONO}">db-02.fra</span>`,
  content: `
      <div style="display: flex; align-items: flex-start; justify-content: space-between; gap: 16px">
        <div style="display: flex; flex-direction: column; gap: 10px">
          <div style="display: flex; align-items: center; gap: 12px"><h1 style="margin: 0; font-family: ${MONO}; font-size: 22px; font-weight: 700; letter-spacing: -0.02em">db-02.fra</h1>${pill('critical', T.crit)}<span style="font-size: 12px; color: ${T.muted}">1 open alert · acknowledged by DG</span></div>
          <div style="display: flex; gap: 6px; flex-wrap: wrap">${tag('zone: fra')}${tag('postgres')}${tag('production')}${tag('debian')}${tag('worker: fra-1')}</div>
        </div>
        <div style="display: flex; gap: 8px">${btn('Edit vars', false, ic.key)}${btn('Terminal', false, ic.terminal)}${btn('Run playbook', true, ic.play)}</div>
      </div>
      <div style="display: flex; gap: 24px; border-bottom: 1px solid ${T.border}">${tab('Overview', true)}${tab('Checks')}${tab('Metrics')}${tab('Variables')}${tab('Groups')}${tab('Jobs')}${tab('Alerts')}${tab('Agent')}</div>
      <div style="display: grid; grid-template-columns: minmax(0, 1fr) 340px; gap: 16px; flex-grow: 1; min-height: 0">
        <div style="display: flex; flex-direction: column; gap: 16px; min-height: 0">
          <div style="display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px">
            ${chartCard('CPU', '24 %', [{ data: cpu, color: T.accent, fill: true }], `<span>load 1.9 · 8 cores</span><span>spike 06:10 → pg_dump</span>`)}
            ${chartCard('Memory', '61 %', [{ data: mem, color: T.accentDim, fill: true }], `<span>19.5 / 32 GiB</span><span>swap 0 %</span>`)}
            ${chartCard('Disk /var/lib/postgresql', '92 %', [{ data: diskS, color: T.crit, fill: true }], `<span style="color: ${T.crit}">368 / 400 GiB · ~9 d until full</span>`)}
            ${chartCard('Network', '18 Mbit/s', [{ data: netIn, color: T.accent }, { data: netOut, color: T.accentDim }], `<span style="display: flex; align-items: center; gap: 6px">${dot(T.accent, 6)} in</span><span style="display: flex; align-items: center; gap: 6px">${dot(T.accentDim, 6)} out</span>`, 100, 'M')}
          </div>
          ${card('Checks', `<div style="display: flex; flex-direction: column">${checkRows}</div>`, `<span>9 checks · 1 failing</span>${btn('Add check')}`)}
        </div>
        <div style="display: flex; flex-direction: column; gap: 16px; min-height: 0">
          ${card('Facts', `<div style="display: flex; flex-direction: column">${factRows}</div>`, `<span>refreshed 4 min ago</span>`)}
          ${card('Groups &amp; variables', `<div style="display: flex; flex-direction: column">${groupRows}</div>`, `${btn('Assign')}`)}
          ${card('Agent secrets', `<div style="display: flex; flex-direction: column; gap: 6px; padding: 12px 16px; font-size: 12px">
            <div style="display: flex; align-items: center; gap: 8px">${dot(T.ok, 6)}<span style="font-family: ${MONO}">pg_main</span><span style="color: ${T.muted}; margin-left: auto">used by 1 check</span></div>
            <div style="display: flex; align-items: center; gap: 8px">${dot(T.ok, 6)}<span style="font-family: ${MONO}">pgbouncer_stats</span><span style="color: ${T.muted}; margin-left: auto">unused</span></div>
            <div style="color: ${T.muted}; margin-top: 4px">Values live only in the host's agent.yaml. Pulsix sees names, never secrets.</div>
          </div>`, `<span>local</span>`)}
        </div>
      </div>`,
  height: 1080,
});
fs.writeFileSync('HostDetail.dc.html', hostDetail);

// ================= HOST LIST =================
const chip = (l, on = false, icon = '') => `<div style="display: inline-flex; align-items: center; gap: 6px; height: 30px; padding: 0 12px; border-radius: 6px; font-size: 12px; font-weight: 500; ${on ? `background: ${T.accent}1A; color: ${T.accent}; border: 1px solid ${T.accent}55` : `background: ${T.surface}; color: ${T.dim}; border: 1px solid ${T.border}`}">${icon}${l}</div>`;
const cap = (kind, on, detail) => on
  ? `<div style="display: flex; align-items: center; gap: 8px"><span style="display: inline-flex; align-items: center; justify-content: center; width: 22px; height: 22px; border-radius: 6px; background: ${T.accent}1A; color: ${T.accent}">${kind === 'm' ? ic.terminal : ic.activity}</span><div style="display: flex; flex-direction: column"><span style="font-size: 12px; font-weight: 600">${detail[0]}</span><span style="font-size: 11px; color: ${T.muted}; font-family: ${MONO}">${detail[1]}</span></div></div>`
  : `<div style="display: flex; align-items: center; gap: 8px; color: ${T.muted}"><span style="display: inline-flex; align-items: center; justify-content: center; width: 22px; height: 22px; border-radius: 6px; border: 1px dashed ${T.border}">${kind === 'm' ? ic.terminal : ic.activity}</span><span style="font-size: 12px">off</span><span style="font-size: 11px; color: ${T.accentDim}; margin-left: 4px">enable</span></div>`;
const hostRows = [
  ['crit', 'db-02.fra', 'fra', ['role=db', 'env=prod', 'os=debian'], [true, ['Ansible', 'ansible@ · acme-deploy']], [true, ['Agent v0.4.2', '9 checks · 1 failing']], '12 s'],
  ['ok', 'web-01.fra', 'fra', ['role=web', 'env=prod', 'os=debian'], [true, ['Ansible', 'ansible@ · acme-deploy']], [true, ['Agent v0.4.2', '11 checks']], '8 s'],
  ['ok', 'web-02.fra', 'fra', ['role=web', 'env=prod', 'os=debian'], [true, ['Ansible', 'ansible@ · acme-deploy']], [true, ['Agent v0.4.2', '11 checks']], '9 s'],
  ['warn', 'web-03.fra', 'fra', ['role=web', 'env=prod', 'os=debian'], [true, ['Ansible', 'ansible@ · acme-deploy']], [true, ['Agent v0.4.1', '11 checks · 1 warning']], '11 s'],
  ['ok', 'cache-01.fra', 'fra', ['role=cache', 'env=prod', 'os=debian'], [true, ['Ansible', 'ansible@ · acme-deploy']], [true, ['Agent v0.4.2', '6 checks']], '5 s'],
  ['ok', 'k3s-01.hel', 'hel', ['role=k3s', 'env=prod', 'os=ubuntu'], [true, ['Ansible', 'root@ · hel-bootstrap']], [false], '—'],
  ['ok', 'k3s-02.hel', 'hel', ['role=k3s', 'env=prod', 'os=ubuntu'], [true, ['Ansible', 'root@ · hel-bootstrap']], [false], '—'],
  ['ok', 'switch-core', 'office-berlin', ['role=network', 'vendor=mikrotik'], [false], [true, ['Remote only', 'ICMP · SNMP · 3 checks']], '30 s'],
  ['ok', 'api.acme.io', 'fra', ['kind=endpoint', 'env=prod'], [false], [true, ['Remote only', 'HTTPS · TLS expiry · 2 checks']], '60 s'],
  ['ok', 'office-fs', 'office-berlin', ['role=storage', 'os=windows'], [false], [true, ['Agent v0.4.2 (win)', '7 checks · SMART warning']], '14 s'],
  ['unknown', 'pi-garage', 'home-lab', ['role=iot', 'os=raspbian', 'arch=arm64'], [true, ['Ansible', 'pi@ · homelab-key']], [true, ['Agent v0.3.9', 'last seen 9 min ago']], '9 min'],
  ['ok', 'mac-studio-dg', 'office-berlin', ['role=workstation', 'os=macos'], [false], [true, ['Agent v0.4.2 (mac)', '5 checks']], '20 s'],
];
const hostTable = `<div style="display: flex; flex-direction: column; background: ${T.surface}; border: 1px solid ${T.border}; border-radius: 10px; overflow: hidden">
  <div style="display: grid; grid-template-columns: 20px 170px 110px minmax(0, 1fr) 210px 230px 70px 32px; gap: 12px; padding: 10px 16px; border-bottom: 1px solid ${T.border}; font-size: 11px; color: ${T.muted}; text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600"><span></span><span>Host</span><span>Zone</span><span>Tags</span><span>Managed · Ansible</span><span>Monitored</span><span>Seen</span><span></span></div>
  ${hostRows.map(([s, h, z, tags, m, mon, seen]) => `<div style="display: grid; grid-template-columns: 20px 170px 110px minmax(0, 1fr) 210px 230px 70px 32px; align-items: center; gap: 12px; padding: 10px 16px; border-bottom: 1px solid ${T.border}">
    ${dot(T[s])}<span style="font-family: ${MONO}; font-size: 12px; font-weight: 600">${h}</span><span style="font-family: ${MONO}; font-size: 12px; color: ${T.dim}">${z}</span><div style="display: flex; gap: 4px; flex-wrap: wrap">${tags.map(tag).join('')}</div>${cap('m', m[0], m[1])}${cap('o', mon[0], mon[1])}<span style="font-family: ${MONO}; font-size: 11px; color: ${T.muted}">${seen}</span><span style="color: ${T.muted}">${ic.more}</span>
  </div>`).join('')}
</div>`;
fs.writeFileSync('HostList.dc.html', shell({
  active: 'Hosts',
  crumbs: `<span style="color: ${T.text}; font-weight: 600">Hosts</span>`,
  content: `
      <div style="display: flex; align-items: center; justify-content: space-between">
        <div style="display: flex; flex-direction: column; gap: 2px"><h1 style="margin: 0; font-size: 20px; font-weight: 600">Hosts</h1><span style="font-size: 13px; color: ${T.muted}">128 hosts · 96 managed · 117 monitored · 85 both</span></div>
        <div style="display: flex; gap: 8px">${btn('Import from inventory', false, ic.git)}${btn('Add host', true, ic.server)}</div>
      </div>
      <div style="display: flex; align-items: center; gap: 8px">
        <div style="display: flex; align-items: center; gap: 8px; width: 260px; height: 30px; padding: 0 10px; border-radius: 6px; background: ${T.surface}; border: 1px solid ${T.border}; color: ${T.muted}; font-size: 12px">${ic.search}<span>Filter hosts…</span></div>
        <div style="width: 1px; height: 20px; background: ${T.border}; margin: 0 4px"></div>
        ${chip('Managed', false, ic.terminal)}${chip('Monitored', true, ic.activity)}${chip('Agent', false)}${chip('Remote only', false)}
        <div style="width: 1px; height: 20px; background: ${T.border}; margin: 0 4px"></div>
        ${chip('zone: all', false, ic.globe)}${chip('env=prod', true)}${chip('+ tag', false)}
        <span style="margin-left: auto; font-size: 12px; color: ${T.muted}">showing 12 of 117</span>
      </div>
      ${hostTable}`,
  height: 960,
}));

// ================= SCHEMA BUILDER =================
const typeBadge = (t) => `<span style="font-family: ${MONO}; font-size: 10px; font-weight: 600; padding: 2px 6px; border-radius: 4px; color: ${T.accent}; background: ${T.accent}14; border: 1px solid ${T.accent}33">${t}</span>`;
const scopeBadge = (s) => `<span style="font-size: 10px; font-weight: 600; padding: 2px 6px; border-radius: 4px; color: ${s === 'host' ? T.warn : T.dim}; background: ${s === 'host' ? T.warn + '14' : T.ink}; border: 1px solid ${s === 'host' ? T.warn + '33' : T.border}">${s}</span>`;
const fieldCard = (name, type, scope, desc, selected = false, children = '') => `<div style="display: flex; flex-direction: column; gap: 8px; padding: 12px 14px; border-radius: 8px; background: ${selected ? T.ink : 'transparent'}; border: 1px solid ${selected ? T.accent : T.border}">
  <div style="display: flex; align-items: center; gap: 8px"><span style="color: ${T.muted}; cursor: grab">${ic.more}</span><span style="font-family: ${MONO}; font-size: 12px; font-weight: 600; flex-grow: 1">${name}</span>${typeBadge(type)}${scopeBadge(scope)}</div>
  <div style="font-size: 11px; color: ${T.muted}; padding-left: 24px">${desc}</div>
  ${children ? `<div style="display: flex; flex-direction: column; gap: 6px; margin-left: 24px; padding-left: 12px; border-left: 2px solid ${T.border}">${children}</div>` : ''}
</div>`;
const subField = (n, t, req = true) => `<div style="display: flex; align-items: center; gap: 8px"><span style="font-family: ${MONO}; font-size: 11px; flex-grow: 1">${n}</span>${typeBadge(t)}<span style="font-size: 10px; color: ${T.muted}">${req ? 'required' : 'optional'}</span></div>`;
const input = (label, value, w = '100%', mono = true) => `<div style="display: flex; flex-direction: column; gap: 5px; width: ${w}"><span style="font-size: 11px; color: ${T.muted}; font-weight: 500">${label}</span><div style="display: flex; align-items: center; height: 32px; padding: 0 10px; border-radius: 6px; background: ${T.ink}; border: 1px solid ${T.border}; font-size: 12px; ${mono ? `font-family: ${MONO}` : ''}">${value}</div></div>`;
const select = (label, value, w = '100%') => `<div style="display: flex; flex-direction: column; gap: 5px; width: ${w}"><span style="font-size: 11px; color: ${T.muted}; font-weight: 500">${label}</span><div style="display: flex; align-items: center; justify-content: space-between; height: 32px; padding: 0 10px; border-radius: 6px; background: ${T.ink}; border: 1px solid ${T.border}; font-size: 12px">${value}${ic.chevron}</div></div>`;
const toggle = (label, on, hint = '') => `<div style="display: flex; align-items: center; gap: 10px"><div style="width: 32px; height: 18px; border-radius: 999px; background: ${on ? T.accent : T.border}; position: relative; flex-shrink: 0"><div style="position: absolute; top: 2px; ${on ? 'right: 2px' : 'left: 2px'}; width: 14px; height: 14px; border-radius: 50%; background: ${on ? T.ink : T.dim}"></div></div><div style="display: flex; flex-direction: column"><span style="font-size: 12px; font-weight: 500">${label}</span>${hint ? `<span style="font-size: 11px; color: ${T.muted}">${hint}</span>` : ''}</div></div>`;
const seg = (opts, on) => `<div style="display: flex; border-radius: 6px; border: 1px solid ${T.border}; overflow: hidden">${opts.map(o => `<div style="padding: 6px 12px; font-size: 12px; font-weight: 500; ${o === on ? `background: ${T.accent}1A; color: ${T.accent}` : `color: ${T.dim}; background: ${T.ink}`}">${o}</div>`).join('')}</div>`;

const formRow = (label, value, hint = '') => `<div style="display: flex; flex-direction: column; gap: 5px"><div style="display: flex; align-items: baseline; gap: 8px"><span style="font-size: 12px; font-weight: 500">${label}</span>${hint ? `<span style="font-size: 11px; color: ${T.muted}">${hint}</span>` : ''}</div>${value}</div>`;
const box = (v, w = '100%', mono = true) => `<div style="display: flex; align-items: center; height: 32px; padding: 0 10px; border-radius: 6px; background: ${T.ink}; border: 1px solid ${T.border}; font-size: 12px; width: ${w}; box-sizing: border-box; ${mono ? `font-family: ${MONO}` : ''}">${v}</div>`;
const vhostRow = (domain, php, ssl, aliases) => `<div style="display: flex; flex-direction: column; gap: 10px; padding: 12px; border-radius: 8px; border: 1px solid ${T.border}; background: ${T.surface}">
  <div style="display: grid; grid-template-columns: minmax(0, 1fr) 110px 90px; gap: 10px">${formRow('domain', box(domain))}${formRow('php_version', box(`${php} ${ic.chevron}`))}${formRow('ssl', toggle('', ssl))}</div>
  ${formRow('aliases', `<div style="display: flex; gap: 6px; flex-wrap: wrap; min-height: 32px; padding: 5px 8px; border-radius: 6px; background: ${T.ink}; border: 1px solid ${T.border}; align-items: center">${aliases.map(a => tag(a)).join('')}<span style="font-size: 11px; color: ${T.muted}">+ add</span></div>`, 'list of string · optional')}
</div>`;

fs.writeFileSync('SchemaBuilder.dc.html', shell({
  active: 'Groups',
  crumbs: `<span>Groups</span>${ic.chevronR}<span>production</span>${ic.chevronR}<span style="color: ${T.text}; font-weight: 600; font-family: ${MONO}">webserver</span>`,
  content: `
      <div style="display: flex; align-items: flex-start; justify-content: space-between">
        <div style="display: flex; flex-direction: column; gap: 8px">
          <div style="display: flex; align-items: center; gap: 12px"><h1 style="margin: 0; font-family: ${MONO}; font-size: 22px; font-weight: 700">webserver</h1>${tag('inventory group')}<span style="font-size: 12px; color: ${T.muted}">parent: production · 14 members · 2 children</span></div>
          <div style="display: flex; gap: 24px; border-bottom: 1px solid ${T.border}">${tab('Variable schema', true)}${tab('Group values')}${tab('Members')}${tab('Children')}${tab('Preview inventory')}</div>
        </div>
        <div style="display: flex; gap: 8px">${btn('Discard')}${btn('Save schema', true, ic.check)}</div>
      </div>
      <div style="display: grid; grid-template-columns: 400px 340px minmax(0, 1fr); gap: 16px; flex-grow: 1; min-height: 0">
        ${card('Fields', `<div style="display: flex; flex-direction: column; gap: 8px; padding: 12px">
          ${fieldCard('php_version', 'enum', 'group', 'Default PHP for every host in this group · 8.2 | 8.3 | 8.4')}
          ${fieldCard('max_upload_mb', 'int', 'group', 'min 1 · max 2048 · default 64')}
          ${fieldCard('enable_http3', 'bool', 'group', 'default false')}
          ${fieldCard('vhosts', 'repeater', 'host', 'Virtual hosts served by this host · min 1', true,
            subField('domain', 'string') + subField('php_version', 'enum', false) + subField('ssl', 'bool') + subField('aliases', 'list&lt;string&gt;', false) + subField('basic_auth', 'sub-schema', false))}
          ${fieldCard('deploy_key', 'secret', 'host', 'Stored encrypted · injected as extra var at run time')}
          <div style="display: flex; align-items: center; justify-content: center; gap: 8px; height: 40px; border-radius: 8px; border: 1px dashed ${T.border}; color: ${T.accentDim}; font-size: 12px; font-weight: 500">+ Add field</div>
        </div>`, `<span>5 fields · 2 host-scoped</span>`)}
        ${card('Field · vhosts', `<div style="display: flex; flex-direction: column; gap: 14px; padding: 16px">
          <div style="display: flex; gap: 10px">${input('Name', 'vhosts')}${select('Type', 'repeater', '130px')}</div>
          ${input('Label', 'Virtual hosts', '100%', false)}
          <div style="display: flex; flex-direction: column; gap: 6px"><span style="font-size: 11px; color: ${T.muted}; font-weight: 500">Scope</span>${seg(['group', 'host'], 'host')}<span style="font-size: 11px; color: ${T.muted}">Host-scoped fields are asked when a host is assigned to this group and land in host_vars.</span></div>
          <div style="display: flex; gap: 10px">${input('Min items', '1', '50%')}${input('Max items', '∞', '50%')}</div>
          ${toggle('Required', true)}
          ${toggle('Host may override group value', false, 'only for group-scoped fields')}
          <div style="display: flex; flex-direction: column; gap: 6px"><span style="font-size: 11px; color: ${T.muted}; font-weight: 500">Sub-schema</span>
            <div style="display: flex; flex-direction: column; gap: 4px; padding: 8px 10px; border-radius: 6px; background: ${T.ink}; border: 1px solid ${T.border}">${subField('domain', 'string')}${subField('php_version', 'enum', false)}${subField('ssl', 'bool')}${subField('aliases', 'list&lt;string&gt;', false)}${subField('basic_auth', 'sub-schema', false)}<span style="font-size: 11px; color: ${T.accentDim}; margin-top: 4px">+ add sub-field</span></div>
          </div>
          ${input('Description', 'Virtual hosts served by this host', '100%', false)}
        </div>`, `<span style="color: ${T.crit}">delete</span>`)}
        ${card('Assignment preview · web-03.fra', `<div style="display: flex; flex-direction: column; gap: 14px; padding: 16px">
          <div style="font-size: 12px; color: ${T.muted}">This is the form an operator fills when assigning a host to <span style="font-family: ${MONO}; color: ${T.text}">webserver</span>. Group-scoped values are shown read-only.</div>
          <div style="display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 10px; padding: 12px; border-radius: 8px; background: ${T.ink}; border: 1px solid ${T.border}">
            ${formRow('php_version', `<span style="font-family: ${MONO}; font-size: 12px; color: ${T.dim}">8.3</span>`, 'group')}${formRow('max_upload_mb', `<span style="font-family: ${MONO}; font-size: 12px; color: ${T.dim}">64</span>`, 'group')}${formRow('enable_http3', `<span style="font-family: ${MONO}; font-size: 12px; color: ${T.dim}">false</span>`, 'group')}
          </div>
          ${formRow('Virtual hosts', `<div style="display: flex; flex-direction: column; gap: 8px">${vhostRow('acme.io', '8.3', true, ['www.acme.io', 'acme.de'])}${vhostRow('shop.acme.io', '8.4', true, [])}<div style="display: flex; align-items: center; justify-content: center; height: 36px; border-radius: 8px; border: 1px dashed ${T.border}; color: ${T.accentDim}; font-size: 12px; font-weight: 500">+ Add virtual host</div></div>`, 'repeater · host · min 1')}
          ${formRow('deploy_key', box('••••••••••••••••••••••••  <span style="color: #6B7280; margin-left: auto">secret</span>'), 'host')}
          <div style="display: flex; justify-content: flex-end; gap: 8px; margin-top: 4px">${btn('Cancel')}${btn('Assign to webserver', true)}</div>
        </div>`, `<span>generated from schema</span>`)}
      </div>`,
  height: 1080,
}));

// ================= MONITORING GROUP =================
const rule = (k, op, v) => `<div style="display: inline-flex; align-items: center; height: 30px; border-radius: 6px; border: 1px solid ${T.border}; background: ${T.ink}; overflow: hidden; font-family: ${MONO}; font-size: 12px"><span style="padding: 0 10px; color: ${T.dim}">${k}</span><span style="padding: 0 8px; color: ${T.accent}; border-left: 1px solid ${T.border}; border-right: 1px solid ${T.border}">${op}</span><span style="padding: 0 10px">${v}</span></div>`;
const members = [['crit', 'db-02.fra', 'selector'], ['ok', 'db-01.fra', 'selector'], ['ok', 'db-03.fra', 'selector'], ['ok', 'db-01.hel', 'selector'], ['ok', 'db-02.hel', 'selector'], ['ok', 'pg-analytics.fra', 'manual'], ['ok', 'pgbouncer-01.fra', 'manual']];
const alertRules = [
  ['Disk usage', 'any mount ≥ 80 % for 5 m', 'warn', '≥ 90 % for 2 m', 'crit'],
  ['PostgreSQL connections', '≥ 80 % of max for 5 m', 'warn', '≥ 95 % for 1 m', 'crit'],
  ['Replication lag', '≥ 30 s for 2 m', 'warn', '≥ 300 s', 'crit'],
  ['Service postgresql', '—', '', 'inactive for 30 s', 'crit'],
  ['Agent heartbeat', 'missing for 3 m', 'warn', 'missing for 10 m', 'crit'],
];
const routeRow = (when, sev, chans) => `<div style="display: grid; grid-template-columns: 200px 90px minmax(0, 1fr); align-items: center; gap: 12px; padding: 10px 16px; border-bottom: 1px solid ${T.border}"><div style="display: flex; align-items: center; gap: 8px; font-size: 12px">${ic.clock}${when}</div><div>${sev}</div><div style="display: flex; gap: 6px; flex-wrap: wrap">${chans.map(c => tag(c)).join('')}</div></div>`;
fs.writeFileSync('MonitoringGroup.dc.html', shell({
  active: 'Monitoring',
  crumbs: `<span>Monitoring</span>${ic.chevronR}<span>Groups</span>${ic.chevronR}<span style="color: ${T.text}; font-weight: 600; font-family: ${MONO}">prod-databases</span>`,
  content: `
      <div style="display: flex; align-items: flex-start; justify-content: space-between">
        <div style="display: flex; flex-direction: column; gap: 8px">
          <div style="display: flex; align-items: center; gap: 12px"><h1 style="margin: 0; font-family: ${MONO}; font-size: 22px; font-weight: 700">prod-databases</h1>${tag('monitoring group')}${pill('1 critical', T.crit)}<span style="font-size: 12px; color: ${T.muted}">7 members · 5 by selector · 2 manual</span></div>
          <div style="display: flex; gap: 24px; border-bottom: 1px solid ${T.border}">${tab('Overview', true)}${tab('Members')}${tab('Checks')}${tab('Alert rules')}${tab('Notifications')}${tab('Maintenance')}${tab('History')}</div>
        </div>
        <div style="display: flex; gap: 8px">${btn('Silence 1 h')}${btn('Save', true, ic.check)}</div>
      </div>
      <div style="display: grid; grid-template-columns: minmax(0, 1fr) 360px; gap: 16px; flex-grow: 1; min-height: 0">
        <div style="display: flex; flex-direction: column; gap: 16px">
          ${card('Membership', `<div style="display: flex; flex-direction: column; gap: 12px; padding: 16px">
            <div style="display: flex; align-items: center; gap: 8px; flex-wrap: wrap">${rule('role', '=', 'db')}<span style="font-size: 11px; font-weight: 600; color: ${T.muted}">AND</span>${rule('env', '=', 'prod')}<span style="font-size: 11px; font-weight: 600; color: ${T.muted}">AND</span>${rule('zone', 'in', 'fra, hel')}${chip('+ condition')}</div>
            <div style="display: flex; align-items: center; gap: 8px; font-size: 12px; color: ${T.muted}">${ic.check}<span>Selector matches 5 hosts · re-evaluated when tags change · plus 2 manual members</span></div>
          </div>`, `${toggle('Auto-membership', true)}`)}
          ${card('Alert rules', `<div style="display: flex; flex-direction: column">
            <div style="display: grid; grid-template-columns: 200px minmax(0, 1fr) 60px minmax(0, 1fr) 60px; gap: 12px; padding: 8px 16px; border-bottom: 1px solid ${T.border}; font-size: 11px; color: ${T.muted}; text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600"><span>Check</span><span>Warning when</span><span></span><span>Critical when</span><span></span></div>
            ${alertRules.map(([c, w, ws, cr, cs]) => `<div style="display: grid; grid-template-columns: 200px minmax(0, 1fr) 60px minmax(0, 1fr) 60px; align-items: center; gap: 12px; padding: 10px 16px; border-bottom: 1px solid ${T.border}"><span style="font-weight: 600">${c}</span><span style="font-family: ${MONO}; font-size: 12px; color: ${w === '—' ? T.muted : T.text}">${w}</span><span>${ws ? pill('warn', T.warn) : ''}</span><span style="font-family: ${MONO}; font-size: 12px">${cr}</span><span>${pill('crit', T.crit)}</span></div>`).join('')}
          </div>`, `<span>5 rules · inherited by all members</span>${btn('Add rule')}`)}
          ${card('Notification routing', `<div style="display: flex; flex-direction: column">
            ${routeRow('Business hours · Mon–Fri 08–18', pill('warn+', T.warn), ['slack #ops-db', 'email ops@acme.io'])}
            ${routeRow('Nights &amp; weekends', pill('crit', T.crit), ['pagerduty db-oncall', 'telegram Tim'])}
            ${routeRow('Nights &amp; weekends', pill('warn', T.warn), ['email ops@acme.io (digest 08:00)'])}
            <div style="display: flex; align-items: center; gap: 10px; padding: 12px 16px; font-size: 12px"><span style="color: ${T.muted}">Escalation:</span><span>unacknowledged critical after</span>${box('15 min', '90px')}<span>→</span>${tag('telegram Tim')}${tag('sms +49 ···')}<span style="color: ${T.muted}">then every</span>${box('30 min', '90px')}</div>
          </div>`, `<span>3 routes · 1 escalation</span>${btn('Add route')}`)}
        </div>
        <div style="display: flex; flex-direction: column; gap: 16px">
          ${card('Members', `<div style="display: flex; flex-direction: column">${members.map(([s, h, k]) => `<div style="display: flex; align-items: center; gap: 10px; padding: 9px 16px; border-bottom: 1px solid ${T.border}">${dot(T[s])}<span style="font-family: ${MONO}; font-size: 12px; font-weight: 600; flex-grow: 1">${h}</span><span style="font-size: 11px; color: ${k === 'manual' ? T.accentDim : T.muted}">${k}</span></div>`).join('')}</div>`, `${btn('Add host')}`)}
          ${card('Check templates', `<div style="display: flex; flex-direction: column; gap: 8px; padding: 12px 16px; font-size: 12px">
            <div style="display: flex; align-items: center; gap: 8px">${ic.activity}<span style="flex-grow: 1">PostgreSQL · secret <span style="font-family: ${MONO}">pg_main</span></span><span style="color: ${T.muted}">30 s</span></div>
            <div style="display: flex; align-items: center; gap: 8px">${ic.disk}<span style="flex-grow: 1">Disk usage · all mounts</span><span style="color: ${T.muted}">60 s</span></div>
            <div style="display: flex; align-items: center; gap: 8px">${ic.cpu}<span style="flex-grow: 1">Service postgresql, pgbouncer</span><span style="color: ${T.muted}">30 s</span></div>
            <div style="display: flex; align-items: center; gap: 8px">${ic.globe}<span style="flex-grow: 1">TCP 5432 from zone worker</span><span style="color: ${T.muted}">60 s</span></div>
            <div style="color: ${T.muted}; margin-top: 4px">Pushed to every member's agent. Hosts missing the secret <span style="font-family: ${MONO}">pg_main</span> show a config warning.</div>
          </div>`, `${btn('Add')}`)}
          ${card('Status page', `<div style="display: flex; flex-direction: column; gap: 12px; padding: 14px 16px">
            ${toggle('Show on status page', true, 'status.acme.io')}
            ${input('Public display name', 'Database cluster', '100%', false)}
            <div style="font-size: 11px; color: ${T.muted}">Visitors see one aggregated component. Hostnames and metrics stay private.</div>
          </div>`, `${tag('status.acme.io')}`)}
          ${card('Maintenance', `<div style="display: flex; flex-direction: column; gap: 8px; padding: 12px 16px; font-size: 12px">
            <div style="display: flex; align-items: center; gap: 8px">${ic.clock}<div style="display: flex; flex-direction: column"><span style="font-weight: 600">PostgreSQL 17 minor upgrade</span><span style="color: ${T.muted}">Sat 13 Sep · 02:00–04:00 CEST · alerts silenced</span><span style="color: ${T.muted}">visibility: public on status.acme.io as "Database maintenance"</span></div></div>
          </div>`, `${btn('Schedule')}`)}
        </div>
      </div>`,
  height: 1180,
}));

// ================= STATUS PAGE (public) =================
const P = { bg: '#F6F4EF', card: '#FFFFFF', border: '#DCDAD4', text: '#1B1B1F', muted: '#6A6A72', accent: '#009CB2' };
seed = 3;
const bars = (state = 'ok') => { const arr = Array.from({ length: 90 }, () => 'ok'); if (state === 'warn') { arr[70] = 'warn'; arr[88] = 'warn'; arr[89] = 'warn'; } if (state === 'crit') { arr[52] = 'crit'; arr[89] = 'crit'; } if (state === 'mixed') { arr[12] = 'warn'; arr[61] = 'crit'; } return `<div style="display: flex; gap: 2px; flex-grow: 1">${arr.map(s => `<div style="flex: 1 1 0; height: 28px; border-radius: 2px; background: ${s === 'ok' ? '#34D399' : s === 'warn' ? '#FBBF24' : '#F87171'}; opacity: ${s === 'ok' ? 0.75 : 1}"></div>`).join('')}</div>`; };
const comp = (name, state, uptime, label) => `<div style="display: flex; flex-direction: column; gap: 8px; padding: 14px 0; border-bottom: 1px solid ${P.border}">
  <div style="display: flex; align-items: center; justify-content: space-between"><div style="display: flex; align-items: center; gap: 10px"><span style="font-size: 14px; font-weight: 600; color: ${P.text}">${name}</span></div><span style="font-size: 12px; font-weight: 600; color: ${state === 'ok' ? '#0F8A5F' : state === 'crit' ? '#C2410C' : '#B45309'}">${label}</span></div>
  ${bars(state)}
  <div style="display: flex; justify-content: space-between; font-size: 11px; color: ${P.muted}"><span>90 days ago</span><span>${uptime} uptime</span><span>today</span></div>
</div>`;
const statusPage = `<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <script src="./support.js"></script>
</head>
<body>
<x-dc>
<helmet>
  <link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@600;700;800&family=Inter:wght@400;500;600&display=swap">
  <style>
    body { margin: 0; font-family: Inter, system-ui, sans-serif; -webkit-font-smoothing: antialiased; }
    a { color: ${P.accent}; } a:hover { color: #007A8C; }
  </style>
</helmet>
<div style="width: 1440px; height: 1260px; background: ${P.bg}; color: ${P.text}; display: flex; flex-direction: column; align-items: center; overflow: hidden">
  <div style="width: 100%; box-sizing: border-box; display: flex; align-items: center; justify-content: space-between; padding: 0 48px; height: 64px; border-bottom: 1px solid ${P.border}; background: ${P.card}">
    <div style="display: flex; align-items: center; gap: 12px"><div style="width: 28px; height: 28px; border-radius: 7px; background: ${P.text}; display: flex; align-items: center; justify-content: center; color: #fff; font-weight: 700; font-size: 13px">A</div><span style="font-size: 15px; font-weight: 600">Acme Hosting</span><span style="font-size: 13px; color: ${P.muted}">Status</span></div>
    <div style="display: flex; align-items: center; gap: 16px; font-size: 13px"><a href="#" style="text-decoration: none">History</a><a href="#" style="text-decoration: none">Maintenance</a><div style="display: inline-flex; align-items: center; height: 34px; padding: 0 14px; border-radius: 6px; background: ${P.text}; color: #fff; font-weight: 600; font-size: 13px">Subscribe to updates</div></div>
  </div>
  <div style="width: 880px; display: flex; flex-direction: column; gap: 24px; padding: 40px 0">
    <div style="display: flex; align-items: center; gap: 14px; padding: 18px 22px; border-radius: 10px; background: #FEF3C7; border: 1px solid #FCD34D">
      <div style="width: 12px; height: 12px; border-radius: 50%; background: #F59E0B"></div><div style="display: flex; flex-direction: column"><span style="font-size: 17px; font-weight: 600; color: #78350F">Partial outage · Outgoing e-mail delayed</span><span style="font-size: 13px; color: #92400E">Investigating since 09:41 CEST · last update 12 min ago</span></div>
    </div>
    <div style="display: flex; flex-direction: column; padding: 4px 24px; border-radius: 10px; background: ${P.card}; border: 1px solid ${P.border}">
      ${comp('Website &amp; customer portal', 'ok', '99.99 %', 'Operational')}
      ${comp('API', 'ok', '99.97 %', 'Operational')}
      ${comp('Database cluster', 'mixed', '99.91 %', 'Operational')}
      ${comp('Outgoing e-mail', 'warn', '99.62 %', 'Degraded performance')}
      ${comp('Kubernetes workloads (Helsinki)', 'ok', '100 %', 'Operational')}
      <div style="display: flex; flex-direction: column; gap: 8px; padding: 14px 0">
        <div style="display: flex; align-items: center; justify-content: space-between"><span style="font-size: 14px; font-weight: 600">Object storage</span><span style="font-size: 12px; font-weight: 600; color: #0F8A5F">Operational</span></div>
        ${bars('ok')}
        <div style="display: flex; justify-content: space-between; font-size: 11px; color: ${P.muted}"><span>90 days ago</span><span>100 % uptime</span><span>today</span></div>
      </div>
    </div>
    <div style="display: flex; flex-direction: column; gap: 14px">
      <h2 style="margin: 0; font-size: 16px; font-weight: 600">Active incident</h2>
      <div style="display: flex; flex-direction: column; gap: 14px; padding: 20px 24px; border-radius: 10px; background: ${P.card}; border: 1px solid ${P.border}">
        <div style="display: flex; align-items: center; justify-content: space-between"><span style="font-size: 15px; font-weight: 600">Outgoing e-mail delayed</span><span style="font-size: 12px; color: ${P.muted}">affects Outgoing e-mail</span></div>
        <div style="display: flex; flex-direction: column; gap: 12px; padding-left: 14px; border-left: 2px solid ${P.border}">
          <div style="display: flex; flex-direction: column; gap: 3px"><span style="font-size: 12px; font-weight: 600; color: #B45309">Identified · 10:02 CEST</span><span style="font-size: 13px; line-height: 1.5; color: ${P.text}">The mail relay in Helsinki stopped accepting connections. Traffic is being rerouted through Falkenstein; queued messages will be delivered with up to 30 minutes delay.</span></div>
          <div style="display: flex; flex-direction: column; gap: 3px"><span style="font-size: 12px; font-weight: 600; color: ${P.muted}">Investigating · 09:41 CEST</span><span style="font-size: 13px; line-height: 1.5; color: ${P.text}">We are seeing increased delivery times for outgoing e-mail and are looking into it.</span></div>
        </div>
      </div>
    </div>
    <div style="display: flex; flex-direction: column; gap: 14px">
      <h2 style="margin: 0; font-size: 16px; font-weight: 600">Scheduled maintenance</h2>
      <div style="display: flex; align-items: center; gap: 14px; padding: 16px 24px; border-radius: 10px; background: ${P.card}; border: 1px solid ${P.border}">
        <div style="display: flex; flex-direction: column; align-items: center; justify-content: center; width: 52px; height: 52px; border-radius: 8px; background: ${P.bg}; border: 1px solid ${P.border}"><span style="font-size: 10px; font-weight: 600; color: ${P.muted}; text-transform: uppercase">Sep</span><span style="font-size: 18px; font-weight: 700; line-height: 1">13</span></div>
        <div style="display: flex; flex-direction: column; gap: 2px"><span style="font-size: 14px; font-weight: 600">Database cluster · PostgreSQL minor upgrade</span><span style="font-size: 13px; color: ${P.muted}">Saturday 02:00–04:00 CEST · short connection resets expected, no data loss</span></div>
      </div>
    </div>
    <div style="display: flex; align-items: center; justify-content: space-between; padding-top: 8px; font-size: 12px; color: ${P.muted}">
      <span>status.acme.io · times in CEST</span>
      <span style="display: flex; align-items: center; gap: 6px">Powered by <svg width="14" height="14" viewBox="0 0 64 64" fill="none"><path d="M32 5 L55 18.5 V45.5 L32 59 L9 45.5 V18.5 Z" stroke="${P.text}" stroke-width="5" stroke-linejoin="round"/><path d="M14 32 H23 L28 20 L35 44 L40 32 H50" stroke="${P.accent}" stroke-width="5.5" stroke-linecap="round" stroke-linejoin="round"/></svg><span style="font-family: ${MONO}; font-weight: 800; color: ${P.text}">pulsi<span style="color: ${P.accent}">x</span></span></span>
    </div>
  </div>
</div>
</x-dc>
</body>
</html>
`;
fs.writeFileSync('StatusPage.dc.html', statusPage);

// ================= JOB RUN =================
const logLine = (t, cls = '') => { const c = { play: T.accent, task: T.text, ok: T.ok, changed: T.warn, failed: T.crit, skip: T.muted, dim: T.muted }[cls] || T.dim; return `<div style="color: ${c}; white-space: pre">${t}</div>`; };
const jobLog = [
  ['PLAY [Deploy web tier] *********************************************************', 'play'],
  ['', ''],
  ['TASK [Gathering Facts] *********************************************************', 'task'],
  ['ok: [web-01.fra]', 'ok'], ['ok: [web-02.fra]', 'ok'], ['ok: [web-03.fra]', 'ok'], ['ok: [web-04.fra]', 'ok'],
  ['', ''],
  ['TASK [nginx : Render vhost configs] ********************************************', 'task'],
  ['changed: [web-01.fra] => (item=acme.io)', 'changed'], ['changed: [web-01.fra] => (item=shop.acme.io)', 'changed'],
  ['ok: [web-02.fra] => (item=acme.io)', 'ok'], ['changed: [web-03.fra] => (item=acme.io)', 'changed'], ['changed: [web-03.fra] => (item=shop.acme.io)', 'changed'],
  ['ok: [web-04.fra] => (item=blog.acme.io)', 'ok'],
  ['', ''],
  ['TASK [nginx : Validate configuration] ******************************************', 'task'],
  ['ok: [web-01.fra]', 'ok'], ['ok: [web-02.fra]', 'ok'],
  ['fatal: [web-03.fra]: FAILED! => {"changed": false, "cmd": "nginx -t", "msg": "nginx: [emerg] cannot load certificate \\"/etc/letsencrypt/live/shop.acme.io/fullchain.pem\\"", "rc": 1}', 'failed'],
  ['ok: [web-04.fra]', 'ok'],
  ['', ''],
  ['TASK [nginx : Reload service] **************************************************', 'task'],
  ['changed: [web-01.fra]', 'changed'], ['skipping: [web-02.fra]', 'skip'], ['ok: [web-04.fra]', 'ok'],
  ['', ''],
  ['TASK [php : Ensure php8.3-fpm pool] ********************************************', 'task'],
  ['ok: [web-01.fra]', 'ok'], ['ok: [web-02.fra]', 'ok'], ['changed: [web-04.fra]', 'changed'],
  ['', ''],
  ['TASK [app : Pull release 2026.09.10-1] *****************************************', 'task'],
  ['changed: [web-01.fra]', 'changed'], ['changed: [web-02.fra]', 'changed'],
  ['<span style="color: #00E5FF">▌</span> web-04.fra … running (12 s)', 'dim'],
];
const hostRes = [['ok', 'web-01.fra', 6, 3, 0, 0], ['ok', 'web-02.fra', 5, 1, 0, 1], ['crit', 'web-03.fra', 2, 2, 1, 0], ['run', 'web-04.fra', 5, 2, 0, 0]];
fs.writeFileSync('JobRun.dc.html', shell({
  active: 'Jobs',
  crumbs: `<span>Jobs</span>${ic.chevronR}<span>deploy-web</span>${ic.chevronR}<span style="color: ${T.text}; font-weight: 600; font-family: ${MONO}">#1432</span>`,
  content: `
      <div style="display: flex; align-items: flex-start; justify-content: space-between">
        <div style="display: flex; flex-direction: column; gap: 10px">
          <div style="display: flex; align-items: center; gap: 12px"><h1 style="margin: 0; font-family: ${MONO}; font-size: 22px; font-weight: 700">deploy-web.yml <span style="color: ${T.muted}; font-weight: 500">#1432</span></h1>${pill('running', T.accent)}<span style="font-size: 12px; color: ${T.muted}">started 2 m 40 s ago by Tim R. · template deploy-web · zone fra · worker fra-1</span></div>
          <div style="display: flex; gap: 6px; flex-wrap: wrap">${tag('repo: acme/infra @ main (a41c9e2)')}${tag('limit: group:webserver')}${tag('credential: acme-deploy')}${tag('extra: release=2026.09.10-1')}</div>
        </div>
        <div style="display: flex; gap: 8px">${btn('Cancel run')}${btn('Retry failed hosts', false, ic.play)}</div>
      </div>
      <div style="display: flex; flex-direction: column; gap: 6px">
        <div style="display: flex; justify-content: space-between; font-size: 12px; color: ${T.muted}"><span>Play 1 of 2 · task 6 of 11</span><span>3 of 4 hosts finished</span></div>
        <div style="display: flex; height: 6px; border-radius: 999px; overflow: hidden; background: ${T.border}"><div style="width: 40%; background: ${T.ok}"></div><div style="width: 14%; background: ${T.crit}"></div><div style="width: 8%; background: ${T.accent}"></div></div>
      </div>
      <div style="display: grid; grid-template-columns: minmax(0, 1fr) 380px; gap: 16px; flex-grow: 1; min-height: 0">
        ${card('Live log', `<div style="display: flex; flex-direction: column; gap: 1px; padding: 14px 16px; font-family: ${MONO}; font-size: 12px; line-height: 1.55; background: ${T.ink}; overflow: hidden">${jobLog.map(([t, c]) => logLine(t, c)).join('')}</div>`, `<span style="display: flex; align-items: center; gap: 6px">${dot(T.accent, 6)} streaming</span>${chip('Follow', true)}${chip('Raw')}${chip('Filter: failed')}`)}
        <div style="display: flex; flex-direction: column; gap: 16px">
          ${card('Hosts', `<div style="display: flex; flex-direction: column">
            <div style="display: grid; grid-template-columns: 20px minmax(0, 1fr) 36px 36px 36px 36px; gap: 8px; padding: 8px 16px; border-bottom: 1px solid ${T.border}; font-size: 10px; color: ${T.muted}; text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600"><span></span><span>Host</span><span>ok</span><span>chg</span><span>fail</span><span>skip</span></div>
            ${hostRes.map(([s, h, ok, ch, f, sk]) => `<div style="display: grid; grid-template-columns: 20px minmax(0, 1fr) 36px 36px 36px 36px; align-items: center; gap: 8px; padding: 9px 16px; border-bottom: 1px solid ${T.border}; font-family: ${MONO}; font-size: 12px">${s === 'run' ? `<span style="display: inline-block; width: 8px; height: 8px; border-radius: 50%; border: 2px solid ${T.accent}; border-top-color: transparent"></span>` : dot(T[s])}<span style="font-weight: 600">${h}</span><span style="color: ${T.ok}">${ok}</span><span style="color: ${T.warn}">${ch}</span><span style="color: ${f ? T.crit : T.muted}">${f}</span><span style="color: ${T.muted}">${sk}</span></div>`).join('')}
          </div>`, `<span>4 hosts</span>`)}
          ${card('Failed task · web-03.fra', `<div style="display: flex; flex-direction: column; gap: 8px; padding: 12px 16px; font-size: 12px">
            <span style="font-weight: 600">nginx : Validate configuration</span>
            <div style="padding: 10px; border-radius: 6px; background: ${T.ink}; border: 1px solid ${T.crit}55; font-family: ${MONO}; font-size: 11px; color: ${T.crit}; line-height: 1.5">nginx: [emerg] cannot load certificate "/etc/letsencrypt/live/shop.acme.io/fullchain.pem"</div>
            <span style="color: ${T.muted}">Host var <span style="font-family: ${MONO}; color: ${T.text}">vhosts[1].ssl = true</span> but no certificate on the host. Open host vars or run <span style="font-family: ${MONO}; color: ${T.text}">issue-certs.yml</span> first.</span>
            <div style="display: flex; gap: 8px">${btn('Open host vars', false, ic.key)}${btn('Retry web-03.fra')}</div>
          </div>`, `${pill('1 failed', T.crit)}`)}
          ${card('Timeline', `<div style="display: flex; flex-direction: column; gap: 6px; padding: 12px 16px; font-size: 12px; color: ${T.dim}">
            <div style="display: flex; justify-content: space-between"><span>Queued</span><span style="font-family: ${MONO}">10:31:02</span></div>
            <div style="display: flex; justify-content: space-between"><span>Claimed by fra-1 · git fetch · requirements</span><span style="font-family: ${MONO}">10:31:04</span></div>
            <div style="display: flex; justify-content: space-between"><span>Inventory snapshot · 4 hosts</span><span style="font-family: ${MONO}">10:31:09</span></div>
            <div style="display: flex; justify-content: space-between"><span>Playbook started</span><span style="font-family: ${MONO}">10:31:10</span></div>
          </div>`, ``)}
        </div>
      </div>`,
  height: 1180,
}));

// ================= ENROLLMENT =================
const step = (n, l, state) => `<div style="display: flex; align-items: center; gap: 10px"><div style="width: 26px; height: 26px; border-radius: 50%; display: flex; align-items: center; justify-content: center; font-size: 12px; font-weight: 700; ${state === 'done' ? `background: ${T.ok}; color: ${T.ink}` : state === 'active' ? `background: ${T.accent}; color: ${T.ink}` : `border: 1px solid ${T.border}; color: ${T.muted}`}">${state === 'done' ? ic.check : n}</div><span style="font-size: 13px; font-weight: ${state === 'active' ? 600 : 500}; color: ${state === 'todo' ? T.muted : T.text}">${l}</span></div>`;
const codeBlock = (lines) => `<div style="display: flex; flex-direction: column; gap: 2px; padding: 12px 14px; border-radius: 8px; background: ${T.ink}; border: 1px solid ${T.border}; font-family: ${MONO}; font-size: 12px; line-height: 1.6; position: relative">${lines.map(l => `<div style="white-space: pre-wrap; word-break: break-all">${l}</div>`).join('')}<div style="position: absolute; top: 8px; right: 8px; font-size: 11px; color: ${T.accentDim}; font-family: Inter, sans-serif">copy</div></div>`;
const progressRow = (state, label, detail) => `<div style="display: flex; align-items: center; gap: 10px; padding: 8px 0; font-size: 12px">${state === 'done' ? `<span style="color: ${T.ok}">${ic.check}</span>` : state === 'wait' ? `<span style="display: inline-block; width: 10px; height: 10px; border-radius: 50%; border: 2px solid ${T.accent}; border-top-color: transparent; margin: 0 2px"></span>` : `<span style="width: 14px; height: 14px; border-radius: 50%; border: 1px solid ${T.border}"></span>`}<span style="font-weight: 500; color: ${state === 'todo' ? T.muted : T.text}">${label}</span><span style="color: ${T.muted}; margin-left: auto; font-family: ${MONO}">${detail}</span></div>`;
fs.writeFileSync('Enrollment.dc.html', shell({
  active: 'Hosts',
  crumbs: `<span>Hosts</span>${ic.chevronR}<span style="color: ${T.text}; font-weight: 600">Add host</span>`,
  content: `
      <div style="display: flex; align-items: center; justify-content: space-between">
        <div style="display: flex; flex-direction: column; gap: 2px"><h1 style="margin: 0; font-size: 20px; font-weight: 600">Add host</h1><span style="font-size: 13px; color: ${T.muted}">Enroll an agent, register SSH access, or both.</span></div>
        <div style="display: flex; align-items: center; gap: 28px">${step(1, 'Basics', 'done')}<span style="width: 40px; height: 1px; background: ${T.border}"></span>${step(2, 'Install agent', 'active')}<span style="width: 40px; height: 1px; background: ${T.border}"></span>${step(3, 'SSH access', 'todo')}<span style="width: 40px; height: 1px; background: ${T.border}"></span>${step(4, 'Done', 'todo')}</div>
      </div>
      <div style="display: grid; grid-template-columns: minmax(0, 1fr) 400px; gap: 16px; flex-grow: 1; min-height: 0">
        <div style="display: flex; flex-direction: column; gap: 16px">
          ${card('Host', `<div style="display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 12px; padding: 16px">${input('Hostname', 'mail-02.hel')}${select('Zone', 'hel · Hetzner Helsinki')}<div style="display: flex; flex-direction: column; gap: 5px"><span style="font-size: 11px; color: ${T.muted}; font-weight: 500">Tags</span><div style="display: flex; gap: 6px; flex-wrap: wrap; min-height: 32px; padding: 5px 8px; border-radius: 6px; background: ${T.ink}; border: 1px solid ${T.border}; align-items: center">${tag('role=mail')}${tag('env=prod')}<span style="font-size: 11px; color: ${T.muted}">+</span></div></div>
            <div style="grid-column: span 3; display: flex; gap: 32px; padding-top: 4px">${toggle('Monitored · install agent', true, 'metrics, checks, facts')}${toggle('Managed · Ansible via SSH', true, 'asked in step 3')}</div>
          </div>`, `${pill('step 1 done', T.ok)}`)}
          ${card('Install the agent', `<div style="display: flex; flex-direction: column; gap: 14px; padding: 16px">
            <div style="display: flex; gap: 8px">${chip('Linux (systemd)', true)}${chip('Windows (service)')}${chip('macOS (launchd)')}${chip('Docker')}</div>
            <div style="font-size: 12px; color: ${T.dim}">Run as root on <span style="font-family: ${MONO}; color: ${T.text}">mail-02.hel</span>. The token is valid for 24 h and can be used once.</div>
            ${codeBlock([`curl -fsSL https://pulsix.acme.io/install.sh | sh -s -- \\`, `  --token <span style="color: ${T.accent}">pxe_3f9Kq2…Vb7</span> \\`, `  --server fra-1.pulsix.acme.io:8443`])}
            <div style="display: flex; flex-direction: column; gap: 4px; font-size: 12px; color: ${T.muted}">
              <span>The script downloads <span style="font-family: ${MONO}; color: ${T.text}">pulsix-agent</span> (linux/amd64, v0.4.2, sha256 verified), writes <span style="font-family: ${MONO}; color: ${T.text}">/etc/pulsix/agent.yaml</span>, requests a client certificate with the token, and enables the systemd unit.</span>
              <span>Add database or web server credentials to <span style="font-family: ${MONO}; color: ${T.text}">agent.yaml</span> under <span style="font-family: ${MONO}; color: ${T.text}">secrets:</span>; they never leave the host.</span>
            </div>
            <div style="display: flex; align-items: center; justify-content: space-between; padding-top: 4px">${chip('Regenerate token')}<div style="display: flex; gap: 8px">${btn('Skip agent')}${btn('Continue when connected', true)}</div></div>
          </div>`, `${tag('token expires in 23 h 58 m')}`)}
        </div>
        <div style="display: flex; flex-direction: column; gap: 16px">
          ${card('Waiting for agent', `<div style="display: flex; flex-direction: column; padding: 8px 16px">
            ${progressRow('done', 'Token created', '10:41:07')}
            ${progressRow('done', 'Agent connected to fra-1', 'TLS 1.3')}
            ${progressRow('done', 'Certificate issued', 'serial 0x4a1f · 90 d')}
            ${progressRow('wait', 'Receiving facts', '')}
            ${progressRow('todo', 'Default checks applied', 'Linux base')}
            ${progressRow('todo', 'First metrics received', '')}
          </div>`, `<span style="display: flex; align-items: center; gap: 6px">${dot(T.accent, 6)} live</span>`)}
          ${card('What the agent does', `<div style="display: flex; flex-direction: column; gap: 8px; padding: 12px 16px; font-size: 12px; color: ${T.dim}; line-height: 1.5">
            <span>Outbound only, one mTLS connection to the zone worker. No open port on the host.</span>
            <span>Collects CPU, memory, disks, services, network, SMART and whatever checks you bind to this host.</span>
            <span>Renews its certificate automatically. Revoke it from the host page at any time.</span>
          </div>`, ``)}
          ${card('Other ways to add hosts', `<div style="display: flex; flex-direction: column; gap: 8px; padding: 12px 16px; font-size: 12px">
            <div style="display: flex; align-items: center; gap: 8px">${ic.git}<span>Import from an Ansible inventory file</span></div>
            <div style="display: flex; align-items: center; gap: 8px">${ic.play}<span>Roll out agents with the <span style="font-family: ${MONO}">pulsix.agent</span> role</span></div>
            <div style="display: flex; align-items: center; gap: 8px">${ic.code}<span>API: <span style="font-family: ${MONO}">POST /api/v1/hosts</span> + enrollment token</span></div>
          </div>`, ``)}
        </div>
      </div>`,
  height: 1000,
}));

// ================= RULE EDITOR =================
const condRow = (metric, op, val, dur, sev, clear) => `<div style="display: grid; grid-template-columns: 90px minmax(0, 1fr) 70px 90px 110px 120px; align-items: end; gap: 10px">
  <div style="display: flex; align-items: center; height: 32px">${pill(sev, T[sev])}</div>${select('Metric', metric)}${select('Op', op)}${input('Value', val)}${input('For', dur)}${input('Clear below', clear)}
</div>`;
const effRow = (src, cond, wins, sev) => `<div style="display: grid; grid-template-columns: 150px minmax(0, 1fr) 60px; align-items: center; gap: 10px; padding: 9px 16px; border-bottom: 1px solid ${T.border}; ${wins ? '' : 'opacity: 0.55'}"><span style="font-size: 11px; color: ${wins ? T.accent : T.muted}; font-weight: 600">${src}</span><span style="font-family: ${MONO}; font-size: 11px">${cond}</span><span>${pill(sev, T[sev])}</span></div>`;
fs.writeFileSync('RuleEditor.dc.html', shell({
  active: 'Monitoring',
  crumbs: `<span>Monitoring</span>${ic.chevronR}<span>Rules</span>${ic.chevronR}<span style="color: ${T.text}; font-weight: 600">Disk usage · prod-databases</span>`,
  content: `
      <div style="display: flex; align-items: flex-start; justify-content: space-between">
        <div style="display: flex; flex-direction: column; gap: 8px">
          <div style="display: flex; align-items: center; gap: 12px"><h1 style="margin: 0; font-size: 20px; font-weight: 600">Disk usage rule</h1>${tag('plugin: disk')}${tag('scope: group prod-databases')}<span style="font-size: 12px; color: ${T.muted}">overrides org default · affects 47 items on 7 hosts</span></div>
          <div style="display: flex; gap: 24px; border-bottom: 1px solid ${T.border}">${tab('Rule', true)}${tab('Affected items')}${tab('History')}</div>
        </div>
        <div style="display: flex; gap: 8px">${btn('Discard')}${btn('Save rule', true, ic.check)}</div>
      </div>
      <div style="display: grid; grid-template-columns: minmax(0, 1fr) 420px; gap: 16px; flex-grow: 1; min-height: 0">
        <div style="display: flex; flex-direction: column; gap: 16px">
          ${card('Scope &amp; conditions', `<div style="display: flex; flex-direction: column; gap: 14px; padding: 16px">
            <div style="display: flex; flex-direction: column; gap: 6px"><span style="font-size: 11px; color: ${T.muted}; font-weight: 500">Applies to</span>${seg(['Org default', 'Monitoring group', 'Host', 'Item'], 'Monitoring group')}</div>
            <div style="display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px">${select('Monitoring group', 'prod-databases')}${input('Item filter', 'mount = /var/lib/postgresql*')}</div>
            <div style="display: flex; align-items: center; gap: 8px; flex-wrap: wrap"><span style="font-size: 11px; color: ${T.muted}; font-weight: 500">Only when</span>${rule('tag os', '=', 'debian')}${chip('+ condition')}</div>
          </div>`, ``)}
          ${card('Thresholds', `<div style="display: flex; flex-direction: column; gap: 16px; padding: 16px">
            ${condRow('disk.used_pct', '≥', '80', '5m', 'warn', '75')}
            ${condRow('disk.used_pct', '≥', '90', '2m', 'crit', '85')}
            ${condRow('disk.days_until_full', '<', '7', '1h', 'warn', '10')}
            <div style="display: flex; align-items: center; gap: 12px; padding-top: 4px; border-top: 1px solid ${T.border}">${chip('+ threshold')}<span style="font-size: 12px; color: ${T.muted}">Highest matching severity wins. "Clear below" adds hysteresis so states don't flap around the threshold.</span></div>
          </div>`, `${seg(['Form', 'Expression'], 'Form')}`)}
          ${card('Expression (read-only while in form mode)', `<div style="padding: 14px 16px">${codeBlock([`<span style="color: ${T.warn}">warn</span>  when avg(disk.used_pct, 5m) &gt;= 80 clear &lt; 75`, `<span style="color: ${T.crit}">crit</span>  when avg(disk.used_pct, 2m) &gt;= 90 clear &lt; 85`, `<span style="color: ${T.warn}">warn</span>  when disk.days_until_full &lt; 7 for 1h clear &gt;= 10`, `<span style="color: ${T.muted}">where item.mount ~ "/var/lib/postgresql*" and tag.os = "debian"</span>`])}</div>`, `<span>switch to Expression to edit freely</span>`)}
        </div>
        <div style="display: flex; flex-direction: column; gap: 16px">
          ${card('Effective rules · db-02.fra · /var/lib/postgresql', `<div style="display: flex; flex-direction: column">
            ${effRow('item override', '—', false, 'unknown').replace('unknown', 'none').replace(T.unknown, T.muted)}
            ${effRow('host db-02.fra', '—', false, 'unknown').replace('unknown', 'none').replace(T.unknown, T.muted)}
            ${effRow('group prod-databases', 'used_pct ≥ 90 for 2m', true, 'crit')}
            ${effRow('group prod-databases', 'used_pct ≥ 80 for 5m', true, 'warn')}
            ${effRow('org default', 'used_pct ≥ 85 for 10m', false, 'warn')}
            ${effRow('plugin default', 'used_pct ≥ 90 for 10m', false, 'crit')}
            <div style="padding: 10px 16px; font-size: 11px; color: ${T.muted}">Current value <span style="font-family: ${MONO}; color: ${T.crit}">92 %</span> → state <span style="color: ${T.crit}; font-weight: 600">crit</span> since 14 min. Precedence: item &gt; host &gt; group &gt; org &gt; plugin.</div>
          </div>`, `${select('', 'db-02.fra', '140px')}`)}
          ${card('Preview against last 24 h', `<div style="padding: 14px 16px 8px">${chart({ w: 330, h: 90, lines: [{ data: diskS, color: T.crit, fill: true }], grid: 2, labels: ['-24h', '-12h', 'now'] })}
            <div style="display: flex; gap: 12px; font-size: 11px; color: ${T.muted}; padding-top: 6px"><span style="display: flex; align-items: center; gap: 6px">${dot(T.crit, 6)} crit ≥ 90 for 2m: would fire 1×</span><span style="display: flex; align-items: center; gap: 6px">${dot(T.warn, 6)} warn: 1×</span></div>
          </div>`, `<span>47 items</span>`)}
        </div>
      </div>`,
  height: 1080,
}));

// ================= STATUS PAGE ADMIN =================
const compRow = (name, target, kind, state) => `<div style="display: grid; grid-template-columns: 20px 20px minmax(0, 1fr) minmax(0, 1fr) 90px; align-items: center; gap: 10px; padding: 9px 16px; border-bottom: 1px solid ${T.border}"><span style="color: ${T.muted}; cursor: grab">${ic.more}</span>${dot(T[state])}<span style="font-weight: 600">${name}</span><span style="display: flex; align-items: center; gap: 6px; font-size: 11px; color: ${T.muted}">${kind === 'group' ? ic.layers : kind === 'host' ? ic.server : ic.activity}<span style="font-family: ${MONO}">${target}</span></span><span style="font-size: 11px; color: ${T.muted}; text-align: right">${kind}</span></div>`;
const incRow = (state, title, when, color) => `<div style="display: flex; align-items: center; gap: 10px; padding: 9px 16px; border-bottom: 1px solid ${T.border}; font-size: 12px">${pill(state, color)}<span style="font-weight: 600; flex-grow: 1">${title}</span><span style="color: ${T.muted}; font-family: ${MONO}; font-size: 11px">${when}</span></div>`;
fs.writeFileSync('StatusAdmin.dc.html', shell({
  active: 'Settings',
  crumbs: `<span>Settings</span>${ic.chevronR}<span>Status pages</span>${ic.chevronR}<span style="color: ${T.text}; font-weight: 600; font-family: ${MONO}">status.acme.io</span>`,
  content: `
      <div style="display: flex; align-items: flex-start; justify-content: space-between">
        <div style="display: flex; flex-direction: column; gap: 8px">
          <div style="display: flex; align-items: center; gap: 12px"><h1 style="margin: 0; font-family: ${MONO}; font-size: 22px; font-weight: 700">status.acme.io</h1>${pill('partial outage', T.warn)}<span style="font-size: 12px; color: ${T.muted}">6 components · 1 active incident · 312 subscribers</span></div>
          <div style="display: flex; gap: 24px; border-bottom: 1px solid ${T.border}">${tab('Components', true)}${tab('Incidents')}${tab('Maintenance')}${tab('Subscribers')}${tab('Appearance')}${tab('Domain')}</div>
        </div>
        <div style="display: flex; gap: 8px">${btn('Open public page', false, ic.globe)}${btn('New incident', true, ic.bell)}</div>
      </div>
      <div style="display: grid; grid-template-columns: minmax(0, 1fr) 420px; gap: 16px; flex-grow: 1; min-height: 0">
        <div style="display: flex; flex-direction: column; gap: 16px">
          ${card('Components', `<div style="display: flex; flex-direction: column">
            <div style="display: grid; grid-template-columns: 20px 20px minmax(0, 1fr) minmax(0, 1fr) 90px; gap: 10px; padding: 8px 16px; border-bottom: 1px solid ${T.border}; font-size: 10px; color: ${T.muted}; text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600"><span></span><span></span><span>Public name</span><span>Source</span><span>Kind</span></div>
            ${compRow('Website &amp; customer portal', 'mg: public-web', 'group', 'ok')}
            ${compRow('API', 'item: http api.acme.io/health', 'check', 'ok')}
            ${compRow('Database cluster', 'mg: prod-databases', 'group', 'ok')}
            ${compRow('Outgoing e-mail', 'mg: mail-relays', 'group', 'warn')}
            ${compRow('Kubernetes workloads (Helsinki)', 'mg: k3s-hel', 'group', 'ok')}
            ${compRow('Object storage', 'host: s3-gw-01.fra', 'host', 'ok')}
            <div style="display: flex; align-items: center; gap: 12px; padding: 12px 16px; font-size: 12px; color: ${T.muted}">${chip('+ Add component')}<span>Drag to reorder. Groups and hosts can also opt in from their own page; they appear here for naming.</span></div>
          </div>`, `<span>state = worst of source</span>`)}
          ${card('New incident', `<div style="display: flex; flex-direction: column; gap: 12px; padding: 16px">
            <div style="display: grid; grid-template-columns: minmax(0, 1fr) 180px; gap: 12px">${input('Title', 'Outgoing e-mail delayed', '100%', false)}${select('Status', 'Identified')}</div>
            <div style="display: flex; flex-direction: column; gap: 5px"><span style="font-size: 11px; color: ${T.muted}; font-weight: 500">Affected components</span><div style="display: flex; gap: 6px; flex-wrap: wrap; min-height: 32px; padding: 5px 8px; border-radius: 6px; background: ${T.ink}; border: 1px solid ${T.border}; align-items: center">${tag('Outgoing e-mail')}<span style="font-size: 11px; color: ${T.muted}">+</span></div></div>
            <div style="display: flex; flex-direction: column; gap: 5px"><span style="font-size: 11px; color: ${T.muted}; font-weight: 500">Update (Markdown)</span><div style="min-height: 72px; padding: 10px; border-radius: 6px; background: ${T.ink}; border: 1px solid ${T.border}; font-size: 12px; line-height: 1.5; color: ${T.dim}">The mail relay in Helsinki stopped accepting connections. Traffic is being rerouted through Falkenstein; queued messages will be delivered with up to 30 minutes delay.</div></div>
            <div style="display: flex; align-items: center; justify-content: space-between">${toggle('Notify 312 subscribers', true)}<div style="display: flex; gap: 8px">${btn('Save draft')}${btn('Publish update', true)}</div></div>
          </div>`, `${tag('linked alert #8812 · mail-01.hel')}`)}
        </div>
        <div style="display: flex; flex-direction: column; gap: 16px">
          ${card('Incidents', `<div style="display: flex; flex-direction: column">
            ${incRow('identified', 'Outgoing e-mail delayed', 'today 09:41', T.warn)}
            ${incRow('resolved', 'API latency in fra', 'Sep 02', T.ok)}
            ${incRow('resolved', 'Database failover', 'Aug 19', T.ok)}
          </div>`, `${ic.chevronR}`)}
          ${card('Scheduled maintenance', `<div style="display: flex; flex-direction: column; gap: 4px; padding: 12px 16px; font-size: 12px">
            <span style="font-weight: 600">Database cluster · PostgreSQL minor upgrade</span><span style="color: ${T.muted}">Sat 13 Sep 02:00–04:00 CEST · from maintenance window on prod-databases · public</span>
          </div>`, `${btn('Schedule')}`)}
          ${card('Page settings', `<div style="display: flex; flex-direction: column; gap: 12px; padding: 16px">
            ${input('Slug', 'acme  →  acme.status.pulsix.dev')}
            ${input('Custom domain', 'status.acme.io  ·  CNAME verified')}
            <div style="display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px">${input('Timezone', 'Europe/Berlin', '100%', false)}${input('Logo', 'acme-mark.svg', '100%', false)}</div>
            ${toggle('Allow e-mail subscriptions', true, 'double opt-in')}
            ${toggle('Show uptime percentages', true)}
            ${toggle('Public page', true, 'off = private link with token')}
          </div>`, ``)}
        </div>
      </div>`,
  height: 1080,
}));

// ================= MEMBERS / ACL =================
const roleBadge = (r) => { const c = { owner: T.accent, admin: '#A78BFA', operator: T.ok, viewer: T.dim }[r]; return `<span style="display: inline-flex; align-items: center; gap: 6px; padding: 3px 9px; border-radius: 999px; font-size: 11px; font-weight: 600; color: ${c}; background: ${c}1A; border: 1px solid ${c}33">${r}</span>`; };
const avatar = (ini, bg = T.accentDim) => `<div style="width: 28px; height: 28px; border-radius: 50%; background: ${bg}; display: flex; align-items: center; justify-content: center; font-size: 11px; font-weight: 700; color: ${T.ink}; flex-shrink: 0">${ini}</div>`;
const memberRows = [
  ['TR', 'Tim R.', 'tim@acme.io', 'owner', 'passkey · TOTP', '2 min ago', 'active'],
  ['DG', 'Daniel G.', 'daniel@acme.io', 'owner', 'passkey', '1 h ago', 'active'],
  ['SK', 'Sara K.', 'sara@acme.io', 'admin', 'TOTP', 'yesterday', 'active'],
  ['MB', 'Marc B.', 'marc@acme.io', 'operator', 'TOTP', '3 d ago', 'active'],
  ['CI', 'ci-deploy', 'token · created by TR', 'operator', '—', '12 min ago', 'token'],
  ['JL', 'Jana L.', 'jana@acme.io', 'viewer', 'passkey', '2 w ago', 'active'],
  ['??', 'ops@acme.io', 'invited by SK · expires in 5 d', 'operator', '—', '—', 'invited'],
  ['PK', 'Peter K.', 'peter@acme.io', 'viewer', 'TOTP', '3 mo ago', 'disabled'],
];
const perms = [['View hosts, groups, jobs, alerts', [1, 1, 1, 1]], ['Change hosts, groups, variables', [0, 1, 1, 1]], ['Run playbooks, acknowledge alerts', [0, 1, 1, 1]], ['Manage members and tokens', [0, 0, 1, 1]], ['Manage channels, status pages, settings', [0, 0, 1, 1]], ['Grant or revoke owner', [0, 0, 0, 1]], ['Delete organization', [0, 0, 0, 1]]];
const permCell = (on) => on ? `<span style="color: ${T.ok}">${ic.check}</span>` : `<span style="color: ${T.border}">—</span>`;
fs.writeFileSync('Members.dc.html', shell({
  active: 'Settings',
  crumbs: `<span>Settings</span>${ic.chevronR}<span style="color: ${T.text}; font-weight: 600">Members</span>`,
  content: `
      <div style="display: flex; align-items: flex-start; justify-content: space-between">
        <div style="display: flex; flex-direction: column; gap: 8px">
          <div style="display: flex; align-items: center; gap: 12px"><h1 style="margin: 0; font-size: 20px; font-weight: 600">Members</h1><span style="font-size: 12px; color: ${T.muted}">7 members · 1 token · 1 pending invitation</span></div>
          <div style="display: flex; gap: 24px; border-bottom: 1px solid ${T.border}">${tab('Members', true)}${tab('Invitations')}${tab('API tokens')}${tab('Roles')}</div>
        </div>
        <div style="display: flex; gap: 8px">${btn('Create token', false, ic.key)}${btn('Invite member', true, ic.user)}</div>
      </div>
      <div style="display: grid; grid-template-columns: minmax(0, 1fr) 400px; gap: 16px; flex-grow: 1; min-height: 0">
        <div style="display: flex; flex-direction: column; gap: 16px">
          <div style="display: flex; flex-direction: column; background: ${T.surface}; border: 1px solid ${T.border}; border-radius: 10px; overflow: hidden">
            <div style="display: grid; grid-template-columns: 36px minmax(0, 1fr) 110px 120px 110px 90px 32px; gap: 12px; padding: 10px 16px; border-bottom: 1px solid ${T.border}; font-size: 11px; color: ${T.muted}; text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600"><span></span><span>Member</span><span>Role</span><span>Second factor</span><span>Last active</span><span>Status</span><span></span></div>
            ${memberRows.map(([ini, name, mail, role, mfa, last, st]) => `<div style="display: grid; grid-template-columns: 36px minmax(0, 1fr) 110px 120px 110px 90px 32px; align-items: center; gap: 12px; padding: 10px 16px; border-bottom: 1px solid ${T.border}; ${st === 'disabled' ? 'opacity: 0.5' : ''}">
              ${avatar(ini, st === 'token' ? T.border : st === 'invited' ? T.border : T.accentDim)}<div style="display: flex; flex-direction: column; min-width: 0"><span style="font-weight: 600; ${st === 'token' ? `font-family: ${MONO}; font-size: 12px` : ''}">${name}</span><span style="font-size: 11px; color: ${T.muted}; overflow: hidden; text-overflow: ellipsis; white-space: nowrap">${mail}</span></div>
              <div style="display: flex; align-items: center; gap: 4px">${roleBadge(role)}${st === 'active' ? `<span style="color: ${T.muted}">${ic.chevron}</span>` : ''}</div><span style="font-size: 12px; color: ${T.dim}">${mfa}</span><span style="font-size: 12px; color: ${T.muted}">${last}</span>
              <span>${st === 'active' ? pill('active', T.ok) : st === 'token' ? tag('token') : st === 'invited' ? pill('invited', T.warn) : pill('disabled', T.muted)}</span><span style="color: ${T.muted}">${ic.more}</span>
            </div>`).join('')}
          </div>
          ${card('Invite member', `<div style="display: flex; flex-direction: column; gap: 14px; padding: 16px">
            <div style="display: grid; grid-template-columns: minmax(0, 1fr) 160px; gap: 12px">${input('E-mail address', 'new.colleague@acme.io', '100%', false)}${select('Role', 'operator')}</div>
            <div style="display: flex; flex-direction: column; gap: 6px; font-size: 12px; color: ${T.muted}">
              <div style="display: flex; align-items: center; gap: 8px">${ic.check}<span>Invitation link valid for 7 days, sent by e-mail. If mail is not configured the link is shown here.</span></div>
              <div style="display: flex; align-items: center; gap: 8px">${ic.check}<span>The invitee sets a password and must enrol a second factor before entering the organization.</span></div>
              <div style="display: flex; align-items: center; gap: 8px">${ic.check}<span>Owner role cannot be granted by invitation; promote after joining.</span></div>
            </div>
            <div style="display: flex; justify-content: flex-end; gap: 8px">${btn('Cancel')}${btn('Send invitation', true)}</div>
          </div>`, `<span>dialog preview</span>`)}
        </div>
        <div style="display: flex; flex-direction: column; gap: 16px">
          ${card('Role matrix', `<div style="display: flex; flex-direction: column">
            <div style="display: grid; grid-template-columns: minmax(0, 1fr) 44px 44px 44px 44px; gap: 6px; padding: 8px 16px; border-bottom: 1px solid ${T.border}; font-size: 10px; color: ${T.muted}; text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600"><span>Permission</span><span style="text-align: center">view</span><span style="text-align: center">oper</span><span style="text-align: center">adm</span><span style="text-align: center">own</span></div>
            ${perms.map(([l, v]) => `<div style="display: grid; grid-template-columns: minmax(0, 1fr) 44px 44px 44px 44px; align-items: center; gap: 6px; padding: 8px 16px; border-bottom: 1px solid ${T.border}; font-size: 12px"><span>${l}</span>${v.map(x => `<span style="display: flex; justify-content: center">${permCell(x)}</span>`).join('')}</div>`).join('')}
            <div style="padding: 10px 16px; font-size: 11px; color: ${T.muted}">Roles are fixed in this version. API tokens are capped at their creator's role.</div>
          </div>`, ``)}
          ${card('Change role · Marc B.', `<div style="display: flex; flex-direction: column; gap: 10px; padding: 14px 16px">
            ${['viewer', 'operator', 'admin', 'owner'].map(r => `<div style="display: flex; align-items: center; gap: 10px; padding: 8px 10px; border-radius: 6px; border: 1px solid ${r === 'operator' ? T.accent : T.border}; ${r === 'owner' ? 'opacity: 0.45' : ''}"><div style="width: 14px; height: 14px; border-radius: 50%; border: 2px solid ${r === 'operator' ? T.accent : T.border}; background: ${r === 'operator' ? T.accent : 'transparent'}"></div>${roleBadge(r)}<span style="font-size: 11px; color: ${T.muted}; margin-left: auto">${r === 'owner' ? 'owners only' : ''}</span></div>`).join('')}
            <div style="display: flex; justify-content: space-between; align-items: center; padding-top: 4px"><span style="font-size: 11px; color: ${T.crit}">Remove from organization</span><div style="display: flex; gap: 8px">${btn('Cancel')}${btn('Save', true)}</div></div>
          </div>`, `<span>as admin</span>`)}
        </div>
      </div>`,
  height: 1140,
}));

// ================= ACTIVITY FEED =================
const feed = [
  ['today', [
    ['12:04', 'TR', 'Tim R.', 'ran playbook', 'deploy-web.yml #1432', 'job', 'limit group:webserver · 1 host failed', T.crit],
    ['11:52', 'SK', 'Sara K.', 'acknowledged alert', 'db-02.fra · Disk usage', 'alert', '"pg_dump running, cleaning up after"', T.warn],
    ['11:40', 'SK', 'Sara K.', 'invited', 'ops@acme.io', 'member', 'role operator', T.text],
    ['10:31', 'TR', 'Tim R.', 'changed variables on', 'web-03.fra', 'host', 'vhosts[1].ssl: false → true', T.text],
    ['10:15', 'CI', 'ci-deploy (token)', 'created host', 'web-05.fra', 'host', 'zone fra · managed · monitored', T.text],
    ['09:41', 'system', 'Pulsix', 'opened alert', 'mail-01.hel · Service postfix', 'alert', 'critical · notified pagerduty db-oncall', T.crit],
    ['09:02', 'DG', 'Daniel G.', 'changed rule', 'Disk usage · prod-databases', 'rule', 'crit threshold 95 % → 90 %', T.text],
  ]],
  ['yesterday', [
    ['17:20', 'MB', 'Marc B.', 'assigned', 'db-03.fra', 'group', 'to inventory group postgres · 3 host vars set', T.text],
    ['16:05', 'TR', 'Tim R.', 'created API token', 'ci-deploy', 'token', 'role operator · expires in 90 d', T.text],
    ['15:48', 'SK', 'Sara K.', 'scheduled maintenance', 'PostgreSQL 17 minor upgrade', 'maintenance', 'Sat 13 Sep 02:00–04:00 · public on status.acme.io', T.text],
    ['14:10', 'DG', 'Daniel G.', 'changed role of', 'Jana L.', 'member', 'operator → viewer', T.text],
    ['09:30', 'system', 'Pulsix', 'enrolled agent', 'mac-studio-dg', 'host', 'v0.4.2 · zone office-berlin', T.text],
  ]],
];
const objIcon = (k) => ({ job: ic.play, alert: ic.bell, member: ic.user, host: ic.server, rule: ic.activity, group: ic.layers, token: ic.key, maintenance: ic.clock }[k] || ic.more);
const feedHtml = feed.map(([day, items]) => `<div style="display: flex; flex-direction: column">
  <div style="padding: 10px 16px; font-size: 11px; font-weight: 600; color: ${T.muted}; text-transform: uppercase; letter-spacing: 0.08em; background: ${T.ink}; border-bottom: 1px solid ${T.border}">${day}</div>
  ${items.map(([t, ini, who, verb, obj, kind, detail, c]) => `<div style="display: grid; grid-template-columns: 48px 28px minmax(0, 1fr) 110px; align-items: center; gap: 12px; padding: 10px 16px; border-bottom: 1px solid ${T.border}">
    <span style="font-family: ${MONO}; font-size: 11px; color: ${T.muted}">${t}</span>
    ${ini === 'system' ? `<div style="width: 28px; height: 28px; border-radius: 50%; background: ${T.border}; display: flex; align-items: center; justify-content: center">${mark(16)}</div>` : avatar(ini, ini === 'CI' ? T.border : T.accentDim)}
    <div style="display: flex; flex-direction: column; gap: 2px; min-width: 0"><div style="font-size: 13px"><span style="font-weight: 600">${who}</span> <span style="color: ${T.dim}">${verb}</span> <span style="font-family: ${MONO}; font-size: 12px; color: ${c}">${obj}</span></div><span style="font-size: 11px; color: ${T.muted}; overflow: hidden; text-overflow: ellipsis; white-space: nowrap">${detail}</span></div>
    <div style="display: flex; align-items: center; gap: 6px; font-size: 11px; color: ${T.muted}; justify-content: flex-end">${objIcon(kind)}<span>${kind}</span></div>
  </div>`).join('')}
</div>`).join('');
fs.writeFileSync('Activity.dc.html', shell({
  active: 'Settings',
  crumbs: `<span>Acme Hosting</span>${ic.chevronR}<span style="color: ${T.text}; font-weight: 600">Activity</span>`,
  content: `
      <div style="display: flex; align-items: center; justify-content: space-between">
        <div style="display: flex; flex-direction: column; gap: 2px"><h1 style="margin: 0; font-size: 20px; font-weight: 600">Activity</h1><span style="font-size: 13px; color: ${T.muted}">Every change in this organization, by people, tokens and Pulsix itself</span></div>
        <div style="display: flex; gap: 8px">${btn('Export CSV')}</div>
      </div>
      <div style="display: flex; align-items: center; gap: 8px">
        <div style="display: flex; align-items: center; gap: 8px; width: 260px; height: 30px; padding: 0 10px; border-radius: 6px; background: ${T.surface}; border: 1px solid ${T.border}; color: ${T.muted}; font-size: 12px">${ic.search}<span>Search summaries…</span></div>
        <div style="width: 1px; height: 20px; background: ${T.border}; margin: 0 4px"></div>
        ${chip('Actor: anyone', false, ic.user)}${chip('Object: all')}${chip('host', true)}${chip('alert', true)}${chip('member')}${chip('job')}
        <div style="width: 1px; height: 20px; background: ${T.border}; margin: 0 4px"></div>
        ${chip('Last 7 days', true, ic.clock)}
        <span style="margin-left: auto; font-size: 12px; color: ${T.muted}">12 of 1 284 events</span>
      </div>
      <div style="display: grid; grid-template-columns: minmax(0, 1fr) 340px; gap: 16px; flex-grow: 1; min-height: 0">
        <div style="display: flex; flex-direction: column; background: ${T.surface}; border: 1px solid ${T.border}; border-radius: 10px; overflow: hidden">${feedHtml}<div style="display: flex; justify-content: center; padding: 12px; font-size: 12px; color: ${T.accentDim}">Load older</div></div>
        <div style="display: flex; flex-direction: column; gap: 16px">
          ${card('Event · 10:31 today', `<div style="display: flex; flex-direction: column; gap: 10px; padding: 14px 16px; font-size: 12px">
            <div style="display: flex; align-items: center; gap: 10px">${avatar('TR')}<div style="display: flex; flex-direction: column"><span style="font-weight: 600">Tim R.</span><span style="color: ${T.muted}">session · passkey · 84.16.x.x</span></div></div>
            <div style="display: flex; flex-direction: column; gap: 4px"><span style="color: ${T.muted}">Action</span><span style="font-family: ${MONO}">PATCH updateHostVariables</span></div>
            <div style="display: flex; flex-direction: column; gap: 4px"><span style="color: ${T.muted}">Object</span><span style="font-family: ${MONO}; color: ${T.accent}">host · web-03.fra</span></div>
            <div style="display: flex; flex-direction: column; gap: 4px"><span style="color: ${T.muted}">Change</span>
              <div style="padding: 8px 10px; border-radius: 6px; background: ${T.ink}; border: 1px solid ${T.border}; font-family: ${MONO}; font-size: 11px; line-height: 1.6"><div style="color: ${T.crit}">- vhosts[1].ssl: false</div><div style="color: ${T.ok}">+ vhosts[1].ssl: true</div></div>
            </div>
            <div style="display: flex; gap: 8px; padding-top: 4px">${btn('Open host')}${btn('Copy event id')}</div>
          </div>`, `${tag('evt_01j7…')}`)}
          ${card('Actors · 7 days', `<div style="display: flex; flex-direction: column; gap: 8px; padding: 12px 16px; font-size: 12px">
            ${[['TR', 'Tim R.', 412], ['SK', 'Sara K.', 188], ['system', 'Pulsix', 431], ['CI', 'ci-deploy', 96], ['DG', 'Daniel G.', 71]].map(([i, n, c]) => `<div style="display: flex; align-items: center; gap: 10px">${i === 'system' ? `<div style="width: 22px; height: 22px; border-radius: 50%; background: ${T.border}; display: flex; align-items: center; justify-content: center">${mark(13)}</div>` : `<div style="width: 22px; height: 22px; border-radius: 50%; background: ${i === 'CI' ? T.border : T.accentDim}; display: flex; align-items: center; justify-content: center; font-size: 9px; font-weight: 700; color: ${T.ink}">${i}</div>`}<span style="flex-grow: 1">${n}</span><span style="font-family: ${MONO}; color: ${T.muted}">${c}</span></div>`).join('')}
          </div>`, ``)}
        </div>
      </div>`,
  height: 1140,
}));

// ================= SETUP WIZARD =================
const wizStep = (n, l, sub, state) => `<div style="display: flex; align-items: flex-start; gap: 12px"><div style="width: 28px; height: 28px; border-radius: 50%; display: flex; align-items: center; justify-content: center; font-size: 12px; font-weight: 700; flex-shrink: 0; ${state === 'done' ? `background: ${T.ok}; color: ${T.ink}` : state === 'active' ? `background: ${T.accent}; color: ${T.ink}` : `border: 1px solid ${T.border}; color: ${T.muted}`}">${state === 'done' ? ic.check : n}</div><div style="display: flex; flex-direction: column; gap: 2px"><span style="font-size: 13px; font-weight: ${state === 'active' ? 600 : 500}; color: ${state === 'todo' ? T.muted : T.text}">${l}</span><span style="font-size: 11px; color: ${T.muted}">${sub}</span></div></div>`;
const setup = `<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <script src="./support.js"></script>
</head>
<body>
<x-dc>
<helmet>
  <link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@500;600;700;800&family=Inter:wght@400;500;600&display=swap">
  <style>
    body { margin: 0; font-family: Inter, system-ui, sans-serif; -webkit-font-smoothing: antialiased; }
    a { color: ${T.accent}; } a:hover { color: ${T.accentDim}; }
  </style>
</helmet>
<div style="width: 1440px; height: 900px; background: ${T.ink}; color: ${T.text}; display: flex; overflow: hidden; font-size: 13px">
  <div style="width: 420px; flex-shrink: 0; display: flex; flex-direction: column; gap: 28px; padding: 48px 40px; background: ${T.surface}; border-right: 1px solid ${T.border}">
    <div style="display: flex; align-items: center; gap: 12px">${mark(36)}<span style="font-family: ${MONO}; font-weight: 800; font-size: 26px; letter-spacing: -0.045em">pulsi<span style="color: ${T.accent}">x</span></span></div>
    <div style="display: flex; flex-direction: column; gap: 6px"><h1 style="margin: 0; font-size: 20px; font-weight: 600">Set up this installation</h1><span style="font-size: 13px; color: ${T.muted}; line-height: 1.5">Runs once. Afterwards this page is gone and everything happens inside the organization.</span></div>
    <div style="display: flex; flex-direction: column; gap: 18px">
      ${wizStep(1, 'Instance administrator', 'root@acme.io', 'done')}
      ${wizStep(2, 'First organization', 'name and URL slug', 'active')}
      ${wizStep(3, 'Outgoing mail', 'optional, can be set later', 'todo')}
      ${wizStep(4, 'Second factor', 'authenticator app or passkey', 'todo')}
    </div>
    <div style="margin-top: auto; display: flex; flex-direction: column; gap: 6px; font-size: 11px; color: ${T.muted}"><span>Instance pulsix.acme.io · v0.1.0</span><span>Database connected · schema up to date</span></div>
  </div>
  <div style="flex-grow: 1; display: flex; align-items: center; justify-content: center; padding: 48px">
    <div style="width: 560px; display: flex; flex-direction: column; gap: 22px">
      <div style="display: flex; flex-direction: column; gap: 6px"><span style="font-size: 11px; font-weight: 600; letter-spacing: 0.12em; color: ${T.accent}">STEP 2 OF 4</span><h2 style="margin: 0; font-size: 22px; font-weight: 600">Create the first organization</h2><span style="font-size: 13px; color: ${T.muted}; line-height: 1.5">An organization holds hosts, inventory, monitoring and members. You can create more later from the instance administration page.</span></div>
      <div style="display: flex; flex-direction: column; gap: 14px; padding: 20px; background: ${T.surface}; border: 1px solid ${T.border}; border-radius: 12px">
        ${input('Organization name', 'Acme Hosting', '100%', false)}
        <div style="display: flex; flex-direction: column; gap: 5px"><span style="font-size: 11px; color: ${T.muted}; font-weight: 500">URL slug</span><div style="display: flex; align-items: center; height: 32px; border-radius: 6px; background: ${T.ink}; border: 1px solid ${T.accent}; font-size: 12px; font-family: ${MONO}; overflow: hidden"><span style="padding: 0 10px; color: ${T.muted}; border-right: 1px solid ${T.border}; height: 100%; display: flex; align-items: center">pulsix.acme.io/o/</span><span style="padding: 0 10px">acme</span></div><span style="font-size: 11px; color: ${T.muted}">Lowercase letters, digits and dashes. Cannot be changed later.</span></div>
        <div style="display: flex; flex-direction: column; gap: 5px"><span style="font-size: 11px; color: ${T.muted}; font-weight: 500">Default zone</span>${box('default · built-in worker in this container')}<span style="font-size: 11px; color: ${T.muted}">Hosts start in this zone. Add zones and workers for other networks under Workers.</span></div>
        <div style="display: flex; flex-direction: column; gap: 5px"><span style="font-size: 11px; color: ${T.muted}; font-weight: 500">Timezone</span>${box('Europe/Berlin')}</div>
      </div>
      <div style="display: flex; align-items: center; justify-content: space-between">${btn('Back')}<div style="display: flex; align-items: center; gap: 12px"><span style="font-size: 12px; color: ${T.muted}">root@acme.io becomes owner</span>${btn('Continue', true)}</div></div>
    </div>
  </div>
</div>
</x-dc>
</body>
</html>
`;
fs.writeFileSync('SetupWizard.dc.html', setup);

// ================= ACCOUNT SECURITY =================
const qr = `<div style="width: 132px; height: 132px; display: grid; grid-template-columns: repeat(11, 1fr); gap: 2px; padding: 8px; background: #F6F4EF; border-radius: 8px; flex-shrink: 0">${Array.from({ length: 121 }, (_, i) => { const r = Math.floor(i / 11), c = i % 11; const corner = (r < 3 && c < 3) || (r < 3 && c > 7) || (r > 7 && c < 3); const on = corner ? !(r === 1 && c === 1) && !(r === 1 && c === 9) && !(r === 9 && c === 1) : ((i * 7 + r * 3) % 5 < 2); return `<div style="background: ${on ? T.ink : 'transparent'}; border-radius: 1px"></div>`; }).join('')}</div>`;
fs.writeFileSync('AccountSecurity.dc.html', shell({
  active: 'Settings',
  crumbs: `<span>Account</span>${ic.chevronR}<span style="color: ${T.text}; font-weight: 600">Security</span>`,
  content: `
      <div style="display: flex; align-items: flex-start; justify-content: space-between">
        <div style="display: flex; flex-direction: column; gap: 8px">
          <div style="display: flex; align-items: center; gap: 12px"><h1 style="margin: 0; font-size: 20px; font-weight: 600">Security</h1>${pill('second factor active', T.ok)}</div>
          <div style="display: flex; gap: 24px; border-bottom: 1px solid ${T.border}">${tab('Profile')}${tab('Security', true)}${tab('Sessions')}${tab('Activity')}</div>
        </div>
      </div>
      <div style="display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 16px; align-items: start">
        <div style="display: flex; flex-direction: column; gap: 16px">
          ${card('Passkeys', `<div style="display: flex; flex-direction: column">
            ${[['MacBook Pro · Touch ID', 'platform · synced', 'used 2 min ago'], ['YubiKey 5C', 'security key', 'used 3 d ago'], ['1Password', 'password manager · synced', 'added 2 w ago']].map(([n, k, u]) => `<div style="display: flex; align-items: center; gap: 12px; padding: 10px 16px; border-bottom: 1px solid ${T.border}">${ic.key}<div style="display: flex; flex-direction: column; flex-grow: 1"><span style="font-weight: 600">${n}</span><span style="font-size: 11px; color: ${T.muted}">${k}</span></div><span style="font-size: 11px; color: ${T.muted}">${u}</span><span style="font-size: 11px; color: ${T.crit}; margin-left: 8px">remove</span></div>`).join('')}
            <div style="display: flex; align-items: center; justify-content: space-between; padding: 12px 16px; font-size: 12px; color: ${T.muted}"><span>Passkeys sign you in without a password and count as your second factor.</span>${btn('Add passkey', true, ic.key)}</div>
          </div>`, `<span>3 registered</span>`)}
          ${card('Password', `<div style="display: flex; flex-direction: column; gap: 12px; padding: 16px">
            ${input('Current password', '••••••••••••••••')}${input('New password', '')}${input('Repeat new password', '')}
            <div style="display: flex; justify-content: space-between; align-items: center"><span style="font-size: 11px; color: ${T.muted}">At least 12 characters. Changing it logs out other sessions.</span>${btn('Change password')}</div>
          </div>`, ``)}
        </div>
        <div style="display: flex; flex-direction: column; gap: 16px">
          ${card('Authenticator app', `<div style="display: flex; flex-direction: column; gap: 14px; padding: 16px">
            <div style="display: flex; gap: 16px">${qr}<div style="display: flex; flex-direction: column; gap: 8px; font-size: 12px; color: ${T.dim}; line-height: 1.5"><span>Scan with any TOTP app (1Password, Bitwarden, Google Authenticator, Aegis), then enter the six-digit code to confirm.</span><span style="font-family: ${MONO}; font-size: 11px; color: ${T.muted}; word-break: break-all">JBSWY3DPEHPK3PXP JBSWY3DPEHPK3PXP</span></div></div>
            <div style="display: flex; align-items: flex-end; gap: 10px">${input('Code', '4 8 2 _ _ _', '160px')}${btn('Confirm', true)}</div>
          </div>`, `${pill('enrolling', T.warn)}`)}
          ${card('Recovery codes', `<div style="display: flex; flex-direction: column; gap: 10px; padding: 16px">
            <div style="display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 6px; padding: 12px; border-radius: 8px; background: ${T.ink}; border: 1px solid ${T.border}; font-family: ${MONO}; font-size: 12px">${['k7p2q-m4x9r-b3ntv', 'w8ha4-c2vzs-p9lmk', 'r3dfg-x7q2n-h5tyb', 'm9zxc-v4b2n-q8wer', 'p2lkj-h6gfd-s4aqw', 'z5xcv-b8nm3-k7jhg', 't4rfe-d9swq-a2zxc', 'y6uij-k3hgf-d8sae'].map(c => `<span>${c}</span>`).join('')}</div>
            <div style="display: flex; justify-content: space-between; align-items: center"><span style="font-size: 11px; color: ${T.muted}">Shown once. Each code works one time.</span><div style="display: flex; gap: 8px">${btn('Copy')}${btn('Download')}</div></div>
          </div>`, `<span>8 unused</span>`)}
          ${card('Sessions', `<div style="display: flex; flex-direction: column">
            ${[['This browser · macOS · Safari', 'passkey · 84.16.x.x', 'now', true], ['iPhone · Safari', 'password + TOTP · 84.16.x.x', '3 h ago', false], ['Linux · Firefox', 'password + TOTP · 10.0.x.x', '2 d ago', false]].map(([d, h, w, cur]) => `<div style="display: flex; align-items: center; gap: 12px; padding: 9px 16px; border-bottom: 1px solid ${T.border}; font-size: 12px">${dot(cur ? T.ok : T.muted, 6)}<div style="display: flex; flex-direction: column; flex-grow: 1"><span style="font-weight: 600">${d}</span><span style="font-size: 11px; color: ${T.muted}">${h}</span></div><span style="color: ${T.muted}">${w}</span>${cur ? '' : `<span style="color: ${T.crit}; margin-left: 8px">end</span>`}</div>`).join('')}
            <div style="display: flex; justify-content: flex-end; padding: 10px 16px">${btn('Log out everywhere')}</div>
          </div>`, ``)}
        </div>
      </div>`,
  height: 1140,
}));

// ================= PLAYBOOKS / REPOSITORIES =================
const repos = [
  ['acme/infra', 'git@github.com:acme/infra.git', 'main', 'a41c9e2', '3 min ago', 'ok', 14, 'deploy-key acme-infra'],
  ['acme/k3s-cluster', 'https://gitlab.acme.io/ops/k3s.git', 'release/1.31', '9f0b12d', '1 h ago', 'ok', 6, 'token gitlab-ops'],
  ['community/hardening', 'https://github.com/dev-sec/ansible-collection-hardening', 'master', 'c7e21aa', '2 d ago', 'ok', 3, 'public'],
  ['acme/legacy', 'git@bitbucket.org:acme/legacy-playbooks.git', 'master', '—', 'failed 12 min ago', 'crit', 0, 'deploy-key legacy'],
];
const repoRow = (r, sel) => `<div style="display: flex; flex-direction: column; gap: 6px; padding: 12px 14px; border-radius: 8px; border: 1px solid ${sel ? T.accent : T.border}; background: ${sel ? T.ink : 'transparent'}">
  <div style="display: flex; align-items: center; gap: 8px">${ic.git}<span style="font-family: ${MONO}; font-size: 12px; font-weight: 600; flex-grow: 1">${r[0]}</span>${dot(T[r[5]], 6)}<span style="font-size: 11px; color: ${T.muted}">${r[6]} playbooks</span></div>
  <div style="display: flex; align-items: center; gap: 8px; font-size: 11px; color: ${T.muted}; padding-left: 24px"><span style="font-family: ${MONO}">${r[2]} @ ${r[3]}</span><span>·</span><span style="color: ${r[5] === 'crit' ? T.crit : T.muted}">synced ${r[4]}</span></div>
</div>`;
const pbRows = [
  ['deploy-web.yml', 'playbooks/', 'Deploy web tier (nginx, php-fpm, release)', 3, '12 min ago · ok'],
  ['patch-all.yml', 'playbooks/', 'OS updates with reboot window', 1, '52 min ago · failed'],
  ['issue-certs.yml', 'playbooks/', 'Request or renew TLS certificates', 1, '2 d ago · ok'],
  ['rotate-logs.yml', 'playbooks/maintenance/', 'Logrotate + journald vacuum', 1, '2 h ago · ok'],
  ['add-monitor-user.yml', 'playbooks/db/', 'Create the monitoring role on database hosts', 1, '5 h ago · ok'],
  ['pg-failover.yml', 'playbooks/db/', 'Promote replica, repoint pgbouncer', 0, 'never'],
  ['bootstrap.yml', 'playbooks/', 'First-boot hardening + pulsix agent', 1, '1 d ago · ok'],
];
fs.writeFileSync('Playbooks.dc.html', shell({
  active: 'Playbooks',
  crumbs: `<span style="color: ${T.text}; font-weight: 600">Playbooks</span>`,
  content: `
      <div style="display: flex; align-items: flex-start; justify-content: space-between">
        <div style="display: flex; flex-direction: column; gap: 8px">
          <div style="display: flex; align-items: center; gap: 12px"><h1 style="margin: 0; font-size: 20px; font-weight: 600">Playbooks</h1><span style="font-size: 12px; color: ${T.muted}">4 repositories · 23 playbooks · 9 job templates · 2 editor playbooks</span></div>
          <div style="display: flex; gap: 24px; border-bottom: 1px solid ${T.border}">${tab('Repositories', true)}${tab('Job templates')}${tab('Editor')}${tab('Schedules')}</div>
        </div>
        <div style="display: flex; gap: 8px">${btn('Sync all', false, ic.git)}${btn('Add repository', true, ic.git)}</div>
      </div>
      <div style="display: grid; grid-template-columns: 360px minmax(0, 1fr); gap: 16px; flex-grow: 1; min-height: 0">
        <div style="display: flex; flex-direction: column; gap: 16px">
          ${card('Repositories', `<div style="display: flex; flex-direction: column; gap: 8px; padding: 12px">${repos.map((r, i) => repoRow(r, i === 0)).join('')}</div>`, `<span>per organization</span>`)}
          ${card('Sync error · acme/legacy', `<div style="display: flex; flex-direction: column; gap: 8px; padding: 12px 16px; font-size: 12px">
            <div style="padding: 8px 10px; border-radius: 6px; background: ${T.ink}; border: 1px solid ${T.crit}55; font-family: ${MONO}; font-size: 11px; color: ${T.crit}">Permission denied (publickey). Deploy key "legacy" rejected by bitbucket.org</div>
            <div style="display: flex; gap: 8px">${btn('Open credential', false, ic.key)}${btn('Retry sync')}</div>
          </div>`, `${pill('failed', T.crit)}`)}
        </div>
        <div style="display: flex; flex-direction: column; gap: 16px">
          ${card('acme/infra', `<div style="display: flex; flex-direction: column">
            <div style="display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px; padding: 14px 16px; border-bottom: 1px solid ${T.border}; font-size: 12px">
              <div style="display: flex; flex-direction: column; gap: 2px"><span style="color: ${T.muted}">Remote</span><span style="font-family: ${MONO}; font-size: 11px">git@github.com:acme/infra.git</span></div>
              <div style="display: flex; flex-direction: column; gap: 2px"><span style="color: ${T.muted}">Branch · subpath</span><span style="font-family: ${MONO}; font-size: 11px">main · /</span></div>
              <div style="display: flex; flex-direction: column; gap: 2px"><span style="color: ${T.muted}">Credential</span><span style="font-family: ${MONO}; font-size: 11px">deploy-key acme-infra</span></div>
              <div style="display: flex; flex-direction: column; gap: 2px"><span style="color: ${T.muted}">requirements.yml</span><span style="font-family: ${MONO}; font-size: 11px">5 collections · 2 roles · installed</span></div>
            </div>
            <div style="display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr) 90px 150px 110px; gap: 12px; padding: 8px 16px; border-bottom: 1px solid ${T.border}; font-size: 10px; color: ${T.muted}; text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600"><span>Playbook</span><span>Description</span><span>Templates</span><span>Last run</span><span></span></div>
            ${pbRows.map(([f, dir, d, n, last]) => `<div style="display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr) 90px 150px 110px; align-items: center; gap: 12px; padding: 10px 16px; border-bottom: 1px solid ${T.border}">
              <div style="display: flex; flex-direction: column; min-width: 0"><span style="font-family: ${MONO}; font-size: 12px; font-weight: 600">${f}</span><span style="font-size: 11px; color: ${T.muted}; font-family: ${MONO}">${dir}</span></div>
              <span style="font-size: 12px; color: ${T.dim}; overflow: hidden; text-overflow: ellipsis; white-space: nowrap">${d}</span>
              <span style="font-size: 12px; color: ${n ? T.text : T.muted}">${n ? n + ' template' + (n > 1 ? 's' : '') : '—'}</span>
              <span style="font-size: 11px; color: ${last.includes('failed') ? T.crit : T.muted}; font-family: ${MONO}">${last}</span>
              <div style="display: flex; justify-content: flex-end; gap: 6px">${btn('Run')}${btn('Template')}</div>
            </div>`).join('')}
            <div style="display: flex; align-items: center; gap: 12px; padding: 10px 16px; font-size: 11px; color: ${T.muted}"><span>Playbooks are discovered from the repository (any <span style="font-family: ${MONO}">*.yml</span> with a play). Description comes from the first comment line.</span><span style="margin-left: auto">webhook: <span style="font-family: ${MONO}">/api/v1/hooks/git/9c1f…</span> · auto-sync on push</span></div>
          </div>`, `${pill('synced', T.ok)}${tag('a41c9e2')}${btn('Sync now')}`)}
        </div>
      </div>`,
  height: 1100,
}));

// ================= SCHEDULER =================
const scheds = [
  ['ok', 'Nightly OS patching', 'patch-all.yml', 'zone:hel', '0 3 * * 2,4', 'Tue, Thu 03:00', 'in 14 h', 'last: failed 1 host', T.crit, 'on failure → slack #ops, pagerduty (crit)'],
  ['ok', 'Log rotation', 'rotate-logs.yml', 'all', '0 */6 * * *', 'every 6 h', 'in 2 h 12', 'last: ok · 1 m 03 s', T.ok, 'on failure → email ops@'],
  ['ok', 'TLS renewal', 'issue-certs.yml', 'group:webserver', '0 4 1 * *', 'monthly, 1st 04:00', 'in 20 d', 'last: ok · 0 changed', T.ok, 'on change → slack #ops'],
  ['paused', 'DB monitor user', 'add-monitor-user.yml', 'group:postgres', '@weekly', 'Sun 00:00', 'paused', 'last: ok · 5 d ago', T.muted, 'always → email dba@'],
  ['ok', 'Agent bootstrap sweep', 'bootstrap.yml', 'tag:bootstrap=pending', '*/30 * * * *', 'every 30 min', 'in 7 min', 'last: ok · 0 hosts', T.ok, 'on change → activity only'],
];
const hours = ['00', '03', '06', '09', '12', '15', '18', '21'];
const days = ['Mon 8', 'Tue 9', 'Wed 10', 'Thu 11', 'Fri 12', 'Sat 13', 'Sun 14'];
const cal = `<div style="display: grid; grid-template-columns: 44px repeat(7, minmax(0, 1fr)); gap: 4px; padding: 12px 16px; font-size: 10px; color: ${T.muted}">
  <span></span>${days.map((d, i) => `<span style="text-align: center; font-weight: 600; color: ${i === 2 ? T.accent : T.muted}">${d}</span>`).join('')}
  ${hours.map((h, hi) => `<span style="font-family: ${MONO}">${h}:00</span>` + days.map((_, di) => {
    const ev = [];
    if (hi === 1 && (di === 1 || di === 3)) ev.push(['Nightly OS patching', T.warn]);
    if (hi % 2 === 0) ev.push(['Log rotation', T.dim]);
    if (hi === 1 && di === 5) ev.push(['Maintenance · PostgreSQL', '#A78BFA']);
    if (di === 2 && hi === 4) ev.push(['deploy-web (manual)', T.accent]);
    return `<div style="min-height: 26px; border-radius: 4px; background: ${T.ink}; border: 1px solid ${T.border}; display: flex; flex-direction: column; gap: 2px; padding: 2px">${ev.map(([l, c]) => `<div style="font-size: 9px; padding: 1px 4px; border-radius: 3px; background: ${c}22; color: ${c}; overflow: hidden; text-overflow: ellipsis; white-space: nowrap">${l}</div>`).join('')}</div>`;
  }).join('')).join('')}
</div>`;
fs.writeFileSync('Scheduler.dc.html', shell({
  active: 'Playbooks',
  crumbs: `<span>Playbooks</span>${ic.chevronR}<span style="color: ${T.text}; font-weight: 600">Schedules</span>`,
  content: `
      <div style="display: flex; align-items: flex-start; justify-content: space-between">
        <div style="display: flex; flex-direction: column; gap: 8px">
          <div style="display: flex; align-items: center; gap: 12px"><h1 style="margin: 0; font-size: 20px; font-weight: 600">Schedules</h1><span style="font-size: 12px; color: ${T.muted}">5 schedules · 4 active · next run in 7 min · timezone Europe/Berlin</span></div>
          <div style="display: flex; gap: 24px; border-bottom: 1px solid ${T.border}">${tab('Repositories')}${tab('Job templates')}${tab('Editor')}${tab('Schedules', true)}</div>
        </div>
        <div style="display: flex; gap: 8px">${btn('Pause all')}${btn('New schedule', true, ic.clock)}</div>
      </div>
      <div style="display: grid; grid-template-columns: minmax(0, 1fr) 420px; gap: 16px; flex-grow: 1; min-height: 0">
        <div style="display: flex; flex-direction: column; gap: 16px">
          <div style="display: flex; flex-direction: column; background: ${T.surface}; border: 1px solid ${T.border}; border-radius: 10px; overflow: hidden">
            <div style="display: grid; grid-template-columns: 20px minmax(0, 1fr) 118px 78px 118px 150px 24px; gap: 12px; padding: 10px 16px; border-bottom: 1px solid ${T.border}; font-size: 10px; color: ${T.muted}; text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600"><span></span><span>Schedule</span><span>Cron</span><span>Next</span><span>Last</span><span>Notify</span><span></span></div>
            ${scheds.map(([s, n, pb, lim, cron, human, next, last, lc, notify]) => `<div style="display: grid; grid-template-columns: 20px minmax(0, 1fr) 118px 78px 118px 150px 24px; align-items: center; gap: 12px; padding: 10px 16px; border-bottom: 1px solid ${T.border}; ${s === 'paused' ? 'opacity: 0.55' : ''}">
              ${dot(s === 'paused' ? T.muted : lc)}<div style="display: flex; flex-direction: column; min-width: 0"><span style="font-weight: 600; white-space: nowrap; overflow: hidden; text-overflow: ellipsis">${n}</span><span style="font-size: 11px; color: ${T.muted}; font-family: ${MONO}; white-space: nowrap; overflow: hidden; text-overflow: ellipsis">${pb} · ${lim}</span></div>
              <div style="display: flex; flex-direction: column; min-width: 0"><span style="font-family: ${MONO}; font-size: 11px; white-space: nowrap">${cron}</span><span style="font-size: 11px; color: ${T.muted}; white-space: nowrap; overflow: hidden; text-overflow: ellipsis">${human}</span></div>
              <span style="font-size: 11px; color: ${s === 'paused' ? T.muted : T.accent}; white-space: nowrap">${next}</span><span style="font-size: 11px; color: ${lc}; overflow: hidden; text-overflow: ellipsis; white-space: nowrap">${last}</span>
              <span style="font-size: 11px; color: ${T.muted}; overflow: hidden; text-overflow: ellipsis; white-space: nowrap">${notify}</span><span style="color: ${T.muted}">${ic.more}</span>
            </div>`).join('')}
          </div>
          ${card('Week · 8–14 Sep', cal, `${chip('Week', true)}${chip('Month')}${chip('List')}<span style="display: flex; align-items: center; gap: 6px">${dot('#A78BFA', 6)} maintenance</span>`)}
        </div>
        <div style="display: flex; flex-direction: column; gap: 16px">
          ${card('Schedule · Nightly OS patching', `<div style="display: flex; flex-direction: column; gap: 14px; padding: 16px">
            <div style="display: grid; grid-template-columns: minmax(0, 1fr) 120px; gap: 10px">${select('Job template', 'patch-all · zone:hel')}${input('Timezone', 'Europe/Berlin', '100%', false)}</div>
            <div style="display: flex; flex-direction: column; gap: 6px"><span style="font-size: 11px; color: ${T.muted}; font-weight: 500">When</span>${seg(['Simple', 'Cron'], 'Cron')}<div style="display: flex; align-items: center; gap: 10px">${box('0 3 * * 2,4', '150px')}<span style="font-size: 12px; color: ${T.dim}">Tuesday and Thursday at 03:00 · next 5: 11 Sep, 16 Sep, 18 Sep, 23 Sep, 25 Sep</span></div></div>
            <div style="display: flex; flex-direction: column; gap: 6px"><span style="font-size: 11px; color: ${T.muted}; font-weight: 500">Guards</span>${toggle('Skip when a run of this template is still active', true)}${toggle('Skip during maintenance windows on the targets', true)}${toggle('Catch up a missed run after downtime', false, 'otherwise the next regular run happens')}</div>
            <div style="display: flex; flex-direction: column; gap: 8px"><span style="font-size: 11px; color: ${T.muted}; font-weight: 500">Notifications</span>
              <div style="display: flex; flex-direction: column; gap: 6px; padding: 10px; border-radius: 8px; background: ${T.ink}; border: 1px solid ${T.border}; font-size: 12px">
                <div style="display: flex; align-items: center; gap: 8px">${pill('on failure', T.crit)}<span style="color: ${T.muted}">→</span>${tag('slack #ops')}${tag('pagerduty db-oncall')}</div>
                <div style="display: flex; align-items: center; gap: 8px">${pill('on change', T.warn)}<span style="color: ${T.muted}">→</span>${tag('slack #ops')}</div>
                <div style="display: flex; align-items: center; gap: 8px">${pill('on success', T.ok)}<span style="color: ${T.muted}">→</span><span style="font-size: 11px; color: ${T.muted}">activity feed only</span></div>
                <div style="display: flex; align-items: center; gap: 8px">${pill('skipped / missed', T.muted)}<span style="color: ${T.muted}">→</span>${tag('email ops@acme.io')}</div>
                <span style="font-size: 11px; color: ${T.accentDim}">+ add rule</span>
              </div>
              <span style="font-size: 11px; color: ${T.muted}">Every run is recorded in the activity feed with its result and a link to the log. Weekly digest of all scheduled runs → email ops@acme.io, Mondays 08:00.</span>
            </div>
            <div style="display: flex; justify-content: space-between; align-items: center">${btn('Run now')}<div style="display: flex; gap: 8px">${btn('Pause')}${btn('Save', true, ic.check)}</div></div>
          </div>`, `${pill('active', T.ok)}`)}
          ${card('Triggers · patch-all', `<div style="display: flex; flex-direction: column; gap: 10px; padding: 14px 16px; font-size: 12px">
            <div style="display: flex; align-items: center; gap: 8px">${ic.user}<span style="flex-grow: 1">Manual · Run button, API <span style="font-family: ${MONO}">POST /jobs</span></span>${toggle('', true)}</div>
            <div style="display: flex; align-items: center; gap: 8px">${ic.clock}<span style="flex-grow: 1">Schedule · Nightly OS patching</span>${toggle('', true)}</div>
            <div style="display: flex; flex-direction: column; gap: 8px; padding: 10px; border-radius: 8px; background: ${T.ink}; border: 1px solid ${T.accent}55">
              <div style="display: flex; align-items: center; gap: 8px">${ic.code}<span style="font-weight: 600; flex-grow: 1">Incoming webhook</span>${toggle('', true)}</div>
              <div style="display: flex; align-items: center; gap: 8px; font-family: ${MONO}; font-size: 11px; padding: 6px 8px; border-radius: 6px; background: ${T.surface}; border: 1px solid ${T.border}"><span style="color: ${T.muted}">POST</span><span style="flex-grow: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap">https://pulsix.acme.io/api/v1/hooks/jobs/7d2f…c91a</span><span style="color: ${T.accentDim}; font-family: Inter, sans-serif">copy</span></div>
              <div style="display: flex; flex-direction: column; gap: 4px; color: ${T.muted}">
                <span>Auth: <span style="font-family: ${MONO}; color: ${T.text}">X-Pulsix-Signature</span> (HMAC-SHA256 with the hook secret) or <span style="font-family: ${MONO}; color: ${T.text}">?token=</span></span>
                <span>Allowed sources: <span style="font-family: ${MONO}; color: ${T.text}">140.82.112.0/20, 10.0.0.0/8</span></span>
                <span>Payload → extra vars: <span style="font-family: ${MONO}; color: ${T.text}">$.ref → release</span>, <span style="font-family: ${MONO}; color: ${T.text}">$.hosts[*] → limit</span></span>
                <span>Rate limit 1 run / 5 min · queue if a run is active · last call 12 min ago from GitHub Actions</span>
              </div>
              <div style="display: flex; gap: 8px">${btn('Rotate secret')}${btn('Test call')}</div>
            </div>
          </div>`, `<span>per job template</span>`)}
          ${card('Recent runs of this schedule', `<div style="display: flex; flex-direction: column">
            ${[['crit', '#1428', 'Tue 9 Sep 03:00', '4 m 12 s', '1 failed · k3s-02.hel'], ['ok', '#1401', 'Thu 4 Sep 03:00', '3 m 40 s', '12 changed'], ['ok', '#1377', 'Tue 2 Sep 03:00', '3 m 51 s', '9 changed'], ['unknown', '—', 'Thu 28 Aug 03:00', '—', 'skipped · maintenance window']].map(([s, id, t, d, r]) => `<div style="display: flex; align-items: center; gap: 10px; padding: 9px 16px; border-bottom: 1px solid ${T.border}; font-size: 12px">${dot(T[s], 6)}<span style="font-family: ${MONO}; color: ${T.accent}; width: 44px">${id}</span><span style="flex-grow: 1; color: ${T.dim}">${t}</span><span style="font-family: ${MONO}; color: ${T.muted}">${d}</span><span style="color: ${s === 'crit' ? T.crit : T.muted}; width: 150px; text-align: right">${r}</span></div>`).join('')}
          </div>`, `${ic.chevronR}`)}
        </div>
      </div>`,
  height: 1460,
}));

// ================= CREDENTIALS & SECRETS =================
const credRows = [
  ['key', 'acme-deploy', 'SSH key · ed25519', 'ansible@ · 41 hosts · 3 templates', 'TR · 3 mo ago', 'ok'],
  ['key', 'hel-bootstrap', 'SSH key · ed25519', 'root@ · 8 hosts', 'DG · 2 mo ago', 'ok'],
  ['key', 'homelab-key', 'SSH key · rsa-4096', 'pi@ · 1 host', 'TR · 1 y ago', 'warn'],
  ['password', 'office-fs-admin', 'SSH password', 'Administrator@ · 1 host (win)', 'SK · 5 d ago', 'ok'],
  ['git', 'acme-infra', 'Git deploy key', 'repo acme/infra', 'TR · 3 mo ago', 'ok'],
  ['git', 'gitlab-ops', 'Git token', 'repo acme/k3s-cluster · expires in 23 d', 'DG · 11 mo ago', 'warn'],
  ['git', 'legacy', 'Git deploy key', 'repo acme/legacy · rejected by remote', 'TR · 2 y ago', 'crit'],
  ['channel', 'slack #ops', 'Slack bot token', 'channel · 6 routes', 'SK · 4 mo ago', 'ok'],
  ['channel', 'pagerduty db-oncall', 'PagerDuty integration key', 'channel · 2 routes', 'SK · 4 mo ago', 'ok'],
  ['channel', 'telegram Tim', 'Telegram bot + chat id', 'channel · 1 route', 'TR · 1 mo ago', 'ok'],
];
const credIcon = (k) => ({ key: ic.key, password: ic.key, git: ic.git, channel: ic.bell }[k]);
fs.writeFileSync('Credentials.dc.html', shell({
  active: 'Settings',
  crumbs: `<span>Settings</span>${ic.chevronR}<span style="color: ${T.text}; font-weight: 600">Credentials &amp; secrets</span>`,
  content: `
      <div style="display: flex; align-items: flex-start; justify-content: space-between">
        <div style="display: flex; flex-direction: column; gap: 8px">
          <div style="display: flex; align-items: center; gap: 12px"><h1 style="margin: 0; font-size: 20px; font-weight: 600">Credentials &amp; secrets</h1><span style="font-size: 12px; color: ${T.muted}">10 credentials · 2 jump hosts · encrypted with the instance master key · values are never shown again</span></div>
          <div style="display: flex; gap: 24px; border-bottom: 1px solid ${T.border}">${tab('Credentials', true)}${tab('Jump hosts')}${tab('API tokens')}${tab('Agent secrets (names)')}</div>
        </div>
        <div style="display: flex; gap: 8px">${btn('Rotate…')}${btn('Add credential', true, ic.key)}</div>
      </div>
      <div style="display: flex; align-items: center; gap: 8px">
        <div style="display: flex; align-items: center; gap: 8px; width: 240px; height: 30px; padding: 0 10px; border-radius: 6px; background: ${T.surface}; border: 1px solid ${T.border}; color: ${T.muted}; font-size: 12px">${ic.search}<span>Filter…</span></div>
        <div style="width: 1px; height: 20px; background: ${T.border}; margin: 0 4px"></div>
        ${chip('SSH', true, ic.key)}${chip('Git', true, ic.git)}${chip('Channels', true, ic.bell)}${chip('Expiring')}${chip('Unused')}
      </div>
      <div style="display: grid; grid-template-columns: minmax(0, 1fr) 400px; gap: 16px; flex-grow: 1; min-height: 0">
        <div style="display: flex; flex-direction: column; background: ${T.surface}; border: 1px solid ${T.border}; border-radius: 10px; overflow: hidden">
          <div style="display: grid; grid-template-columns: 28px 170px 170px minmax(0, 1fr) 130px 32px; gap: 12px; padding: 10px 16px; border-bottom: 1px solid ${T.border}; font-size: 10px; color: ${T.muted}; text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600"><span></span><span>Name</span><span>Kind</span><span>Used by</span><span>Created</span><span></span></div>
          ${credRows.map(([k, n, kind, used, by, st]) => `<div style="display: grid; grid-template-columns: 28px 170px 170px minmax(0, 1fr) 130px 32px; align-items: center; gap: 12px; padding: 10px 16px; border-bottom: 1px solid ${T.border}">
            <span style="display: inline-flex; align-items: center; justify-content: center; width: 24px; height: 24px; border-radius: 6px; background: ${T.ink}; color: ${T[st] === T.ok ? T.dim : T[st]}">${credIcon(k)}</span>
            <span style="font-family: ${MONO}; font-size: 12px; font-weight: 600">${n}</span><span style="font-size: 12px; color: ${T.dim}">${kind}</span>
            <span style="font-size: 12px; color: ${st === 'ok' ? T.dim : T[st]}; overflow: hidden; text-overflow: ellipsis; white-space: nowrap">${used}</span><span style="font-size: 11px; color: ${T.muted}">${by}</span><span style="color: ${T.muted}">${ic.more}</span>
          </div>`).join('')}
        </div>
        <div style="display: flex; flex-direction: column; gap: 16px">
          ${card('Jump hosts', `<div style="display: flex; flex-direction: column">
            ${[['bastion.fra', 'bastion.acme.io:22 · jump@ · key acme-deploy', 'default for zone fra · 41 hosts'], ['bastion.hel', '10.20.0.2:2222 · ops@ · key hel-bootstrap', 'default for zone hel · via bastion.fra']].map(([n, c, u]) => `<div style="display: flex; flex-direction: column; gap: 3px; padding: 10px 16px; border-bottom: 1px solid ${T.border}"><div style="display: flex; align-items: center; gap: 8px">${ic.server}<span style="font-family: ${MONO}; font-size: 12px; font-weight: 600; flex-grow: 1">${n}</span>${dot(T.ok, 6)}</div><span style="font-size: 11px; color: ${T.dim}; font-family: ${MONO}; padding-left: 24px">${c}</span><span style="font-size: 11px; color: ${T.muted}; padding-left: 24px">${u}</span></div>`).join('')}
            <div style="padding: 10px 16px; font-size: 11px; color: ${T.muted}">Resolution: host → inventory group → zone default. Chains are rendered as ProxyJump.</div>
          </div>`, `${btn('Add jump host')}`)}
          ${card('Rotate · acme-deploy', `<div style="display: flex; flex-direction: column; gap: 12px; padding: 16px; font-size: 12px">
            <div style="display: flex; flex-direction: column; gap: 6px; color: ${T.dim}; line-height: 1.5"><span>Generates a new ed25519 key, keeps the old one until the rollout succeeds, then revokes it.</span></div>
            <div style="display: flex; flex-direction: column; gap: 6px">${toggle('Roll out the new public key with the pulsix.ssh-key role', true, 'job template rotate-ssh-key · 41 hosts')}${toggle('Notify on completion', true, 'slack #ops')}</div>
            <div style="display: flex; justify-content: flex-end; gap: 8px">${btn('Cancel')}${btn('Start rotation', true)}</div>
          </div>`, `${tag('41 hosts')}`)}
          ${card('Agent secrets reported by hosts', `<div style="display: flex; flex-direction: column; gap: 6px; padding: 12px 16px; font-size: 12px">
            ${[['pg_main', '7 hosts', T.ok], ['mysql_shop', '3 hosts', T.ok], ['nginx_status', '12 hosts · 1 check references a missing secret on web-05.fra', T.warn]].map(([n, u, c]) => `<div style="display: flex; align-items: center; gap: 8px">${dot(c, 6)}<span style="font-family: ${MONO}">${n}</span><span style="color: ${c === T.warn ? T.warn : T.muted}; margin-left: auto; text-align: right">${u}</span></div>`).join('')}
            <span style="color: ${T.muted}; margin-top: 4px">Names only. Values live in each host's agent.yaml.</span>
          </div>`, ``)}
        </div>
      </div>`,
  height: 1100,
}));

// ================= ENROLLMENT · SSH STEP =================
const PUBKEY = 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGx3v9Qm2kQ0f4zJb8Vt1uYcR7pXhN5sWa2eKq9LdT0c pulsix:acme:acme-deploy';
const testRow = (state, label, detail) => `<div style="display: flex; align-items: center; gap: 10px; padding: 7px 0; font-size: 12px">${state === 'ok' ? `<span style="color: ${T.ok}">${ic.check}</span>` : state === 'wait' ? `<span style="display: inline-block; width: 10px; height: 10px; border-radius: 50%; border: 2px solid ${T.accent}; border-top-color: transparent; margin: 0 2px"></span>` : state === 'ask' ? `<span style="color: ${T.warn}">${ic.key}</span>` : `<span style="width: 14px; height: 14px; border-radius: 50%; border: 1px solid ${T.border}"></span>`}<span style="font-weight: 500; color: ${state === 'todo' ? T.muted : T.text}">${label}</span><span style="color: ${T.muted}; margin-left: auto; font-family: ${MONO}; font-size: 11px; text-align: right">${detail}</span></div>`;
fs.writeFileSync('EnrollmentSSH.dc.html', shell({
  active: 'Hosts',
  crumbs: `<span>Hosts</span>${ic.chevronR}<span style="color: ${T.text}; font-weight: 600">Add host</span>`,
  content: `
      <div style="display: flex; align-items: center; justify-content: space-between">
        <div style="display: flex; flex-direction: column; gap: 2px"><h1 style="margin: 0; font-size: 20px; font-weight: 600">Add host · mail-02.hel</h1><span style="font-size: 13px; color: ${T.muted}">Managed via SSH: Pulsix needs a login on the host. Nothing is stored on the host except a public key.</span></div>
        <div style="display: flex; align-items: center; gap: 28px">${step(1, 'Basics', 'done')}<span style="width: 40px; height: 1px; background: ${T.border}"></span>${step(2, 'Install agent', 'done')}<span style="width: 40px; height: 1px; background: ${T.border}"></span>${step(3, 'SSH access', 'active')}<span style="width: 40px; height: 1px; background: ${T.border}"></span>${step(4, 'Done', 'todo')}</div>
      </div>
      <div style="display: grid; grid-template-columns: minmax(0, 1fr) 420px; gap: 16px; flex-grow: 1; min-height: 0">
        <div style="display: flex; flex-direction: column; gap: 16px">
          ${card('Connection', `<div style="display: flex; flex-direction: column; gap: 14px; padding: 16px">
            <div style="display: grid; grid-template-columns: 140px 90px minmax(0, 1fr) minmax(0, 1fr); gap: 12px">${input('SSH user', 'ansible')}${input('Port', '22')}${select('Credential', 'acme-deploy · ed25519')}${select('Jump host', 'zone default · bastion.hel')}</div>
            <div style="display: flex; gap: 8px; flex-wrap: wrap; font-size: 12px">${chip('Use existing credential', true, ic.key)}${chip('Generate new key for this host')}${chip('Import private key')}${chip('Password')}</div>
            ${toggle('Privilege escalation (become) with sudo', true, 'passwordless sudo expected; a become password can be set on the credential')}
          </div>`, `${tag('managed')}`)}
          ${card('Authorize the public key on the host', `<div style="display: flex; flex-direction: column; gap: 14px; padding: 16px">
            <div style="font-size: 12px; color: ${T.dim}">Run one of these on <span style="font-family: ${MONO}; color: ${T.text}">mail-02.hel</span> as the target user. The private key never leaves Pulsix.</div>
            <div style="display: flex; gap: 8px">${chip('authorized_keys', true)}${chip('cloud-init')}${chip('Terraform (hcloud)')}${chip('Ansible bootstrap')}</div>
            ${codeBlock([`mkdir -p ~/.ssh && chmod 700 ~/.ssh`, `echo '<span style="color: ${T.accent}">${PUBKEY}</span>' >> ~/.ssh/authorized_keys`, `chmod 600 ~/.ssh/authorized_keys`])}
            <div style="display: flex; flex-direction: column; gap: 4px; font-size: 11px; color: ${T.muted}"><span>Fingerprint <span style="font-family: ${MONO}; color: ${T.text}">SHA256:Q2m8…7VfA</span> · comment identifies the organization and credential so admins can find stray keys later.</span></div>
          </div>`, `${btn('Copy public key', false, ic.key)}`)}
          ${card('Or let Pulsix install the key', `<div style="display: flex; flex-direction: column; gap: 12px; padding: 16px; font-size: 12px">
            <div style="color: ${T.dim}; line-height: 1.5">If you already have a working login (for example the root password from your hosting provider), Pulsix connects once with it, installs the public key above, creates the <span style="font-family: ${MONO}; color: ${T.text}">ansible</span> user with sudo, and then forgets the bootstrap credential.</div>
            <div style="display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr) 150px; gap: 12px; align-items: end">${input('Bootstrap user', 'root')}${input('Bootstrap password (used once)', '••••••••••••')}${btn('Run bootstrap', true, ic.play)}</div>
            <span style="font-size: 11px; color: ${T.muted}">Runs the built-in <span style="font-family: ${MONO}">pulsix.bootstrap</span> playbook from worker hel-1 · logged like any job, without the password.</span>
          </div>`, `<span>optional</span>`)}
        </div>
        <div style="display: flex; flex-direction: column; gap: 16px">
          ${card('Test connection', `<div style="display: flex; flex-direction: column; padding: 8px 16px">
            ${testRow('ok', 'Worker hel-1 reaches mail-02.hel:22', 'via bastion.hel · 24 ms')}
            ${testRow('ask', 'Host key fingerprint', 'SHA256:9k3P…tZ1Q')}
            <div style="display: flex; flex-direction: column; gap: 8px; margin: 4px 0 8px 24px; padding: 10px; border-radius: 6px; background: ${T.ink}; border: 1px solid ${T.warn}55; font-size: 11px"><span style="color: ${T.dim}">First contact. Compare with <span style="font-family: ${MONO}; color: ${T.text}">ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub</span> on the host, then trust it. Pulsix pins the key; a later change raises a critical alert.</span><div style="display: flex; gap: 8px">${btn('Trust and continue', true)}${btn('Reject')}</div></div>
            ${testRow('todo', 'Login as ansible with acme-deploy', '')}
            ${testRow('todo', 'sudo without password', '')}
            ${testRow('todo', 'Python for Ansible', '')}
            ${testRow('todo', 'Gather facts', '')}
          </div>`, `<span style="display: flex; align-items: center; gap: 6px">${dot(T.warn, 6)} waiting for you</span>`)}
          ${card('Last successful test', `<div style="display: flex; flex-direction: column; gap: 4px; padding: 12px 16px; font-size: 12px; color: ${T.muted}"><span>Never. After a successful test the host shows <span style="color: ${T.ok}">SSH ok</span> in the list; Pulsix re-tests daily and after credential rotation.</span></div>`, ``)}
          ${card('What the ansible user needs', `<div style="display: flex; flex-direction: column; gap: 6px; padding: 12px 16px; font-size: 12px; color: ${T.dim}; line-height: 1.5">
            <span>A login shell, the public key in <span style="font-family: ${MONO}; color: ${T.text}">authorized_keys</span>, passwordless sudo (or a become password on the credential), and Python 3.</span>
            <span>Windows hosts use OpenSSH Server or WinRM; pick the connection type on the credential.</span>
          </div>`, ``)}
          <div style="display: flex; justify-content: space-between; align-items: center">${btn('Back')}<div style="display: flex; gap: 8px">${btn('Skip for now')}${btn('Continue', true)}</div></div>
        </div>
      </div>`,
  height: 1100,
}));

// ================= WORKERS & ZONES =================
const capBadge = (l, on = true) => `<span style="font-size: 10px; font-weight: 600; padding: 2px 6px; border-radius: 4px; color: ${on ? T.accent : T.muted}; background: ${on ? T.accent + '14' : T.ink}; border: 1px solid ${on ? T.accent + '33' : T.border}">${l}</span>`;
const workerRow = (name, st, ver, caps, agents, jobs, checks, load, seen) => `<div style="display: grid; grid-template-columns: 20px 100px 56px 160px 56px 76px 68px minmax(0, 1fr) 70px; align-items: center; gap: 10px; padding: 9px 16px; border-bottom: 1px solid ${T.border}; font-size: 12px">
  ${dot(T[st])}<span style="font-family: ${MONO}; font-weight: 600">${name}</span><span style="font-family: ${MONO}; font-size: 11px; color: ${T.muted}">${ver}</span><div style="display: flex; gap: 4px">${caps.map(([c, on]) => capBadge(c, on)).join('')}</div>
  <span style="font-family: ${MONO}; font-size: 11px">${agents}</span><span style="font-family: ${MONO}; font-size: 11px">${jobs}</span><span style="font-family: ${MONO}; font-size: 11px">${checks}</span>
  <div style="display: flex; align-items: center; gap: 8px"><div style="flex-grow: 1; height: 6px; border-radius: 999px; background: ${T.border}; overflow: hidden"><div style="width: ${load}%; height: 100%; background: ${load > 80 ? T.warn : T.accentDim}"></div></div><span style="font-family: ${MONO}; font-size: 11px; color: ${T.muted}; width: 32px">${load} %</span></div>
  <span style="font-size: 11px; color: ${st === 'ok' ? T.muted : T[st]}; text-align: right">${seen}</span>
</div>`;
const zoneCard = (id, name, bastion, workers, agentsC, agentsT, hostsM, remote) => `<div style="display: flex; flex-direction: column; background: ${T.surface}; border: 1px solid ${T.border}; border-radius: 10px; overflow: hidden">
  <div style="display: flex; align-items: center; gap: 12px; padding: 12px 16px; border-bottom: 1px solid ${T.border}">
    ${ic.globe}<div style="display: flex; flex-direction: column; gap: 3px; min-width: 0"><div style="display: flex; align-items: baseline; gap: 10px"><span style="font-family: ${MONO}; font-size: 13px; font-weight: 700">${id}</span><span style="font-size: 11px; color: ${T.muted}">${name}</span></div>
    <div style="display: flex; gap: 14px; font-size: 11px; color: ${T.muted}; white-space: nowrap"><span><span style="color: ${T.text}; font-family: ${MONO}">${agentsC}/${agentsT}</span> agents</span><span><span style="color: ${T.text}; font-family: ${MONO}">${hostsM}</span> managed</span><span><span style="color: ${T.text}; font-family: ${MONO}">${remote}</span> remote checks</span><span>jump host <span style="color: ${T.text}; font-family: ${MONO}">${bastion}</span></span></div></div>
    <div style="margin-left: auto; display: flex; gap: 6px; flex-shrink: 0">${btn('Add worker')}<span style="color: ${T.muted}; display: flex; align-items: center">${ic.more}</span></div>
  </div>
  <div style="display: grid; grid-template-columns: 20px 100px 56px 160px 56px 76px 68px minmax(0, 1fr) 70px; gap: 10px; padding: 6px 16px; border-bottom: 1px solid ${T.border}; font-size: 10px; color: ${T.muted}; text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600"><span></span><span>Worker</span><span>Version</span><span>Capabilities</span><span>Agents</span><span>Jobs</span><span>Checks</span><span>Load</span><span>Seen</span></div>
  ${workers}
</div>`;
fs.writeFileSync('Workers.dc.html', shell({
  active: 'Workers',
  crumbs: `<span style="color: ${T.text}; font-weight: 600">Workers</span>`,
  content: `
      <div style="display: flex; align-items: flex-start; justify-content: space-between">
        <div style="display: flex; flex-direction: column; gap: 8px">
          <div style="display: flex; align-items: center; gap: 12px"><h1 style="margin: 0; font-size: 20px; font-weight: 600">Workers &amp; zones</h1>${pill('1 worker degraded', T.warn)}<span style="font-size: 12px; color: ${T.muted}">4 zones · 5 workers · 126/128 agents connected · 3 jobs running</span></div>
          <div style="display: flex; gap: 24px; border-bottom: 1px solid ${T.border}">${tab('Zones', true)}${tab('Registration tokens')}${tab('Certificates')}${tab('Events')}</div>
        </div>
        <div style="display: flex; gap: 8px">${btn('Add zone', false, ic.globe)}${btn('Add worker', true, ic.cpu)}</div>
      </div>
      <div style="display: grid; grid-template-columns: minmax(0, 1fr) 400px; gap: 16px; flex-grow: 1; min-height: 0">
        <div style="display: flex; flex-direction: column; gap: 14px">
          ${zoneCard('fra', 'Hetzner Falkenstein · Europe/Berlin', 'bastion.fra', workerRow('fra-1', 'ok', 'v0.4.2', [['ansible', 1], ['checks', 1], ['gateway', 1]], '34', '2 running', '412/min', 62, '3 s') + workerRow('fra-2', 'ok', 'v0.4.2', [['ansible', 1], ['checks', 1], ['gateway', 1]], '30', '—', '398/min', 41, '2 s'), 64, 64, 41, 118)}
          ${zoneCard('hel', 'Hetzner Helsinki · Europe/Helsinki', 'bastion.hel', workerRow('hel-1', 'warn', 'v0.4.1', [['ansible', 1], ['checks', 1], ['gateway', 1]], '37', '1 running', '240/min', 88, 'high load'), 37, 38, 8, 52)}
          ${zoneCard('office-berlin', 'Office LAN · behind NAT', '—', workerRow('office-1', 'ok', 'v0.4.2', [['ansible', 0], ['checks', 1], ['gateway', 1]], '18', '—', '96/min', 12, '5 s'), 18, 18, 0, 22)}
          ${zoneCard('default', 'Built-in worker in the manager container', '—', workerRow('embedded', 'ok', 'v0.4.2', [['ansible', 1], ['checks', 1], ['gateway', 1]], '7', '—', '40/min', 8, '1 s'), 7, 8, 3, 9)}
        </div>
        <div style="display: flex; flex-direction: column; gap: 16px">
          ${card('Worker · hel-1', `<div style="display: flex; flex-direction: column; gap: 10px; padding: 14px 16px; font-size: 12px">
            <div style="display: flex; align-items: center; gap: 8px">${pill('degraded', T.warn)}<span style="color: ${T.dim}">CPU 88 % for 25 min · check queue 1 m 40 s behind</span></div>
            ${[['Host', 'worker-hel-1 · 2 vCPU · 4 GiB'], ['Address', '10.20.0.10 → manager pulsix.acme.io:8443'], ['Certificate', 'serial 0x2c1a · renews in 41 d'], ['Repositories', '4 cached · acme/legacy failing'], ['Ansible runtime', 'core 2.19 · runner 2.4 · 5 collections'], ['Agent connections', '37 · max 500']].map(([k, v]) => `<div style="display: flex; justify-content: space-between; gap: 12px"><span style="color: ${T.muted}">${k}</span><span style="font-family: ${MONO}; font-size: 11px; text-align: right">${v}</span></div>`).join('')}
            <div style="padding: 8px 10px; border-radius: 6px; background: ${T.ink}; border: 1px solid ${T.warn}55; color: ${T.warn}; font-size: 11px">Suggestion: add a second worker to zone hel. Agents and remote checks rebalance automatically.</div>
            <div style="display: flex; gap: 8px">${btn('Drain')}${btn('Restart')}${btn('Revoke')}</div>
          </div>`, `${tag('v0.4.1 · update available')}`)}
          ${card('Add worker to zone hel', `<div style="display: flex; flex-direction: column; gap: 12px; padding: 14px 16px; font-size: 12px">
            <div style="display: flex; gap: 8px">${chip('Docker', true)}${chip('systemd binary')}${chip('Compose service')}</div>
            ${codeBlock([`docker run -d --name pulsix-worker --restart unless-stopped \\`, `  -e PULSIX_MANAGER_ADDR=pulsix.acme.io:8443 \\`, `  -e PULSIX_ZONE=hel \\`, `  -e PULSIX_WORKER_TOKEN=<span style="color: ${T.accent}">pxw_8Hq…d2K</span> \\`, `  -p 8443:8443 -v pulsix-worker:/var/lib/pulsix \\`, `  noixdev/pulsix-worker:0.4.2`])}
            <span style="color: ${T.muted}">Token valid 24 h, single use. The worker connects outbound, receives its certificate and appears here as <span style="color: ${T.ok}">connected</span>. Agents of the zone get the new address on their next config sync.</span>
          </div>`, `${tag('token expires in 23 h 12 m')}`)}
          ${card('Recent events', `<div style="display: flex; flex-direction: column; gap: 6px; padding: 12px 16px; font-size: 11px">
            ${[['11:52', 'hel-1', 'load above 80 % for 20 min', T.warn], ['11:40', 'fra-2', 'took over 3 remote checks from fra-1 (maintenance)', T.dim], ['10:31', 'fra-1', 'job #1432 claimed', T.dim], ['09:12', 'office-1', 'certificate renewed', T.ok], ['Yesterday', 'pi-garage', 'agent reconnected after 9 min', T.dim]].map(([t, w, m, c]) => `<div style="display: flex; gap: 8px"><span style="font-family: ${MONO}; color: ${T.muted}; width: 60px; flex-shrink: 0">${t}</span><span style="font-family: ${MONO}; width: 64px; flex-shrink: 0">${w}</span><span style="color: ${c}">${m}</span></div>`).join('')}
          </div>`, `${ic.chevronR}`)}
        </div>
      </div>`,
  height: 1100,
}));

fs.writeFileSync('canvas.json', JSON.stringify({
  pages: [{ id: 'page-1', name: 'Core & monitoring' }, { id: 'page-2', name: 'Admin & Ansible' }],
  artboards: [
    { file: 'Main.dc.html', x: 0, y: 0, w: 1440, h: 960, title: 'Dashboard' },
    { file: 'HostDetail.dc.html', x: 1540, y: 0, w: 1440, h: 1080, title: 'Host detail · db-02.fra' },
    { file: 'HostList.dc.html', x: 0, y: 1260, w: 1440, h: 960, title: 'Hosts · managed vs. monitored' },
    { file: 'SchemaBuilder.dc.html', x: 1540, y: 1260, w: 1440, h: 1080, title: 'Inventory group · variable schema builder' },
    { file: 'MonitoringGroup.dc.html', x: 0, y: 2500, w: 1440, h: 1180, title: 'Monitoring group · selector, rules, routing' },
    { file: 'StatusPage.dc.html', x: 1540, y: 2500, w: 1440, h: 1260, title: 'Public status page' },
    { file: 'RuleEditor.dc.html', x: 0, y: 3920, w: 1440, h: 1080, title: 'Rule editor · form + expression + effective rules' },
    { file: 'StatusAdmin.dc.html', x: 1540, y: 3920, w: 1440, h: 1080, title: 'Status page admin' },
    { file: 'Workers.dc.html', x: 0, y: 6420, w: 1440, h: 1100, title: 'Workers & zones' },
    { file: 'Enrollment.dc.html', x: 0, y: 5160, w: 1440, h: 1000, title: 'Add host · step 2 agent enrollment' },
    { file: 'EnrollmentSSH.dc.html', x: 1540, y: 5160, w: 1440, h: 1100, title: 'Add host · step 3 SSH access (public key, bootstrap, test)' },

    { file: 'SetupWizard.dc.html', x: 0, y: 0, w: 1440, h: 900, title: 'First-run setup wizard', page: 'page-2' },
    { file: 'AccountSecurity.dc.html', x: 1540, y: 0, w: 1440, h: 1140, title: 'Account · security (TOTP, passkeys, sessions)', page: 'page-2' },
    { file: 'Members.dc.html', x: 0, y: 1300, w: 1440, h: 1140, title: 'Members · roles (ACL), invitations, tokens', page: 'page-2' },
    { file: 'Activity.dc.html', x: 1540, y: 1300, w: 1440, h: 1140, title: 'Activity feed', page: 'page-2' },
    { file: 'Credentials.dc.html', x: 0, y: 2600, w: 1440, h: 1100, title: 'Credentials, jump hosts, agent secrets', page: 'page-2' },
    { file: 'Playbooks.dc.html', x: 1540, y: 2600, w: 1440, h: 1100, title: 'Playbooks · repositories per org', page: 'page-2' },
    { file: 'Scheduler.dc.html', x: 0, y: 3860, w: 1440, h: 1460, title: 'Schedules · cron, calendar, notifications, webhook triggers', page: 'page-2' },
    { file: 'JobRun.dc.html', x: 1540, y: 3860, w: 1440, h: 1180, title: 'Ansible job · live run', page: 'page-2' },
  ],
  annotations: [
    { id: 'shell-note', x: 0, y: -170, w: 520, text: 'App shell: 232 px sidebar with org switcher, top bar with ⌘K search, zone filter, notifications.\nDark-first, tokens from brand/tokens.css. Status colours only for states, cyan for actions and selection.', page: 'page-1' },
    { id: 'host-note', x: 1540, y: -130, w: 520, text: 'Host detail: header with status + group tags, tab bar, 4 metric cards, check table (agent vs. remote source), facts, groups with host vars, jump host, local agent secrets (names only).', page: 'page-1' },
    { id: 'cap-note', x: 0, y: 1130, w: 560, text: 'Managed (Ansible via SSH) and Monitored (agent and/or remote checks) are independent per host. Rows show all four combinations.', page: 'page-1' },
    { id: 'schema-note', x: 1540, y: 1130, w: 560, text: 'Schema builder: field list → field editor → generated assignment form. Scope group = set once, scope host = asked on assignment. Repeater with sub-schema.', page: 'page-1' },
    { id: 'mon-note', x: 0, y: 2380, w: 560, text: 'Monitoring group: tag selector + manual hosts; check templates, alert rules, routing by schedule, escalation, maintenance with visibility, status-page toggle.', page: 'page-1' },
    { id: 'status-note', x: 1540, y: 2380, w: 560, text: 'Public status page: aggregated components with display names; no hostnames, no metrics. Incidents, scheduled maintenance, subscribe.', page: 'page-1' },
    { id: 'rule-note', x: 0, y: 3800, w: 560, text: 'Rule editor: scope, item filter, thresholds with duration and clear value, form ↔ expression, effective rules with precedence, dry run.', page: 'page-1' },
    { id: 'sadmin-note', x: 1540, y: 3800, w: 560, text: 'Status page admin: components mapping, incident composer, maintenance from windows, slug, custom domain, appearance.', page: 'page-1' },
    { id: 'enroll-note', x: 0, y: 5040, w: 560, text: 'Enrollment wizard: capability toggles, one-line install with single-use token per OS, live progress, SSH step only if managed.', page: 'page-1' },
    { id: 'workers-note', x: 0, y: 6300, w: 560, text: 'Workers & zones: one card per zone with jump-host default, agent/managed/remote-check counts and its workers (capabilities ansible/checks/gateway, agents, jobs, check throughput, load). Worker detail with drain/restart/revoke, add-worker command with single-use token, event log.', page: 'page-1' },
    { id: 'ssh-note', x: 1540, y: 5040, w: 560, text: 'SSH step: credential (Pulsix-generated key, import, password), public key with authorized_keys / cloud-init / Terraform snippets, optional one-time bootstrap with an existing login, connection test from the zone worker with host-key trust-on-first-use and pinning, sudo/Python checks.', page: 'page-1' },

    { id: 'setup-note', x: 0, y: -120, w: 560, text: 'First-run wizard (runs once): admin → first org (slug, default zone, timezone) → optional SMTP → forced second-factor enrolment.', page: 'page-2' },
    { id: 'security-note', x: 1540, y: -120, w: 560, text: 'Account security: passkeys, TOTP enrolment with QR + confirm, recovery codes shown once, password change, sessions.', page: 'page-2' },
    { id: 'members-note', x: 0, y: 1180, w: 560, text: 'Members = ACL: people, API tokens and invitations in one list; fixed role matrix viewer / operator / admin / owner; invite dialog; role change with owner-only guard.', page: 'page-2' },
    { id: 'activity-note', x: 1540, y: 1180, w: 560, text: 'Activity feed from the audit log: grouped by day, actor (person, token, Pulsix), verb, object, detail; filters; event detail with before/after diff. Scheduled runs land here too.', page: 'page-2' },
    { id: 'cred-note', x: 0, y: 2480, w: 560, text: 'Credentials & secrets: SSH keys/passwords, Git deploy keys/tokens, channel secrets in one encrypted store with usage, expiry warnings and rotation; jump hosts with inheritance; agent secret names.', page: 'page-2' },
    { id: 'pb-note', x: 1540, y: 2480, w: 560, text: 'Playbooks: several Git repositories per org (deploy key or token, branch, subpath, requirements.yml, webhook auto-sync), discovered playbooks with templates and last run; sync errors surfaced.', page: 'page-2' },
    { id: 'sched-note', x: 0, y: 3740, w: 560, text: 'Schedules: cron or simple mode with next runs, guards (overlap, maintenance, catch-up), per-schedule notification rules (failure / change / success / skipped → channels), weekly calendar incl. maintenance, run history; every run goes to the activity feed. Triggers per job template: manual, schedule, incoming webhook (HMAC or token, IP allowlist, payload → extra vars mapping, rate limit).', page: 'page-2' },
    { id: 'job-note', x: 1540, y: 3740, w: 560, text: 'Job run: streamed events, per-host results, failed task card with hint, timeline.', page: 'page-2' },
  ],
  launch: { view: 'canvas', page: 'page-2' },
}, null, 2));
console.log('built all 17');

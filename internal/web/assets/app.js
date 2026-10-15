// NetGold dashboard SPA — vanilla JS + uPlot (vendored, no CDN).
const $ = s => document.querySelector(s);
const api = async (path, opts) => {
  const r = await fetch(path, opts);
  const data = await r.json().catch(() => ({}));
  if (r.status === 401) {
    // The session is gone (signed out elsewhere, or expired): the
    // portal is behind the login page — go there.
    window.location.replace('/login');
    throw new Error('Sign-in required.');
  }
  if (!r.ok) throw new Error(data.error || r.statusText);
  return data;
};
const post = (path, body) => api(path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });

function fmtBps(b) {
  if (b == null) return '–';
  const u = ['bps', 'Kbps', 'Mbps', 'Gbps', 'Tbps']; let i = 0;
  while (b >= 1000 && i < 4) { b /= 1000; i++; }
  return b.toFixed(i ? 1 : 0) + ' ' + u[i];
}
function fmtSpeed(b) { return b ? fmtBps(b) : '–'; }
function fmtUptime(s) {
  if (s == null) return '–';
  const d = Math.floor(s / 86400), h = Math.floor(s % 86400 / 3600), m = Math.floor(s % 3600 / 60);
  return d ? `${d}d ${h}h` : h ? `${h}h ${m}m` : `${m}m`;
}
function fmtTime(ts) { return ts ? new Date(ts * 1000).toLocaleString() : '–'; }
const esc = s => String(s ?? '').replace(/[&<>"]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));

// ---------- sign-in state (v3.4) ----------
// The portal is gated server-side (see auth.go); the role kept here
// drives only the courtesy layer: viewer sessions get a "Read-only
// access" indicator and no change controls are rendered for them.
// The server refuses viewer mutations with 403 regardless.
const AUTH = { id: '', role: '' };
const IS_ADMIN = () => AUTH.role === 'admin';
async function initAuth() {
  try {
    const me = await api('/api/auth/me');
    AUTH.id = me.id; AUTH.role = me.role;
  } catch (e) { return; } // api() already redirected to /login on 401
  document.body.classList.toggle('role-viewer', !IS_ADMIN());
  document.body.classList.toggle('role-admin', IS_ADMIN());
  $('#user-role').innerHTML = IS_ADMIN()
    ? 'Signed in as <b>Administrator</b>'
    : `Signed in as <b>${esc(AUTH.id)}</b> (viewer)`;
}
$('#signout-btn').onclick = async () => {
  try { await post('/api/auth/logout', {}); } catch (e) { /* the session may already be gone */ }
  window.location.replace('/login');
};

// ---------- User Access settings (admin only, v3.4) ----------
async function loadAccessSettings() {
  if (!IS_ADMIN()) return; // the panel is hidden for viewers anyway
  try {
    const a = await api('/api/settings/access');
    $('#access-viewer-status').textContent = a.viewer_configured
      ? `Viewer access: enabled — viewers sign in with the ID "${a.viewer_id}".`
      : 'Viewer access: not enabled — set a viewer ID and password below to enable it.';
    const idEl = $('#access-viewer-id');
    if (document.activeElement !== idEl) idEl.value = a.viewer_id || '';
  } catch (e) { /* leave the panel as-is */ }
}
$('#access-viewer-form').addEventListener('submit', async ev => {
  ev.preventDefault();
  const f = new FormData(ev.target);
  const msg = $('#access-viewer-msg');
  try {
    const a = await api('/api/settings/access', {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ viewer_id: f.get('viewer_id'), viewer_password: f.get('viewer_password') })
    });
    ev.target.querySelector('input[name="viewer_password"]').value = '';
    msg.textContent = a.viewer_configured ? `Saved. Viewers now sign in as "${a.viewer_id}".` : 'Saved.';
    loadAccessSettings();
  } catch (e) { msg.textContent = e.message; }
});
$('#access-viewer-disable').onclick = async () => {
  const msg = $('#access-viewer-msg');
  try {
    await api('/api/settings/access', {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ viewer_id: '', viewer_password: '' })
    });
    $('#access-viewer-id').value = '';
    msg.textContent = 'Viewer access disabled.';
    loadAccessSettings();
  } catch (e) { msg.textContent = e.message; }
};
$('#access-admin-form').addEventListener('submit', async ev => {
  ev.preventDefault();
  const f = new FormData(ev.target);
  const msg = $('#access-admin-msg');
  try {
    await api('/api/settings/access/admin-password', {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ current_password: f.get('current_password'), new_password: f.get('new_password') })
    });
    ev.target.reset();
    msg.textContent = 'Administrator password changed.';
  } catch (e) { msg.textContent = e.message; }
});

// ---------- navigation ----------
let currentDevice = null, currentRange = '6h', currentIf = null, charts = {};
function show(view) {
  // The Topology section is hidden for now (v3.6.1): its tab is
  // gone, and anything that still asks for the view lands on the
  // Dashboard instead. The topology code itself is untouched and
  // returns with the section.
  if (view === 'topology') view = 'dashboard';
  document.querySelectorAll('.view').forEach(v => v.classList.add('hidden'));
  $('#view-' + view).classList.remove('hidden');
  document.querySelectorAll('.navbtn').forEach(b => b.classList.toggle('active', b.dataset.view === view));
  if (view === 'dashboard') loadDashboard();
  if (view === 'devices') loadDevices();
  if (view === 'maps') loadMaps();
  if (view === 'reports') loadReport();
  if (view === 'topology') { loadTopologies(); loadLinks(); }
  if (view === 'traffic') loadTraffic();
  if (view === 'logs') loadLogs();
  if (view === 'alarms') loadAlarmsPage();
  if (view === 'settings') { loadEmailSettings(); loadAccessSettings(); }
  if (view === 'import') refreshBakEngine();
}
document.querySelectorAll('.navbtn').forEach(b => b.onclick = () => show(b.dataset.view));
$('#back-btn').onclick = () => show('devices');

// ---------- dashboard ----------
// Animate a number counting toward its target (dashboard counters).
function countUp(el, to) {
  if (!el) return;
  const from = parseInt(el.dataset.v || '0', 10) || 0;
  el.dataset.v = to;
  if (from === to) { el.textContent = to; return; }
  const t0 = performance.now(), dur = 450;
  const step = t => {
    const k = Math.min(1, (t - t0) / dur);
    el.textContent = Math.round(from + (to - from) * (1 - Math.pow(1 - k, 3)));
    if (k < 1 && parseInt(el.dataset.v, 10) === to) requestAnimationFrame(step);
  };
  requestAnimationFrame(step);
}
function renderLiveSummary(st) {
  $('#live-summary').innerHTML = `<b>${st.total}</b> devices · <b class="up">${st.up} up</b> · <b class="down">${st.down} down</b>` +
    (st.disabled ? ` · <b>${st.disabled} monitoring off</b>` : '');
}
async function updateLiveSummary() {
  try { renderLiveSummary(await api('/api/stats')); } catch (e) { checkServerVersion(); }
}

// ---------- connection check (v2.2.2): stale-tab / dead-server trap ----------
// This page carries the build version of the program that served it
// (meta tag, stamped at serve time); the running program reports its
// own version at /api/version. If that endpoint stops answering, the
// program behind this tab is gone; if it answers with a different
// version, this tab is showing an older program's page. Either way,
// say so loudly instead of leaving a convincing dead page on screen.
const PAGE_VERSION = (document.querySelector('meta[name="netgold-build"]') || {}).content || '';
let connLost = false;
function showConnBanner(kind, text) {
  const b = $('#conn-banner');
  b.className = 'conn-banner ' + kind;
  b.textContent = text;
}
function hideConnBanner() { $('#conn-banner').className = 'conn-banner hidden'; }
async function checkServerVersion() {
  let serverVersion = null;
  try {
    const r = await fetch('/api/version', { cache: 'no-store' });
    if (r.status === 401) { window.location.replace('/login'); return; }
    if (!r.ok) throw new Error('bad status');
    serverVersion = (await r.json()).version;
  } catch (e) {
    connLost = true;
    $('#live-summary').textContent = 'Not connected';
    showConnBanner('lost', 'Connection to NetGold was lost — the program may have been closed. Start NetGold Monitor again, then refresh this page.');
    return;
  }
  const wasLost = connLost;
  connLost = false;
  if (PAGE_VERSION && serverVersion && serverVersion !== PAGE_VERSION) {
    showConnBanner('stale', `This page is from NetGold Monitor v${PAGE_VERSION}, but v${serverVersion} is now running. Refresh the page (F5) to use the new version.`);
  } else {
    hideConnBanner();
  }
  if (wasLost) updateLiveSummary();
}
async function loadDashboard() {
  try {
    const st = await api('/api/stats');
    renderLiveSummary(st);
    countUp($('#st-total'), st.total); countUp($('#st-up'), st.up); countUp($('#st-down'), st.down);
    document.querySelector('.card.down').classList.toggle('has-down', st.down > 0);
    const nowS = Math.floor(Date.now() / 1000);
    $('#down-list').innerHTML = st.down_devices.length
      ? st.down_devices.map(d => {
          const since = ALARM.downSince[d.id];
          const tier = since ? ` <span class="tier">${downTier(nowS - since)}</span> <span class="muted">(down for ${fmtDur(nowS - since)})</span>` : '';
          return `<div class="downrow">🔴 <b>${esc(d.name)}</b> <span class="muted">${esc(d.ip)}</span>${tier}</div>`;
        }).join('')
      : '<p class="muted">All devices up 🎉</p>';
    const top = await api('/api/top');
    $('#top-table tbody').innerHTML = top.map(t =>
      `<tr><td>${esc(t.device_name)}</td><td>${esc(t.if_name)}</td><td>${fmtBps(t.in_bps)}</td><td>${fmtBps(t.out_bps)}</td></tr>`).join('') || '<tr><td colspan="4" class="muted">No data yet</td></tr>';
    const evs = await api('/api/events');
    $('#events').innerHTML = evs.map(e =>
      `<div class="event ${e.kind}">${e.kind === 'down' ? '🔴' : e.kind === 'up' ? '🟢' : 'ℹ️'} ${esc(e.message)}<br><span class="muted">${fmtTime(e.ts)}</span></div>`).join('') || '<p class="muted">No events yet</p>';
  } catch (e) { console.error(e); }
}

// ---------- device icons (v3.3) ----------
// A crafted SVG icon set: one <symbol> per icon id resolved by the
// server (see internal/devicon). The artwork is the classic
// network-diagram language chosen for NetGold's maps: flat
// steel-gradient equipment pictograms with a consistent stroke —
// a router is a disc with opposing data arrows, a switch a wide
// body with port squares and crossed arrows, a firewall a brick
// wall with a shield — drawn on a 48x48 canvas so the pictograms
// stay crisp at map size. The sprite is injected once; every use
// is a cheap <use> reference.
const ICON_SPRITE = `<svg width="0" height="0" style="position:absolute" aria-hidden="true">
<defs>
<linearGradient id="ng-g-steel" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#7194ac"/><stop offset="1" stop-color="#40607a"/></linearGradient>
<linearGradient id="ng-g-jun" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#a84e57"/><stop offset="1" stop-color="#7c2d34"/></linearGradient>
<linearGradient id="ng-g-brick" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#c07a52"/><stop offset="1" stop-color="#9c5a38"/></linearGradient>
<linearGradient id="ng-g-darksteel" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#5d7185"/><stop offset="1" stop-color="#37485a"/></linearGradient>
<linearGradient id="ng-g-gold" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#ffd97a"/><stop offset="1" stop-color="#e8a33d"/></linearGradient>
</defs>
<symbol id="ng-icon-folder" viewBox="0 0 48 48">
  <path d="M5 13.5 C5 11.6 6.6 10 8.5 10 H18 L22.5 14.5 H39.5 C41.4 14.5 43 16.1 43 18 V35.5 C43 37.4 41.4 39 39.5 39 H8.5 C6.6 39 5 37.4 5 35.5 Z" fill="url(#ng-g-gold)" stroke="#8a5a17" stroke-width="2"/>
  <path d="M5 19.5 H43" stroke="#8a5a17" stroke-width="1.6" stroke-opacity=".55"/>
  <path d="M8.5 24.5 H39.5" stroke="#fff" stroke-width="1.4" stroke-opacity=".35"/>
</symbol>
<symbol id="ng-icon-router" viewBox="0 0 48 48">
  <circle cx="24" cy="24" r="15.5" fill="url(#ng-g-steel)" stroke="#2c4356" stroke-width="2"/>
  <circle cx="24" cy="24" r="11.8" fill="none" stroke="#ffffff" stroke-opacity=".38"/>
  <path d="M15.5 20.2 H29.4" stroke="#fff" stroke-width="2.4" stroke-linecap="round"/>
  <path d="M26.2 16.6 L30.4 20.2 L26.2 23.8" fill="none" stroke="#fff" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"/>
  <path d="M32.5 27.8 H18.6" stroke="#fff" stroke-width="2.4" stroke-linecap="round"/>
  <path d="M21.8 24.2 L17.6 27.8 L21.8 31.4" fill="none" stroke="#fff" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"/>
</symbol>
<symbol id="ng-icon-juniper" viewBox="0 0 48 48">
  <circle cx="24" cy="24" r="15.5" fill="url(#ng-g-jun)" stroke="#5f2126" stroke-width="2"/>
  <circle cx="24" cy="24" r="11.8" fill="none" stroke="#ffffff" stroke-opacity=".38"/>
  <path d="M15.5 20.2 H29.4" stroke="#fff" stroke-width="2.4" stroke-linecap="round"/>
  <path d="M26.2 16.6 L30.4 20.2 L26.2 23.8" fill="none" stroke="#fff" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"/>
  <path d="M32.5 27.8 H18.6" stroke="#fff" stroke-width="2.4" stroke-linecap="round"/>
  <path d="M21.8 24.2 L17.6 27.8 L21.8 31.4" fill="none" stroke="#fff" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"/>
</symbol>
<symbol id="ng-icon-switch" viewBox="0 0 48 48">
  <rect x="5" y="14" width="38" height="20" rx="3.5" fill="url(#ng-g-steel)" stroke="#2c4356" stroke-width="2"/>
  <path d="M11.5 20.6 H19" stroke="#fff" stroke-width="2" stroke-linecap="round"/>
  <path d="M16.6 18 L19.8 20.6 L16.6 23.2" fill="none" stroke="#fff" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>
  <path d="M36.5 20.6 H29" stroke="#fff" stroke-width="2" stroke-linecap="round"/>
  <path d="M31.4 18 L28.2 20.6 L31.4 23.2" fill="none" stroke="#fff" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>
  <g fill="#d9e6ee">
    <rect x="9.4" y="26.8" width="5.6" height="4.6" rx="0.8"/>
    <rect x="17.9" y="26.8" width="5.6" height="4.6" rx="0.8"/>
    <rect x="26.4" y="26.8" width="5.6" height="4.6" rx="0.8"/>
    <rect x="34.9" y="26.8" width="5.6" height="4.6" rx="0.8"/>
  </g>
</symbol>
<symbol id="ng-icon-ascend" viewBox="0 0 48 48">
  <rect x="7" y="12" width="34" height="24" rx="5" fill="url(#ng-g-steel)" stroke="#2c4356" stroke-width="2"/>
  <circle cx="24" cy="24" r="7" fill="none" stroke="#fff" stroke-width="1.8"/>
  <ellipse cx="24" cy="24" rx="3.1" ry="7" fill="none" stroke="#fff" stroke-width="1.3" stroke-opacity=".85"/>
  <path d="M17 24 H31" stroke="#fff" stroke-width="1.3" stroke-opacity=".85"/>
  <circle cx="12.3" cy="31.6" r="1.3" fill="#8fe3a8"/>
  <circle cx="35.7" cy="31.6" r="1.3" fill="#ffd27a"/>
</symbol>
<symbol id="ng-icon-server" viewBox="0 0 48 48">
  <rect x="12" y="7" width="24" height="34" rx="3" fill="url(#ng-g-steel)" stroke="#2c4356" stroke-width="2"/>
  <path d="M12 18.3 H36 M12 29.6 H36" stroke="#ffffff" stroke-opacity=".4" stroke-width="1.2"/>
  <g stroke="#dfeaf1" stroke-width="1.6" stroke-linecap="round" stroke-opacity=".9">
    <path d="M15.5 11.4 H21 M15.5 14.2 H21 M15.5 22.7 H21 M15.5 25.5 H21 M15.5 34 H21 M15.5 36.8 H21"/>
  </g>
  <circle cx="32.6" cy="12.8" r="1.5" fill="#8fe3a8"/>
  <circle cx="32.6" cy="24.1" r="1.5" fill="#ffd27a"/>
  <circle cx="32.6" cy="35.4" r="1.5" fill="#8fe3a8"/>
</symbol>
<symbol id="ng-icon-linux-server" viewBox="0 0 48 48">
  <rect x="12" y="7" width="24" height="34" rx="3" fill="url(#ng-g-darksteel)" stroke="#222d38" stroke-width="2"/>
  <path d="M12 18.3 H36 M12 29.6 H36" stroke="#ffffff" stroke-opacity=".35" stroke-width="1.2"/>
  <g stroke="#dfeaf1" stroke-width="1.5" stroke-linecap="round" stroke-opacity=".85">
    <path d="M15.5 11.4 H20.5 M15.5 14.2 H20.5 M15.5 33.8 H20.5 M15.5 36.6 H20.5"/>
  </g>
  <ellipse cx="24" cy="24.6" rx="4.1" ry="4.3" fill="#f4f7fa"/>
  <circle cx="24" cy="20.6" r="2.7" fill="#12181f"/>
  <circle cx="23" cy="20" r=".8" fill="#fff"/><circle cx="25" cy="20" r=".8" fill="#fff"/>
  <path d="M22.9 21.7 h2.2 l-1.1 1.2 Z" fill="#f0a63c"/>
  <ellipse cx="20.9" cy="29.2" rx="2" ry="1" fill="#f0a63c" transform="rotate(-16 20.9 29.2)"/>
  <ellipse cx="27.1" cy="29.2" rx="2" ry="1" fill="#f0a63c" transform="rotate(16 27.1 29.2)"/>
  <circle cx="32.6" cy="12.8" r="1.4" fill="#8fe3a8"/>
  <circle cx="32.6" cy="35.4" r="1.4" fill="#ffd27a"/>
</symbol>
<symbol id="ng-icon-windows-server" viewBox="0 0 48 48">
  <rect x="12" y="7" width="24" height="34" rx="3" fill="url(#ng-g-steel)" stroke="#2c4356" stroke-width="2"/>
  <path d="M12 18.3 H36 M12 29.6 H36" stroke="#ffffff" stroke-opacity=".4" stroke-width="1.2"/>
  <g stroke="#dfeaf1" stroke-width="1.5" stroke-linecap="round" stroke-opacity=".9">
    <path d="M15.5 11.4 H20.5 M15.5 14.2 H20.5 M15.5 33.8 H20.5 M15.5 36.6 H20.5"/>
  </g>
  <path d="M19.4 20.9 l4.4-.7 v3.6 h-4.4 Z" fill="#bfe0f7"/>
  <path d="M25 20 l4.4-.7 v4.5 H25 Z" fill="#e6f3fd"/>
  <path d="M19.4 25 h4.4 v3.6 l-4.4-.7 Z" fill="#8ec9f2"/>
  <path d="M25 25 h4.4 v4.5 L25 28.8 Z" fill="#bfe0f7"/>
  <circle cx="32.6" cy="12.8" r="1.4" fill="#8fe3a8"/>
  <circle cx="32.6" cy="35.4" r="1.4" fill="#8fe3a8"/>
</symbol>
<symbol id="ng-icon-workstation" viewBox="0 0 48 48">
  <rect x="6" y="10" width="36" height="25" rx="2.5" fill="url(#ng-g-steel)" stroke="#2c4356" stroke-width="2"/>
  <rect x="9.5" y="13.5" width="29" height="18" rx="1.2" fill="#dcebf5"/>
  <path d="M9.5 31.5 L25 13.5 h13.5 v4.5 L19.5 31.5 Z" fill="#ffffff" opacity=".35"/>
  <path d="M21.5 35 h5 l1.2 4.5 H20.3 Z" fill="#40607a"/>
  <rect x="14.5" y="40.5" width="19" height="2.8" rx="1.4" fill="#40607a"/>
</symbol>
<symbol id="ng-icon-firewall" viewBox="0 0 48 48">
  <rect x="6" y="13" width="36" height="22" rx="2" fill="url(#ng-g-brick)" stroke="#6e3b26" stroke-width="2"/>
  <g stroke="#f3d9c6" stroke-opacity=".5" stroke-width="1.3">
    <path d="M6 20.3 H42 M6 27.6 H42"/>
    <path d="M18 13 V20.3 M30 13 V20.3 M12 20.3 V27.6 M24 20.3 V27.6 M36 20.3 V27.6 M18 27.6 V35 M30 27.6 V35"/>
  </g>
  <path d="M24 18.4 L29.4 20.4 V24.8 C29.4 28.5 27.2 30.8 24 32.3 C20.8 30.8 18.6 28.5 18.6 24.8 V20.4 Z"
        fill="#f7fafc" stroke="#6e3b26" stroke-width="1.4"/>
  <path d="M21.2 25.1 L23.2 27.1 L26.8 23.3" fill="none" stroke="#3e7d4f" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"/>
</symbol>
<symbol id="ng-icon-hub" viewBox="0 0 48 48">
  <path d="M24 24 L11 11 M24 24 L37 11 M24 24 L11 37 M24 24 L37 37" stroke="#40607a" stroke-width="2"/>
  <rect x="15" y="15" width="18" height="18" rx="4" fill="url(#ng-g-steel)" stroke="#2c4356" stroke-width="2"/>
  <circle cx="24" cy="24" r="2.6" fill="#fff"/>
  <circle cx="11" cy="11" r="3.6" fill="url(#ng-g-steel)" stroke="#2c4356" stroke-width="1.8"/>
  <circle cx="37" cy="11" r="3.6" fill="url(#ng-g-steel)" stroke="#2c4356" stroke-width="1.8"/>
  <circle cx="11" cy="37" r="3.6" fill="url(#ng-g-steel)" stroke="#2c4356" stroke-width="1.8"/>
  <circle cx="37" cy="37" r="3.6" fill="url(#ng-g-steel)" stroke="#2c4356" stroke-width="1.8"/>
</symbol>
<symbol id="ng-icon-wireless-ap" viewBox="0 0 48 48">
  <path d="M10.5 20 A19 19 0 0 1 37.5 20" fill="none" stroke="#40607a" stroke-width="2.6" stroke-linecap="round"/>
  <path d="M15.8 26.6 A11.5 11.5 0 0 1 32.2 26.6" fill="none" stroke="#40607a" stroke-width="2.6" stroke-linecap="round"/>
  <circle cx="24" cy="31.5" r="3" fill="url(#ng-g-steel)" stroke="#2c4356" stroke-width="1.8"/>
  <rect x="13" y="39" width="22" height="4.6" rx="2.3" fill="url(#ng-g-steel)" stroke="#2c4356" stroke-width="1.8"/>
</symbol>
<symbol id="ng-icon-loadbalancer" viewBox="0 0 48 48">
  <path d="M21.5 13.5 C18 20 13.5 23.5 12 29.5 M26.5 13.5 C30 20 34.5 23.5 36 29.5" fill="none" stroke="#40607a" stroke-width="2.2"/>
  <circle cx="24" cy="9.5" r="5" fill="url(#ng-g-steel)" stroke="#2c4356" stroke-width="2"/>
  <rect x="4.5" y="30" width="15" height="11" rx="2.4" fill="url(#ng-g-steel)" stroke="#2c4356" stroke-width="2"/>
  <rect x="28.5" y="30" width="15" height="11" rx="2.4" fill="url(#ng-g-steel)" stroke="#2c4356" stroke-width="2"/>
  <path d="M7.5 34 h9 M7.5 37.4 h9 M31.5 34 h9 M31.5 37.4 h9" stroke="#dfeaf1" stroke-width="1.6" stroke-linecap="round"/>
</symbol>
<symbol id="ng-icon-ups" viewBox="0 0 48 48">
  <rect x="14" y="7" width="20" height="34" rx="3.5" fill="url(#ng-g-darksteel)" stroke="#222d38" stroke-width="2"/>
  <path d="M25.8 13 L19 25.5 h4.6 L21.8 34 L29.5 21 h-4.8 L27 13 Z" fill="url(#ng-g-gold)" stroke="#8a5a00" stroke-width="1.1"/>
  <circle cx="24" cy="37.9" r="1.4" fill="#8fe3a8"/>
</symbol>
<symbol id="ng-icon-printer" viewBox="0 0 48 48">
  <rect x="16" y="6" width="16" height="11" rx="1" fill="#f4f8fb" stroke="#9db1c5" stroke-width="1.2"/>
  <path d="M19.5 9.5 h9 M19.5 13 h9" stroke="#9db1c5" stroke-width="1.4" stroke-linecap="round"/>
  <rect x="7" y="15" width="34" height="13" rx="2.6" fill="url(#ng-g-steel)" stroke="#2c4356" stroke-width="2"/>
  <circle cx="36.6" cy="19.6" r="1.4" fill="#8fe3a8"/>
  <rect x="16" y="24" width="16" height="12" rx="1" fill="#f4f8fb" stroke="#9db1c5" stroke-width="1.2"/>
  <path d="M19.5 28.5 h9 M19.5 32 h6.5" stroke="#9db1c5" stroke-width="1.4" stroke-linecap="round"/>
</symbol>
<symbol id="ng-icon-phone" viewBox="0 0 48 48">
  <path d="M16.9 5.9 C19.1 5.7 22.8 9.4 23 12.1 C23.1 14.1 21.7 15.3 20.1 16.1 C21.9 20.2 25.7 24 29.8 25.8 C30.6 24.2 31.8 22.9 33.8 23 C36.5 23.2 40.2 26.9 40 29.1 C39.8 32 36.9 34 33.3 33.4 C24.7 32.1 14.7 22.1 13.4 13.5 C12.9 9.9 14.7 6.2 16.9 5.9 Z" fill="url(#ng-g-steel)" stroke="#2c4356" stroke-width="2" transform="translate(2.4 6.4)"/>
</symbol>
<symbol id="ng-icon-cloud" viewBox="0 0 48 48">
  <path d="M36 34.5 H18.4 a7.2 7.2 0 1 1 1-14.3 A10.2 10.2 0 0 1 39 23.4 a6.6 6.6 0 0 1-3 11.1 Z" fill="url(#ng-g-steel)" stroke="#2c4356" stroke-width="2"/>
  <path d="M18 27.5 a7.6 7.6 0 0 1 4.5-6.5" fill="none" stroke="#ffffff" stroke-opacity=".55" stroke-width="1.8" stroke-linecap="round"/>
</symbol>
<symbol id="ng-icon-generic" viewBox="0 0 48 48">
  <rect x="9" y="9" width="30" height="30" rx="7" fill="url(#ng-g-steel)" stroke="#2c4356" stroke-width="2"/>
  <path d="M17.5 19 L29 17 M17.5 19 L24 30 M29 17 L24 30" stroke="#fff" stroke-width="1.7" stroke-opacity=".9"/>
  <circle cx="17.5" cy="19" r="2.7" fill="#fff"/>
  <circle cx="29" cy="17" r="2.7" fill="#fff"/>
  <circle cx="24" cy="30" r="2.7" fill="#fff"/>
</symbol>
</svg>`;

(function injectIconSprite() {
  if (document.getElementById('ng-icon-sprite')) return;
  const holder = document.createElement('div');
  holder.id = 'ng-icon-sprite';
  holder.innerHTML = ICON_SPRITE;
  document.body.insertBefore(holder, document.body.firstChild);
})();
// iconChip is the small standalone icon used in tables and legends.
function iconChip(iconId, size) {
  const s = size || 22;
  return `<svg class="icon-chip" width="${s}" height="${s}" viewBox="0 0 48 48"><use href="#ng-icon-${esc(iconId || 'generic')}"/></svg>`;
}
// wrapLabel splits a device name into display lines of at most
// perLine characters, breaking at word boundaries where the name
// has them. Since v3.6 a name is NEVER shortened: however many
// lines it takes, every character is kept — a cut-off name on a
// network diagram is a wrong name.
function wrapLabel(text, perLine) {
  const max = perLine || 16;
  const words = String(text).split(/\s+/).filter(w => w.length);
  const lines = [''];
  for (const w of words) {
    const cur = lines[lines.length - 1];
    if (cur && (cur + ' ' + w).length > max) lines.push(w);
    else lines[lines.length - 1] = cur ? cur + ' ' + w : w;
  }
  return lines.length ? lines : [''];
}

// ---------- devices ----------
async function loadGroups() {
  const gs = await api('/api/groups');
  const sel = $('#group-select');
  sel.innerHTML = '<option value="">No group</option>' + gs.map(g => `<option value="${g.id}">${esc(g.name)}</option>`).join('');
  return gs;
}
async function loadDevices() {
  const tbody = $('#dev-table tbody');
  if (!tbody.children.length) tbody.innerHTML = '<tr><td colspan="8"><div class="skeleton"></div><div class="skeleton"></div></td></tr>';
  await loadGroups();
  const devs = await api('/api/devices');
  const byId = new Map(devs.map(d => [d.id, d]));
  $('#dev-table tbody').innerHTML = devs.map(d => `<tr>
    <td>${d.monitoring_disabled ? '⏸' : d.status === 'up' ? '🟢' : d.status === 'down' ? '🔴' : '⚪'}</td>
    <td><a href="#" data-dev="${d.id}"><b>${esc(d.name)}</b></a>${d.monitoring_disabled ? ' <span class="badge">Monitoring off</span>' : ''}${d.sys_name && d.sys_name !== d.name ? `<br><span class="muted">${esc(d.sys_name)}</span>` : ''}</td>
    <td>${esc(d.ip)}</td><td><span class="dev-type">${iconChip(d.icon_id)}<span>${esc(d.device_type || d.type)}</span></span></td>
    <td>${d.latency_ms != null ? d.latency_ms.toFixed(1) + ' ms' : '–'}</td>
    <td>${fmtUptime(d.uptime_s)}</td><td>${fmtTime(d.last_poll)}</td>
    <td>${IS_ADMIN() ? `<button data-edit="${d.id}" title="Edit this device">✏️ Edit</button> <button data-mon="${d.id}" title="${d.monitoring_disabled ? 'Switch monitoring back on' : 'Stop polling this device'}">${d.monitoring_disabled ? '▶ Enable' : '⏸ Disable'}</button> <button data-del="${d.id}" title="Delete this device">🗑</button>` : ''}</td></tr>`).join('') || '<tr><td colspan="8" class="muted">No devices yet — add one above, scan a subnet, or run with -demo</td></tr>';
  document.querySelectorAll('[data-dev]').forEach(a => a.onclick = e => { e.preventDefault(); openDevice(+a.dataset.dev); });
  document.querySelectorAll('[data-edit]').forEach(b => b.onclick = async () => {
    await openDevice(+b.dataset.edit);
    $('#edit-panel').classList.remove('hidden');
  });
  document.querySelectorAll('[data-mon]').forEach(b => b.onclick = async () => {
    const d = byId.get(+b.dataset.mon);
    if (!d) return;
    const body = {
      name: d.name, ip: d.ip, type: d.type, group_id: d.group_id || null,
      community: d.community || 'public', snmp_disabled: !!d.snmp_disabled,
      snmp_version: d.snmp_version || 'v2c',
      v3_sec_name: d.v3_sec_name || '', v3_sec_level: d.v3_sec_level || '',
      v3_auth_proto: d.v3_auth_proto || '', v3_auth_pass: d.v3_auth_pass || '',
      v3_priv_proto: d.v3_priv_proto || '', v3_priv_pass: d.v3_priv_pass || '',
      monitoring_disabled: !d.monitoring_disabled,
    };
    try {
      await api('/api/devices/' + d.id, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
      loadDevices();
      updateLiveSummary();
    } catch (err) { alert(err.message); }
  });
  document.querySelectorAll('[data-del]').forEach(b => b.onclick = async () => {
    if (confirm('Delete this device and its history?')) { await api('/api/devices/' + b.dataset.del, { method: 'DELETE' }); loadDevices(); }
  });
}
function wireSnmpVersion(selectSel, fieldsSel) {
  const sel = $(selectSel), box = $(fieldsSel);
  if (!sel || !box) return;
  const sync = () => box.classList.toggle('hidden', sel.value !== 'v3');
  sel.onchange = sync;
  sync();
}
wireSnmpVersion('#add-snmp-version', '#add-v3-fields');
wireSnmpVersion('#edit-snmp-version', '#edit-v3-fields');

function snmpFieldsFromForm(f) {
  const body = {
    community: f.get('community') || 'public',
    snmp_version: f.get('snmp_version') || 'v2c',
    v3_sec_name: f.get('v3_sec_name') || '',
    v3_sec_level: f.get('v3_sec_level') || '',
    v3_auth_proto: f.get('v3_auth_proto') || '',
    v3_auth_pass: f.get('v3_auth_pass') || '',
    v3_priv_proto: f.get('v3_priv_proto') || '',
    v3_priv_pass: f.get('v3_priv_pass') || '',
  };
  return body;
}
$('#add-form').onsubmit = async e => {
  e.preventDefault();
  const f = new FormData(e.target);
  const body = { name: f.get('name'), ip: f.get('ip'), type: f.get('type'), ...snmpFieldsFromForm(f) };
  const g = f.get('group_id'); if (g) body.group_id = +g;
  try { await post('/api/devices', body); uiTick(true); e.target.reset(); e.target.community.value = 'public'; $('#add-snmp-version').value = 'v2c'; $('#add-v3-fields').classList.add('hidden'); loadDevices(); }
  catch (err) { uiTick(false); alert(err.message); }
};
$('#group-form').onsubmit = async e => {
  e.preventDefault();
  const f = new FormData(e.target);
  try { await post('/api/groups', { name: f.get('name') }); e.target.reset(); loadGroups(); } catch (err) { alert(err.message); }
};

// ---------- device detail + MRTG-style graphs ----------
function makeChart(el, seriesDefs, yFmt) {
  if (charts[el.id]) { charts[el.id].destroy(); }
  el.innerHTML = '';
  const opts = {
    width: el.clientWidth || 520, height: 180,
    scales: { y: { auto: true } },
    axes: [{}, { size: 55, values: (u, vs) => vs.map(v => yFmt(v)) }],
    series: [{}, ...seriesDefs],
    legend: { show: true },
  };
  return data => {
    if (!charts[el.id]) charts[el.id] = new uPlot(opts, data, el);
    else charts[el.id].setData(data);
  };
}
const pctFmt = v => v == null ? '' : v.toFixed(0) + '%';
const msFmt = v => v == null ? '' : v.toFixed(1);
const bpsFmtAxis = v => v == null ? '' : fmtBps(v);

async function loadGraph(kind, el, setters, extra = '') {
  const g = await api(`/api/graph?device=${currentDevice}&kind=${kind}&range=${currentRange}${extra}`);
  const ts = g.ts;
  const names = Object.keys(g.series);
  setters(names.map(n => g.series[n]), ts);
}
const MONITOR_PORTS = { HTTP: 80, HTTPS: 443, FTP: 21, SMTP: 25, POP3: 110, IMAP: 143, NNTP: 119, Telnet: 23, SSH: 22, DNS: 53, Echo: 7, Time: 37, TCP: 0 };
let currentDeviceData = null;

function renderMonitors(d, mons) {
  const el = $('#dev-monitors');
  let html =
    (d.monitoring_disabled ? '<span class="monchip">⏸ Monitoring is off for this device — it is not being polled</span>' : '') +
    (d.needs_credentials ? '<span class="monchip">⚠️ SNMP community needs re-entry (was encrypted in the legacy system)</span>' : '') +
    (d.snmp_disabled ? '<span class="monchip">Ping-only (SNMP off)</span>' : '') +
    (d.snmp_version === 'v3' ? `<span class="monchip">SNMP v3 · ${esc(d.v3_sec_level || 'noAuthNoPriv')} · user ${esc(d.v3_sec_name || '')}</span>` : '');
  if (mons.length) {
    html += mons.map(m => {
      const st = m.kind === 'service' && m.state ? ' st-' + m.state : '';
      const stateTxt = m.kind === 'service' ? (m.state ? ` · ${m.state}` : ' · not checked yet') : '';
      const title = m.last_detail ? ` title="${esc(m.last_detail)}"` : '';
      return `<span class="monchip${st}"${title}><span class="dot"></span>${esc(m.name)}${m.port ? ':' + m.port : ''}${stateTxt}` +
        (IS_ADMIN() ? `<button data-mondel="${m.id}" title="Remove this monitor">✕</button>` : '') + `</span>`;
    }).join('');
  } else {
    html += '<span class="muted">No monitors yet — add a service monitor below.</span>';
  }
  el.innerHTML = html;
  el.querySelectorAll('[data-mondel]').forEach(b => b.onclick = async () => {
    if (!confirm('Remove this monitor?')) return;
    await api(`/api/devices/${currentDevice}/monitors/${b.dataset.mondel}`, { method: 'DELETE' });
    openDevice(currentDevice);
  });
}

// Format a Date for a datetime-local input, in local time.
function dtLocalValue(d) {
  const p = n => String(n).padStart(2, '0');
  return d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate()) +
    'T' + p(d.getHours()) + ':' + p(d.getMinutes());
}

// Point the timeline export links at the open device, carrying
// the From/To window chosen on the device page (v3.6.1). A field
// left empty stays out of the URL; both empty means full history.
function syncTimelineExportLinks() {
  if (!currentDevice) return;
  const params = new URLSearchParams();
  const f = $('#tl-export-from'), t = $('#tl-export-to');
  if (f && f.value) params.set('from', f.value);
  if (t && t.value) params.set('to', t.value);
  const qs = params.toString();
  const suffix = qs ? '?' + qs : '';
  const tlx = $('#tl-export-xlsx'); if (tlx) tlx.href = `/api/devices/${currentDevice}/timeline.xlsx${suffix}`;
  const tlp = $('#tl-export-pdf'); if (tlp) tlp.href = `/api/devices/${currentDevice}/timeline.pdf${suffix}`;
}
['tl-export-from', 'tl-export-to'].forEach(id => {
  const el = document.getElementById(id);
  if (el) el.addEventListener('change', syncTimelineExportLinks);
});

async function openDevice(id) {
  currentDevice = id; currentIf = null;
  show('device');
  const d = await api('/api/devices/' + id);
  currentDeviceData = d;
  $('#dev-title').textContent = (d.monitoring_disabled ? '⏸ ' : d.status === 'up' ? '🟢 ' : d.status === 'down' ? '🔴 ' : '⚪ ') + d.name;
  $('#dev-meta').textContent = `${d.ip} · ${d.type}${d.sys_descr ? ' · ' + d.sys_descr : ''}`;
  renderDeviceHead(d);
  // Header actions (v3.6): the monitoring on/off switch and the
  // delete button live here as well as on the Devices page; the
  // timeline export links point at this device's downloads.
  const monBtn = $('#dev-mon-btn');
  if (monBtn) monBtn.textContent = d.monitoring_disabled ? 'Enable Monitoring' : 'Disable Monitoring';
  // Timeline export window (v3.6.1): prefill From = 7 days ago and
  // To = now each time a device is opened; clearing both fields
  // exports the full history again.
  const tlf = $('#tl-export-from'), tlt = $('#tl-export-to');
  if (tlf && tlt) {
    const nowD = new Date();
    tlf.value = dtLocalValue(new Date(nowD.getTime() - 7 * 24 * 3600 * 1000));
    tlt.value = dtLocalValue(nowD);
  }
  syncTimelineExportLinks();
  const mons = await api(`/api/devices/${id}/monitors`).catch(() => []);
  renderMonitors(d, mons);
  fillEditForm(d);
  loadTimeline().catch(() => {});
  loadDeviceEvents().catch(() => {});
  await refreshDevice();
}

// The device header: the same status marker the maps use, the
// device's type / address / group identity, live status and the last
// state change. Uptime figures arrive with the timeline response.
async function renderDeviceHead(d) {
  const groups = await api('/api/groups').catch(() => []);
  const grp = groups.find(g => g.id === d.group_id);
  const st = d.status || 'unknown';
  $('#dev-head-marker').innerHTML = markerChip(st, d.icon_id, 72);
  $('#dev-head-line').innerHTML =
    `<span class="dev-head-name">${esc(d.name)}</span> <span class="tip-status st-${st}">${st === 'up' ? 'Up' : st === 'down' ? 'Down' : 'Unknown'}</span>` +
    (d.monitoring_disabled ? ' <span class="badge">Monitoring off — not being polled</span>' : '') +
    (d.needs_credentials ? ' <span class="badge">SNMP community needs re-entry</span>' : '');
  $('#dev-head-sub').textContent =
    `${d.device_type || d.type || 'Device'} · ${d.ip} · Group: ${grp ? grp.name : '—'}` +
    `${d.sys_name ? ' · ' + d.sys_name : ''} · Last poll: ${d.last_poll ? fmtTime(d.last_poll) : 'never'}`;
}
function fmtUptime(v) { return v == null || v < 0 ? '—' : (Math.round(v * 10) / 10).toFixed(1) + '%'; }
function fmtDur(sec) {
  sec = Math.max(0, Math.round(sec));
  if (sec < 60) return sec + 's';
  if (sec < 3600) return Math.floor(sec / 60) + 'm';
  if (sec < 86400) return Math.floor(sec / 3600) + 'h ' + String(Math.floor((sec % 3600) / 60)).padStart(2, '0') + 'm';
  return Math.floor(sec / 86400) + 'd ' + Math.floor((sec % 86400) / 3600) + 'h';
}
function fmtClock(ts) {
  const dt = new Date(ts * 1000);
  const today = new Date();
  const sameDay = dt.toDateString() === today.toDateString();
  const hm = dt.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  return sameDay ? hm : dt.toLocaleDateString([], { day: 'numeric', month: 'short' }) + ' ' + hm;
}

// ---- status timeline (device detail) ----
let tlRange = '48h';
document.querySelectorAll('.tlbtn').forEach(b => b.onclick = () => {
  tlRange = b.dataset.tlrange;
  document.querySelectorAll('.tlbtn').forEach(x => x.classList.toggle('active', x === b));
  loadTimeline().catch(() => {});
});
async function loadTimeline() {
  if (!currentDevice) return;
  const t = await api(`/api/devices/${currentDevice}/timeline?range=${tlRange}`);
  $('#dev-uptime-24h').textContent = fmtUptime(t.uptime_24h);
  $('#dev-uptime-7d').textContent = fmtUptime(t.uptime_7d);
  const span = Math.max(1, t.to_ts - t.from_ts);
  $('#tl-summary').textContent =
    `Uptime in range: ${fmtUptime(t.uptime_pct)} · Last change: ${t.last_change ? fmtTime(t.last_change) : 'none recorded'}`;
  $('#tl-bar').innerHTML = t.segments.map(s =>
    `<div class="tl-seg tl-seg-${s.state}" style="width:${(100 * (s.to_ts - s.from_ts) / span).toFixed(2)}%" title="${esc(s.state)} · ${fmtClock(s.from_ts)} → ${s.ongoing ? 'now' : fmtClock(s.to_ts)} · ${fmtDur(s.duration_sec)}"></div>`).join('')
    || '<div class="tl-seg tl-seg-unknown" style="width:100%"></div>';
  const rows = [...t.segments].reverse().map(s => {
    const stLabel = s.state === 'up' ? 'Up' : s.state === 'down' ? 'Down' : 'Unknown';
    return `<div class="tl-row"><span class="tl-state st-${s.state}">${stLabel}</span>` +
      `<span>${fmtClock(s.from_ts)} → ${s.ongoing ? 'now' : fmtClock(s.to_ts)}</span>` +
      `<span class="muted">${fmtDur(s.duration_sec)}${s.ongoing ? ' · still ' + s.state : ''}</span></div>`;
  }).join('');
  $('#tl-list').innerHTML = rows || '<p class="muted">No state history in this range yet.</p>';
}
async function loadDeviceEvents() {
  if (!currentDevice) return;
  const evs = await api(`/api/events?device=${currentDevice}&limit=15`);
  $('#dev-events').innerHTML = evs.map(e =>
    `<div class="event">${e.kind === 'down' ? '🔴' : e.kind === 'up' ? '🟢' : 'ℹ️'} ${fmtTime(e.ts)} — ${esc(e.message)}</div>`).join('')
    || '<p class="muted">No events recorded for this device yet.</p>';
}

async function fillEditForm(d) {
  const f = $('#edit-form');
  const el = n => f.elements.namedItem(n);
  el('name').value = d.name || '';
  if (el('ip')) el('ip').value = d.ip || '';
  if (el('monitoring_disabled')) el('monitoring_disabled').checked = !!d.monitoring_disabled;
  el('type').value = d.type || 'unknown';
  el('community').value = d.community || 'public';
  el('snmp_disabled').checked = !!d.snmp_disabled;
  $('#edit-snmp-version').value = d.snmp_version || 'v2c';
  $('#edit-v3-fields').classList.toggle('hidden', (d.snmp_version || 'v2c') !== 'v3');
  el('v3_sec_name').value = d.v3_sec_name || '';
  el('v3_sec_level').value = d.v3_sec_level || 'noAuthNoPriv';
  el('v3_auth_proto').value = d.v3_auth_proto || '';
  el('v3_auth_pass').value = d.v3_auth_pass || '';
  el('v3_priv_proto').value = d.v3_priv_proto || '';
  el('v3_priv_pass').value = d.v3_priv_pass || '';
  const gs = await api('/api/groups').catch(() => []);
  const sel = $('#edit-group-select');
  sel.innerHTML = '<option value="">No group</option>' + gs.map(g => `<option value="${g.id}">${esc(g.name)}</option>`).join('');
  sel.value = d.group_id || '';
}
$('#edit-btn').onclick = () => $('#edit-panel').classList.toggle('hidden');
// Device header actions (v3.6): switch monitoring off/on and
// delete, without leaving the device page. The PUT carries the
// device's current values (the masked password fields are mapped
// back to the stored passwords by the server).
$('#dev-mon-btn') && ($('#dev-mon-btn').onclick = async () => {
  const d = currentDeviceData;
  if (!d) return;
  const body = {
    name: d.name, ip: d.ip, type: d.type, group_id: d.group_id || null,
    community: d.community || 'public', snmp_disabled: !!d.snmp_disabled,
    snmp_version: d.snmp_version || 'v2c',
    v3_sec_name: d.v3_sec_name || '', v3_sec_level: d.v3_sec_level || '',
    v3_auth_proto: d.v3_auth_proto || '', v3_auth_pass: d.v3_auth_pass || '',
    v3_priv_proto: d.v3_priv_proto || '', v3_priv_pass: d.v3_priv_pass || '',
    monitoring_disabled: !d.monitoring_disabled,
  };
  try {
    await api('/api/devices/' + currentDevice, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    openDevice(currentDevice);
    updateLiveSummary();
  } catch (err) { alert(err.message); }
});
$('#dev-del-btn') && ($('#dev-del-btn').onclick = async () => {
  if (!currentDevice) return;
  if (!confirm('Delete this device and its history? This cannot be undone.')) return;
  try {
    await api('/api/devices/' + currentDevice, { method: 'DELETE' });
    currentDevice = null;
    show('devices');
    loadDevices();
    updateLiveSummary();
  } catch (err) { alert(err.message); }
});
$('#edit-form').onsubmit = async e => {
  e.preventDefault();
  const f = new FormData(e.target);
  const body = { name: f.get('name'), ip: f.get('ip'), type: f.get('type'), snmp_disabled: !!f.get('snmp_disabled'), monitoring_disabled: !!f.get('monitoring_disabled'), ...snmpFieldsFromForm(f) };
  const g = f.get('group_id'); body.group_id = g ? +g : null;
  try {
    await api('/api/devices/' + currentDevice, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    $('#edit-panel').classList.add('hidden');
    openDevice(currentDevice);
  } catch (err) { alert(err.message); }
};
$('#monitor-form').onsubmit = async e => {
  e.preventDefault();
  const f = new FormData(e.target);
  const name = f.get('name');
  const body = {
    kind: 'service', name,
    port: f.get('port') ? +f.get('port') : (MONITOR_PORTS[name] || 0),
    target: f.get('target') || '',
    interval_sec: f.get('interval_sec') ? +f.get('interval_sec') : 0,
    timeout_sec: f.get('timeout_sec') ? +f.get('timeout_sec') : 0,
    retries: f.get('retries') ? +f.get('retries') : 0,
  };
  try { await post(`/api/devices/${currentDevice}/monitors`, body); uiTick(true); e.target.reset(); openDevice(currentDevice); }
  catch (err) { uiTick(false); alert(err.message); }
};
async function refreshDevice() {
  if (!currentDevice) return;
  const ifs = await api(`/api/devices/${currentDevice}/interfaces`);
  $('#if-table tbody').innerHTML = ifs.map(i => `<tr data-if="${i.ifindex}" class="${i.ifindex === currentIf ? 'sel' : ''}">
    <td>${i.ifindex}</td><td>${esc(i.name)}</td><td>${i.oper_status === 'up' ? '🟢 up' : '🔴 ' + esc(i.oper_status)}</td>
    <td>${fmtSpeed(i.speed_bps)}</td><td>${fmtBps(i.in_bps)}</td><td>${fmtBps(i.out_bps)}</td></tr>`).join('')
    || '<tr><td colspan="6" class="muted">No interfaces yet — wait for the first SNMP poll</td></tr>';
  document.querySelectorAll('[data-if]').forEach(tr => tr.onclick = () => { currentIf = +tr.dataset.if; refreshDevice(); });
  if (currentIf == null && ifs.length) currentIf = ifs[0].ifindex;

  const setPing = makeChart($('#chart-ping'), [{ label: 'ms', stroke: '#f5a623', fill: 'rgba(245,166,35,.15)', width: 2 }], msFmt);
  loadGraph('ping_ms', null, (series, ts) => setPing([ts, ...series])).catch(() => {});
  const setCpu = makeChart($('#chart-cpu'), [{ label: 'CPU %', stroke: '#e74c3c', fill: 'rgba(231,76,60,.12)', width: 2 }], pctFmt);
  loadGraph('cpu_pct', null, (series, ts) => setCpu([ts, ...series])).catch(() => {});
  const setMem = makeChart($('#chart-mem'), [{ label: 'Mem %', stroke: '#9b59b6', fill: 'rgba(155,89,182,.12)', width: 2 }], pctFmt);
  loadGraph('mem_pct', null, (series, ts) => setMem([ts, ...series])).catch(() => {});
  if (currentIf != null) {
    $('#bw-title').textContent = 'Bandwidth — interface ' + currentIf + ' (MRTG style: green = In, blue = Out)';
    const setBw = makeChart($('#chart-bw'), [
      { label: 'In', stroke: '#00a651', fill: 'rgba(0,166,81,.25)', width: 2 },
      { label: 'Out', stroke: '#2471a3', fill: 'rgba(36,113,163,.18)', width: 2 },
    ], bpsFmtAxis);
    loadGraph('bandwidth', null, (series, ts) => setBw([ts, ...series]), `&ifindex=${currentIf}`).catch(() => {});
  }
}
document.querySelectorAll('.rbtn').forEach(b => b.onclick = () => {
  currentRange = b.dataset.range;
  document.querySelectorAll('.rbtn').forEach(x => x.classList.toggle('active', x === b));
  refreshDevice();
});
$('#poll-now').onclick = async () => { await post(`/api/devices/${currentDevice}/poll`, {}); setTimeout(refreshDevice, 4000); };

// ---------- discovery ----------
$('#discover-form').onsubmit = async e => {
  e.preventDefault();
  const f = new FormData(e.target);
  $('#discover-status').textContent = 'Scanning… (a /24 takes ~20–40 seconds)';
  $('#found-table tbody').innerHTML = '';
  try {
    const found = await post('/api/discover', { cidr: f.get('cidr'), community: f.get('community') || 'public' });
    $('#discover-status').textContent = `Found ${found.length} live hosts`;
    $('#found-table tbody').innerHTML = found.map(h => `<tr>
      <td>${esc(h.ip)}</td><td>${h.latency_ms >= 0 ? h.latency_ms.toFixed(1) + ' ms' : '–'}</td>
      <td>${h.snmp ? '✅' : '—'}</td><td>${esc(h.sys_name)}</td><td class="muted">${esc((h.sys_descr || '').slice(0, 80))}</td>
      <td>${IS_ADMIN() ? `<button data-add="${esc(h.ip)}" data-name="${esc(h.sys_name || h.ip)}">Add</button>` : ''}</td></tr>`).join('');
    document.querySelectorAll('[data-add]').forEach(b => b.onclick = async () => {
      await post('/api/devices', { ip: b.dataset.add, name: b.dataset.name, community: f.get('community') || 'public' });
      b.textContent = 'Added ✓'; b.disabled = true;
    });
  } catch (err) { $('#discover-status').textContent = 'Error: ' + err.message; }
};

// ---------- maps / diagrams (imported from the legacy system) ----------
// The viewer renders each imported map exactly as the legacy system drew
// it: region squares in their stored colours, the drawn lines, the
// region captions, and every device as its device-type icon at its
// stored position. Lines are coloured by the live status of the
// devices their endpoints resolve to (green flowing = up, red pulsing
// = an end is down, the stored the legacy system colour = not resolved).
//
// On top of the faithful drawing, devices are presented the way
// the legacy system presents them (its map-view conventions): each device
// sits on a status marker — a green diamond plate while up, a red
// diamond plate with a white × badge while down so an outage is
// visible at a glance, an amber diamond while its state is not
// known yet — with its bold name and a
// small type/address caption beneath, on a light canvas (dark is
// available), with a device-group tree on the left and a hover card
// showing name, address, type, status and last change.
const LINE_UP = '#2fd06c', LINE_DOWN = '#ff5347';

// ---- status markers (shared by Maps, Topology and the device page)
// Markers are drawn at a FIXED on-screen size (v3.2.1): the plate is
// 44 screen px across and the device glyph 26 px, whatever the zoom.
// The Maps and Topology scenes counter-scale each device's marker
// group by 1/zoom around the device anchor — the standard NMS/GIS
// fixed-symbol technique — so zooming changes how much of the
// drawing is visible, never how big the symbols are. (the legacy system
// scales its icons with the drawing; at fit-to-view its ~75-unit
// devices collide in dense columns. A fixed 44 px plate is smaller
// than the tightest real device spacing — about 68 units in the
// densest columns of a large imported map — so neighbours stay
// clear of each other.)
const DEV_PLATE_R = 22;   // marker plate radius, screen px (44 px plate)
const DEV_GLYPH_PX = 34;  // glyph box, screen px (the artwork fills
                          // roughly two-thirds of its box, so the
                          // drawn glyph reads ~26 px across)
function diamondPoints(r) { return `0,${(-r).toFixed(1)} ${r.toFixed(1)},0 0,${r.toFixed(1)} ${(-r).toFixed(1)},0`; }
// downBadgeSVG is the bold white cross on a dark red disc that sits
// on the down plate's top-right corner (the classic NMS treatment
// chosen for NetGold's maps in v3.3) — the outage reads at a glance
// without hiding the device pictogram under a spiky halo.
function downBadgeSVG() {
  return `<g class="dev-badge" transform="translate(16,-16)"><circle r="7" fill="#8f1d17" stroke="#ffffff" stroke-width="1.5"/><path d="M-2.8 -2.8 L2.8 2.8 M2.8 -2.8 L-2.8 2.8" stroke="#fff" stroke-width="2.1" stroke-linecap="round"/></g>`;
}
// markerSVG(status) returns the status plate centred on (0,0) at
// the standard fixed size: one small crisp diamond plate for every
// state — green while up, red with a white × badge while down,
// amber while the state is not known yet, muted grey while the
// device is in maintenance — with the device pictogram on top.
function markerSVG(status) {
  if (status === 'down') {
    return `<polygon class="dev-marker marker-down" points="${diamondPoints(DEV_PLATE_R)}" fill="url(#ng-grad-down)" stroke="rgba(20,30,40,.25)" stroke-width="1" filter="url(#ng-marker-shadow)"/>${downBadgeSVG()}`;
  }
  if (status === 'up') {
    return `<polygon class="dev-marker marker-up" points="${diamondPoints(DEV_PLATE_R)}" fill="url(#ng-grad-up)" stroke="rgba(20,30,40,.25)" stroke-width="1" filter="url(#ng-marker-shadow)"/>`;
  }
  if (status === 'maintenance') {
    return `<polygon class="dev-marker marker-maint" points="${diamondPoints(DEV_PLATE_R)}" fill="url(#ng-grad-maint)" stroke="rgba(20,30,40,.25)" stroke-width="1" filter="url(#ng-marker-shadow)"/>`;
  }
  return `<polygon class="dev-marker marker-unknown" points="${diamondPoints(DEV_PLATE_R)}" fill="url(#ng-grad-unknown)" stroke="rgba(20,30,40,.25)" stroke-width="1" filter="url(#ng-marker-shadow)"/>`;
}
// markerChip is the standalone marker + glyph used on the device
// detail page header.
function markerChip(status, iconId, px) {
  const s = px || 64;
  return `<svg width="${s}" height="${s}" viewBox="-30 -30 60 60" aria-hidden="true">${markerSVG(status)}${iconUseIn(iconId, 30)}</svg>`;
}
// ---- zoom-aware compact labels (v3.2.2) ----
// Below the scale LBL_COMPACT_K the full label pill (name + caption)
// is wider than the space between neighbouring devices on dense
// maps, so pills run into each other and the text turns to mush.
// Scenes therefore carry two label variants per device — the full
// pill and a compact one (name only, one line, 11 px, 20 px tall) —
// and the scene root wears the .labels-compact class whenever the
// current scale is below LBL_COMPACT_K; CSS shows the matching
// variant. Zooming in past the threshold brings the full labels
// back.
//
// v3.6: labels are NEVER truncated, anywhere. fitText is a pass-
// through kept for its call sites; pill widths are always the
// full measured text widths (resolveLabelWidths no longer shrinks
// anything), label layout works by placement (the slot planner)
// and, in Tidy View, by grid cells reserved from the full widths.
// Text widths are measured with a canvas in the exact label fonts,
// so a pill is never narrower than the text it carries.
const LBL_COMPACT_K = 0.8;    // scale below which labels go compact
const LBL_MIN_W = 34;         // narrowest pill worth drawing, px
// Fallback per-character width, used only if canvas measurement
// is unavailable. Measured against the rendered label font (11 px
// semibold): capital-heavy device names average about 7.3 px per
// character.
const LBL_COMPACT_CHAR_W = 7.3;
const LBL_GAP = 6;            // clear px kept between neighbouring pills
const LBL_COMPACT_REACH = 20; // compact pill height (bottom 47 - top 27)
// The label fonts, exactly as style.css sets them (the name at
// 12.5 px semibold on maps; the topology name at 12 px measures a
// touch narrower, so the map metrics are the safe superset).
const LBL_FONT_STACK = '"Segoe UI", Roboto, "Helvetica Neue", Arial, system-ui, sans-serif';
const LBL_FONT_NAME = '600 12.5px ' + LBL_FONT_STACK;
const LBL_FONT_CAP = '600 10px ' + LBL_FONT_STACK; // captions render at 10px (maps) and 9.5px semibold (topology); this measures the wider of the two
const LBL_FONT_COMPACT = '600 11px ' + LBL_FONT_STACK;
const _lblMeasureCtx = document.createElement('canvas').getContext('2d');
// measureLabelText is the rendered width of text in font, in px.
function measureLabelText(text, font) {
  if (!text) return 0;
  try {
    _lblMeasureCtx.font = font;
    return _lblMeasureCtx.measureText(text).width;
  } catch (e) {
    return text.length * LBL_COMPACT_CHAR_W;
  }
}
// fitText historically truncated text with an ellipsis to fit a
// width budget. Since v3.6 names are never cut: it returns the
// text unchanged (the signature stays for its call sites).
function fitText(text, budget, charW, pad) {
  return text;
}
// labelNaturalW is the full pill's width: the widest of its name
// lines and its caption, measured in the label fonts, plus the
// pill's horizontal padding.
function labelNaturalW(lines, caption) {
  const nameW = Math.max.apply(null, lines.map(l => measureLabelText(l, LBL_FONT_NAME)));
  const capW = caption ? measureLabelText(caption, LBL_FONT_CAP) : 0;
  return Math.ceil(Math.max(nameW, capW)) + 14;
}
// labelReach is how far below the marker centre the full pill
// extends (its bottom minus its top at y=27), in screen px: only a
// neighbour whose centre is closer than this can touch the pill.
function labelReach(lines, caption) {
  const nameLast = 41 + (lines.length - 1) * 14;
  return (caption ? nameLast + 13 : nameLast) + 5 - 27;
}
// resolveLabelWidths historically shrank pill widths so no two
// pills could overlap sideways — at the price of truncating the
// names inside them. Since v3.6 names are never cut, so widths are
// never shrunk: every pill keeps its full natural width and the
// layout separates labels by placement instead (the slot planner
// in faithful view, the reserved grid cells in Tidy View). The
// function stays as the single place budgets are assigned, so the
// renderer's width plumbing is unchanged.
function resolveLabelWidths(pts, k, naturals, reaches) {
  return naturals.slice();
}
// planLabelStagger decides which compact pills are nudged one
// pill-height lower (v3.2.2). A few device pairs sit so close
// (~32 screen px, same row) that even minimum-width pills cannot
// fit side by side; staggering one of them separates the pair
// vertically. A pill is staggered only when doing so strictly
// reduces the total number of colliding pairs, so the result is
// never worse than no stagger, and maps that are already clear
// are left untouched.
function planLabelStagger(pts, k, widths) {
  const stag = pts.map(() => false);
  if (!(k > 0) || !isFinite(k) || pts.length < 2) return stag;
  const sx = pts.map(p => p.x * k), sy = pts.map(p => p.y * k);
  const violates = (i, j) => {
    const aTop = sy[i] + (stag[i] ? 51 : 27), bTop = sy[j] + (stag[j] ? 51 : 27);
    if (Math.min(aTop + 20, bTop + 20) - Math.max(aTop, bTop) <= 0.5) return false;
    return (widths[i] + widths[j]) / 2 - Math.abs(sx[i] - sx[j]) > 0.5;
  };
  const total = () => {
    let n = 0;
    for (let i = 0; i < pts.length; i++) for (let j = i + 1; j < pts.length; j++) if (violates(i, j)) n++;
    return n;
  };
  for (let pass = 0; pass < 4; pass++) {
    let improved = false;
    for (let i = 0; i < pts.length; i++) {
      for (let j = i + 1; j < pts.length; j++) {
        if (!violates(i, j)) continue;
        for (const cand of [j, i]) {
          if (stag[cand]) continue;
          const before = total();
          stag[cand] = true;
          if (total() < before) { improved = true; break; }
          stag[cand] = false;
        }
      }
    }
    if (!improved) break;
  }
  return stag;
}
// ---- smart label placement (v3.4.2) ----
// Until now every label hung below its marker. On the imported
// drawings that breaks in two visible ways: a device sitting low
// in its region box has its pill drawn across (or below) the
// region's border line, and in tight clusters the below-pills of
// neighbours still crowd each other. Labels therefore get a slot
// planner: for each device, and for each variant (full / compact),
// the planner tries four slots around the marker — below (the
// classic spot), above, right, left — plus the staggered below of
// planLabelStagger for compact pills, and picks the placement
// that best honours, in this order:
//   1. the pill lies fully inside the device's own region (the
//      smallest drawn square holding the device's stored
//      position; devices in no region are only canvas-clamped);
//   2. it overlaps no label already placed;
//   3. it overlaps no other device's marker;
//   4. it stays inside the canvas;
//   5. slot preference: below, staggered below, above, right, left.
// The pill may also slide along its slot (horizontally for
// below/above, vertically for right/left) to fit its region.
// Placements are computed per variant at that variant's design
// scale — the same scales the width budgets use — so what the
// planner checks is what the screen shows at that scale. Stored
// positions are never touched: this is purely which side of the
// marker the label is drawn on.
const LBL_SLOT_GAP = 27;      // pill starts this far from the anchor (plate bottom edge is 22)
const LBL_MARK_HALF = 25;     // marker collision half-extent, px (plate 22 + badge spill)
const LBL_REGION_MARGIN = 3;  // clear px kept inside the region border
// Tidy footprints (v3.4.3): the rectangle the tidy layout reserves
// around each device, computed from the label variant the render
// scale will show (the renderer switches to compact labels below
// LBL_COMPACT_K; computeMapTidy solves the render scale from the
// layout result and reserves the variant that scale implies). fw covers
// the label's full
// width plus headroom; fh covers the marker plus the label stack
// hanging below it (labels stay below their markers in the tidy
// arrangement — the region boxes are drawn with room for them —
// and the planner verifies every other slot against the real
// geometry). ext is how far the label stack reaches from the
// marker centre, used for the region-box padding.
const TIDY_FOOT_PAD_X = 12;   // extra footprint width, screen px
const TIDY_FOOT_PAD_Y = 40;   // marker clearance + gap below the stack, screen px
function labelFootprintPx(lines, caption, rawName, compact) {
  if (compact) {
    const ext = LBL_SLOT_GAP + LBL_COMPACT_REACH;
    return {
      fw: labelPillCompactW(rawName, Infinity) + TIDY_FOOT_PAD_X,
      fh: ext + TIDY_FOOT_PAD_Y,
      ext,
    };
  }
  const ext = LBL_SLOT_GAP + labelReach(lines, caption);
  return {
    fw: labelNaturalW(lines, caption) + TIDY_FOOT_PAD_X,
    fh: ext + TIDY_FOOT_PAD_Y,
    ext,
  };
}
// labelPillW is the full pill's rendered width — the same math
// labelPill renders with, factored out so the placement planner
// and the tidy layout see the true width. The budget argument is
// accepted for call-site compatibility and ignored: since v3.6 a
// pill is always as wide as its full text.
function labelPillW(lines, caption, budget) {
  return labelNaturalW(lines, caption);
}
// labelPillCompactW is the compact pill's rendered width: the
// full name on one line, measured in the compact font.
function labelPillCompactW(name, budget) {
  return Math.ceil(measureLabelText(String(name || ''), LBL_FONT_COMPACT)) + 10;
}
// lblSlotRect is a slot's pill rectangle relative to the device
// anchor, in screen px (y grows downward, as in SVG). A staggered
// pill hangs 24 px further from the marker than its slot's normal
// position (the v3.2.2 stagger, available to every slot as a
// repair option).
function lblSlotRect(slot, w, h, stag) {
  const g = LBL_SLOT_GAP;
  const dy = stag ? 24 : 0;
  if (slot === 'above') return { x0: -w / 2, y0: -g - dy - h, x1: w / 2, y1: -g - dy };
  if (slot === 'right') return { x0: g, y0: -h / 2, x1: g + w, y1: h / 2 };
  if (slot === 'left') return { x0: -g - w, y0: -h / 2, x1: -g, y1: h / 2 };
  return { x0: -w / 2, y0: g + dy, x1: w / 2, y1: g + dy + h };
}
// planLabelSlots chooses a placement per label. items carry the
// anchor (ax, ay, scene units), the variant's pill width/height
// (screen px), whether the staggered below is a candidate (stag)
// and the device's region rect in scene units (or null). k is the
// design scale (screen px per scene unit); canvas is the scene
// rect. Returns [{slot, dx, dy, stag}] — dx/dy are the along-slot
// shift in px (only one of them is ever non-zero).
//
// The planner is a repair pass over the classic arrangement, not a
// free-for-all: every label starts in its v3.2.2 spot (below the
// marker, staggered when the stagger plan says so, slid inside its
// region and the canvas), which the width budgets already keep
// clear of other labels. The planner then measures four violation
// areas per label — outside its region, over another label, over
// another marker, outside the canvas — and moves a label to
// another slot (below / above / right / left, with its fit shifts)
// only when the move strictly reduces the scene's total violation
// tuple, compared in that priority order. Passes repeat (worst
// labels first) until nothing improves, so the result is never
// worse than the classic arrangement on any of the four measures,
// and a label only leaves the classic spot when the classic spot
// actually violates something. Fully deterministic.
//
// Two v3.4.3 additions:
// - A final polish pass (default mode) keeps pushing marker
//   overlap down: a move is accepted when it strictly reduces the
//   marker total while the region, pill and canvas totals do not
//   grow — marker contact is squeezed as far as the higher
//   priorities allow, never at their expense.
// - Hard mode (opts.hard, used by the Tidy View, whose layout
//   reserves every label's footprint): the priority order becomes
//   region, marker, pill, canvas, and a label that HAS a fully
//   clean candidate (no region, marker or canvas violation — all
//   three are properties of the candidate alone, since markers
//   and regions do not move) is restricted to clean candidates
//   for good. The classic below spot stays home unless it is
//   itself unclean — which is exactly how a bottom-row label,
//   whose below pill would cross its region's border, ends up
//   flipped above its marker.
function planLabelSlots(items, k, canvas, opts) {
  const hard = !!(opts && opts.hard);
  const out = items.map(() => ({ slot: 'below', dx: 0, dy: 0, stag: false }));
  if (!(k > 0) || !isFinite(k) || !items.length) return out;
  const M = hard ? LBL_MARK_HALF + 2 : LBL_MARK_HALF;
  const I_REG = 0, I_PILL = hard ? 2 : 1, I_MARK = hard ? 1 : 2, I_CVS = 3;
  const marks = items.map(it => ({ x0: it.ax * k - M, y0: it.ay * k - M, x1: it.ax * k + M, y1: it.ay * k + M }));
  const cvs = { x0: canvas.x0 * k, y0: canvas.y0 * k, x1: canvas.x1 * k, y1: canvas.y1 * k };
  const area = r => Math.max(0, r.x1 - r.x0) * Math.max(0, r.y1 - r.y0);
  const ovArea = (a, b) => {
    const ox = Math.min(a.x1, b.x1) - Math.max(a.x0, b.x0);
    const oy = Math.min(a.y1, b.y1) - Math.max(a.y0, b.y0);
    return (ox > 0 && oy > 0) ? ox * oy : 0;
  };
  const lexLess = (a, b) => {
    for (let i = 0; i < a.length; i++) { if (a[i] !== b[i]) return a[i] < b[i]; }
    return false;
  };
  const lexCmp = (a, b) => {
    for (let i = 0; i < a.length; i++) { if (a[i] !== b[i]) return a[i] < b[i] ? -1 : 1; }
    return 0;
  };
  // Slide along a slot's free axis so the pill fits a box: the
  // shift that satisfies the box, preferring no shift at all; when
  // the pill is larger than the box, centre the overflow.
  const shiftInto = (base, ax, ay, horiz, box, m) => {
    const lo = horiz ? box.x0 + m - (base.x0 + ax) : box.y0 + m - (base.y0 + ay);
    const hi = horiz ? box.x1 - m - (base.x1 + ax) : box.y1 - m - (base.y1 + ay);
    return lo <= hi ? Math.min(Math.max(0, lo), hi) : (lo + hi) / 2;
  };
  // Candidate placements for one item: every slot, each with the
  // unshifted position, the region-fit shift and the region+canvas
  // fit shift (deduplicated). The classic starting candidate is
  // flagged so the search can begin from it.
  const candCache = items.map(it => {
    const ax = it.ax * k, ay = it.ay * k;
    const reg = it.region ? { x0: it.region.x0 * k, y0: it.region.y0 * k, x1: it.region.x1 * k, y1: it.region.y1 * k } : null;
    const names = ['below', 'below-stag', 'above', 'above-stag', 'right', 'left'];
    const list = [];
    const seen = new Set();
    for (const nm of names) {
      const stag = nm.endsWith('-stag');
      const slot = stag ? nm.slice(0, -5) : nm;
      const base = lblSlotRect(slot, it.w, it.h, stag);
      const horiz = slot === 'below' || slot === 'above';
      let sReg = 0;
      if (reg) sReg = shiftInto(base, ax, ay, horiz, reg, LBL_REGION_MARGIN);
      let sFull = sReg;
      {
        const r = horiz ? { x0: base.x0 + ax + sReg, x1: base.x1 + ax + sReg, y0: base.y0 + ay, y1: base.y1 + ay }
                        : { x0: base.x0 + ax, x1: base.x1 + ax, y0: base.y0 + ay + sReg, y1: base.y1 + ay + sReg };
        sFull = sReg + shiftInto({ x0: r.x0 - ax, x1: r.x1 - ax, y0: r.y0 - ay, y1: r.y1 - ay }, ax, ay, horiz, cvs, 0);
      }
      for (const s of [0, sReg, sFull]) {
        const key = nm + '|' + s.toFixed(1);
        if (seen.has(key)) continue;
        seen.add(key);
        const rect = horiz
          ? { x0: base.x0 + ax + s, y0: base.y0 + ay, x1: base.x1 + ax + s, y1: base.y1 + ay }
          : { x0: base.x0 + ax, y0: base.y0 + ay + s, x1: base.x1 + ax, y1: base.y1 + ay + s };
        list.push({
          slot, stag, dx: horiz ? s : 0, dy: horiz ? 0 : s, rect, reg,
          home: nm === (it.stag ? 'below-stag' : 'below') && s === sFull,
        });
      }
    }
    return list;
  });
  // Dodge candidates, generated against the CURRENT arrangement:
  // when every fitted slot of a label is blocked, sliding the pill
  // along its slot until it just clears the blocking label or
  // marker often opens a clean spot the fixed shifts cannot reach.
  // For each slot, every nearby obstacle contributes the two
  // shifts that would put the pill just beside it.
  const dodgeFor = (i, it) => {
    const ax = it.ax * k, ay = it.ay * k;
    const reg = candCache[i].length ? candCache[i][0].reg : null;
    const names = ['below', 'below-stag', 'above', 'above-stag', 'right', 'left'];
    const obs = [];
    for (let j = 0; j < rects.length; j++) { if (j !== i && rects[j]) obs.push(rects[j]); }
    for (let j = 0; j < marks.length; j++) { if (j !== i) obs.push(marks[j]); }
    const out = [];
    for (const nm of names) {
      const stag = nm.endsWith('-stag');
      const slot = stag ? nm.slice(0, -5) : nm;
      const base = lblSlotRect(slot, it.w, it.h, stag);
      const horiz = slot === 'below' || slot === 'above';
      const r0 = { x0: base.x0 + ax, y0: base.y0 + ay, x1: base.x1 + ax, y1: base.y1 + ay };
      const shifts = new Set();
      for (const o of obs) {
        if (horiz) {
          if (o.y1 <= r0.y0 || o.y0 >= r0.y1) continue; // no shared band
          if (o.x1 < r0.x0 - it.w - 4 || o.x0 > r0.x1 + it.w + 4) continue; // out of reach
          shifts.add(o.x1 - r0.x0 + 2);
          shifts.add(o.x0 - r0.x1 - 2);
        } else {
          if (o.x1 <= r0.x0 || o.x0 >= r0.x1) continue;
          if (o.y1 < r0.y0 - it.h - 4 || o.y0 > r0.y1 + it.h + 4) continue;
          shifts.add(o.y1 - r0.y0 + 2);
          shifts.add(o.y0 - r0.y1 - 2);
        }
      }
      for (const s of shifts) {
        if (Math.abs(s) > 600 || s === 0) continue;
        const rect = horiz
          ? { x0: r0.x0 + s, y0: r0.y0, x1: r0.x1 + s, y1: r0.y1 }
          : { x0: r0.x0, y0: r0.y0 + s, x1: r0.x1, y1: r0.y1 + s };
        out.push({ slot, stag, dx: horiz ? s : 0, dy: horiz ? 0 : s, rect, reg, home: false });
      }
    }
    return out;
  };
  const rects = new Array(items.length).fill(null);
  const cur = new Array(items.length).fill(null);
  const costOf = (i, rect) => {
    let pillOv = 0;
    for (let j = 0; j < rects.length; j++) {
      if (j !== i && rects[j]) pillOv += ovArea(rect, rects[j]);
    }
    let markOv = 0;
    for (let j = 0; j < marks.length; j++) {
      if (j !== i) markOv += ovArea(rect, marks[j]);
    }
    const reg = candCache[i].length ? candCache[i][0].reg : null;
    const regOv = reg ? area(rect) - ovArea(rect, reg) : 0;
    const cvsOv = area(rect) - ovArea(rect, cvs);
    return hard ? [regOv, markOv, pillOv, cvsOv] : [regOv, pillOv, markOv, cvsOv];
  };
  // Hard mode (v3.4.3): the region, marker and canvas parts of a
  // candidate are static — they depend only on the candidate rect,
  // never on the other labels — so "this label has a completely
  // clean placement" can be decided per label up front. Levels:
  // 2 = a candidate clean on region+marker+canvas exists, 1 = one
  // clean on region+marker exists, 0 = neither (no restriction).
  const staticParts = (i, rect) => {
    let markOv = 0;
    for (let j = 0; j < marks.length; j++) {
      if (j !== i) markOv += ovArea(rect, marks[j]);
    }
    const reg = candCache[i].length ? candCache[i][0].reg : null;
    return {
      reg: reg ? area(rect) - ovArea(rect, reg) : 0,
      mark: markOv,
      cvs: area(rect) - ovArea(rect, cvs),
    };
  };
  const cleanAt = (i, c, level) => {
    const sp = staticParts(i, c.rect);
    if (sp.reg !== 0 || sp.mark !== 0) return false;
    return level < 2 || sp.cvs === 0;
  };
  const levels = items.map((it, i) => {
    if (!hard) return 0;
    if (candCache[i].some(c => cleanAt(i, c, 2))) return 2;
    if (candCache[i].some(c => cleanAt(i, c, 1))) return 1;
    return 0;
  });
  const poolFor = (i, extra) => {
    const base = extra ? candCache[i].concat(extra) : candCache[i];
    if (!hard || levels[i] === 0) return base;
    return base.filter(c => cleanAt(i, c, levels[i]));
  };
  // Start from the classic arrangement (in hard mode: from the
  // most classic CLEAN candidate when home itself is unclean).
  items.forEach((it, i) => {
    let home = candCache[i].find(c => c.home) || candCache[i][0];
    if (hard && levels[i] > 0 && !cleanAt(i, home, levels[i])) {
      home = candCache[i].find(c => cleanAt(i, c, levels[i])) || home;
    }
    cur[i] = home;
    rects[i] = home.rect;
  });
  let costs = items.map((it, i) => costOf(i, rects[i]));
  const totalOf = cs => cs.reduce((t, c) => [t[0] + c[0], t[1] + c[1], t[2] + c[2], t[3] + c[3]], [0, 0, 0, 0]);
  // Note: the pill-overlap component of the total counts every
  // pair twice (once from each side) — consistent for comparisons.
  let total = totalOf(costs);
  for (let pass = 0; pass < 16; pass++) {
    let moved = false;
    const order = items.map((_, i) => i)
      .sort((a, b) => lexCmp(costs[b], costs[a]) || a - b);
    for (const i of order) {
      if (costs[i][0] === 0 && costs[i][1] === 0 && costs[i][2] === 0 && costs[i][3] === 0) continue;
      let bestC = null, bestTotal = total;
      for (const c of poolFor(i, dodgeFor(i, items[i]))) {
        if (c === cur[i]) continue;
        const cc = costOf(i, c.rect);
        const nt = [
          total[0] - costs[i][0] + cc[0],
          total[1] - costs[i][1] + cc[1],
          total[2] - costs[i][2] + cc[2],
          total[3] - costs[i][3] + cc[3],
        ];
        if (lexLess(nt, bestTotal)) { bestTotal = nt; bestC = c; }
      }
      if (bestC) {
        cur[i] = bestC;
        rects[i] = bestC.rect;
        moved = true;
        // A moved label changes its neighbours' overlap costs;
        // resync everything so the next decision (and the totals
        // it is judged against) is exact, not one move stale.
        costs = items.map((it, j) => costOf(j, rects[j]));
        total = totalOf(costs);
      }
    }
    if (!moved) break;
  }
  // Joint pair repair: a wedged pair can block every single-label
  // move — each label's escape is covered by the other's pill. For
  // every pair that still overlaps, move BOTH labels through
  // their candidate sets together and keep the best joint result
  // that strictly improves the scene total. The total for a
  // combination is evaluated exactly in O(n): only the two moved
  // labels' own costs and their overlaps with third parties can
  // change.
  for (let round = 0; round < 6; round++) {
    let improved = false;
    for (let i = 0; i < items.length; i++) {
      for (let j = i + 1; j < items.length; j++) {
        if (ovArea(rects[i], rects[j]) <= 0) continue;
        const oldI = rects[i], oldJ = rects[j];
        const oldCi = costOf(i, oldI), oldCj = costOf(j, oldJ);
        const candI = poolFor(i, dodgeFor(i, items[i]));
        const candJ = poolFor(j, dodgeFor(j, items[j]));
        let bestPair = null, bestTotal = total;
        for (const ci of candI) {
          for (const cj of candJ) {
            if (ci === cur[i] && cj === cur[j]) continue;
            rects[i] = ci.rect;
            rects[j] = cj.rect;
            const ni = costOf(i, ci.rect), nj = costOf(j, cj.rect);
            let extra = 0;
            for (let x = 0; x < items.length; x++) {
              if (x === i || x === j || !rects[x]) continue;
              extra += ovArea(ci.rect, rects[x]) + ovArea(cj.rect, rects[x])
                     - ovArea(oldI, rects[x]) - ovArea(oldJ, rects[x]);
            }
            const t = total.map((tv, x) => tv - oldCi[x] - oldCj[x] + ni[x] + nj[x]);
            t[I_PILL] += extra;
            if (lexLess(t, bestTotal)) { bestTotal = t; bestPair = [ci, cj]; }
          }
        }
        rects[i] = oldI;
        rects[j] = oldJ;
        if (bestPair) {
          cur[i] = bestPair[0];
          cur[j] = bestPair[1];
          rects[i] = bestPair[0].rect;
          rects[j] = bestPair[1].rect;
          costs = items.map((it, x) => costOf(x, rects[x]));
          total = totalOf(costs);
          improved = true;
        }
      }
    }
    if (!improved) break;
  }
  // Marker polish (v3.4.3, default mode): with region and pill
  // totals settled, squeeze marker contact further. The measure
  // that matters on screen is how many markers a label touches,
  // so a move is accepted when it strictly reduces this label's
  // marker-touch count — or keeps the count and strictly reduces
  // the touched area — while the region, pill and canvas totals
  // do not grow and the label gains no new pill neighbour. The
  // polish can never undo the higher priorities, in counts or
  // in areas.
  if (!hard) {
    const markCountOf = (i, rect) => {
      let n = 0;
      for (let j = 0; j < marks.length; j++) {
        if (j !== i && ovArea(rect, marks[j]) > 0) n++;
      }
      return n;
    };
    const pillCountOf = (i, rect) => {
      let n = 0;
      for (let j = 0; j < rects.length; j++) {
        if (j !== i && rects[j] && ovArea(rect, rects[j]) > 0) n++;
      }
      return n;
    };
    for (let pass = 0; pass < 8; pass++) {
      let moved = false;
      for (let i = 0; i < items.length; i++) {
        if (costs[i][I_MARK] === 0) continue;
        const base = total;
        const curMC = markCountOf(i, rects[i]);
        const curPC = pillCountOf(i, rects[i]);
        let bestC = null, bestKey = null;
        for (const c of candCache[i].concat(dodgeFor(i, items[i]))) {
          if (c === cur[i]) continue;
          const cc = costOf(i, c.rect);
          const nt = base.map((t, x) => t - costs[i][x] + cc[x]);
          if (nt[I_REG] > base[I_REG] || nt[I_PILL] > base[I_PILL] || nt[I_CVS] > base[I_CVS]) continue;
          const mc = markCountOf(i, c.rect);
          if (mc > curMC) continue;
          if (mc === curMC && nt[I_MARK] >= base[I_MARK]) continue;
          if (pillCountOf(i, c.rect) > curPC) continue;
          const key = [mc, nt[I_MARK]];
          if (!bestKey || lexLess(key, bestKey)) { bestKey = key; bestC = c; }
        }
        if (bestC) {
          cur[i] = bestC;
          rects[i] = bestC.rect;
          moved = true;
          costs = items.map((it, j) => costOf(j, rects[j]));
          total = totalOf(costs);
        }
      }
      if (!moved) break;
    }
  }
  // Pill polish (v3.4.3, hard mode): the mirror of the marker
  // polish above. With the region and marker totals settled, a
  // move is accepted when it strictly reduces this label's
  // pill-touch count — or keeps the count and strictly reduces
  // the touched area — while the region, marker and canvas
  // totals do not grow and the label gains no new marker
  // neighbour. It squeezes out the last band grazes the joint
  // search's strict lexicographic order leaves behind, without
  // ever trading a higher priority away.
  if (hard) {
    const touchCountOf = (i, rect, againstMarks) => {
      let n = 0;
      const arr = againstMarks ? marks : rects;
      for (let j = 0; j < arr.length; j++) {
        if (j !== i && arr[j] && ovArea(rect, arr[j]) > 0) n++;
      }
      return n;
    };
    for (let pass = 0; pass < 8; pass++) {
      let moved = false;
      for (let i = 0; i < items.length; i++) {
        if (costs[i][I_PILL] === 0) continue;
        const base = total;
        const curPC = touchCountOf(i, rects[i], false);
        const curMC = touchCountOf(i, rects[i], true);
        let bestC = null, bestKey = null;
        for (const c of candCache[i].concat(dodgeFor(i, items[i]))) {
          if (c === cur[i]) continue;
          const cc = costOf(i, c.rect);
          const nt = base.map((t, x) => t - costs[i][x] + cc[x]);
          if (nt[I_REG] > base[I_REG] || nt[I_MARK] > base[I_MARK] || nt[I_CVS] > base[I_CVS]) continue;
          const pc = touchCountOf(i, c.rect, false);
          if (pc > curPC) continue;
          if (pc === curPC && nt[I_PILL] >= base[I_PILL]) continue;
          if (touchCountOf(i, c.rect, true) > curMC) continue;
          const key = [pc, nt[I_PILL]];
          if (!bestKey || lexLess(key, bestKey)) { bestKey = key; bestC = c; }
        }
        if (bestC) {
          cur[i] = bestC;
          rects[i] = bestC.rect;
          moved = true;
          costs = items.map((it, j) => costOf(j, rects[j]));
          total = totalOf(costs);
        }
      }
      if (!moved) break;
    }
  }
  items.forEach((it, i) => {
    out[i] = { slot: cur[i].slot, dx: cur[i].dx, dy: cur[i].dy, stag: cur[i].stag };
  });
  return out;
}
// labelPill renders one full label variant at its planned slot:
// the device name (one or two lines) and an optional caption, on a
// soft pill background so the text stays readable over lines and
// region fills. Coordinates are screen px (the group is
// counter-scaled); below the marker the pill starts at y=27,
// exactly as it always has — the other slots mirror that geometry.
function labelPill(lines, caption, nameCls, capCls, budget, place) {
  const ls = lines; // full text, never truncated (v3.6)
  const cap = caption || '';
  const w = labelPillW(lines, caption, budget);
  const h = labelReach(lines, caption); // the pill's height (its reach below the plate)
  const slot = place && place.slot ? place.slot : 'below';
  let px, py, anchor, tx;
  if (slot === 'above') { px = -w / 2; py = -LBL_SLOT_GAP - (place && place.stag ? 24 : 0) - h; anchor = 'middle'; tx = 0; }
  else if (slot === 'right') { px = LBL_SLOT_GAP; py = -h / 2; anchor = 'start'; tx = px + 7; }
  else if (slot === 'left') { px = -LBL_SLOT_GAP - w; py = -h / 2; anchor = 'end'; tx = px + w - 7; }
  else { px = -w / 2; py = LBL_SLOT_GAP + (place && place.stag ? 24 : 0); anchor = 'middle'; tx = 0; }
  const dx = place && place.dx ? place.dx : 0, dy = place && place.dy ? place.dy : 0;
  let s = `<g class="lbl-place" transform="translate(${dx.toFixed(1)} ${dy.toFixed(1)})">`;
  s += `<rect class="lbl-pill" x="${px.toFixed(1)}" y="${py.toFixed(1)}" width="${w}" height="${h}" rx="7"/>`;
  ls.forEach((line, li) => {
    s += `<text class="${nameCls}" text-anchor="${anchor}" x="${tx.toFixed(1)}" y="${(py + 14 + li * 14).toFixed(1)}">${esc(line)}</text>`;
  });
  if (cap) s += `<text class="${capCls}" text-anchor="${anchor}" x="${tx.toFixed(1)}" y="${(py + 14 + (ls.length - 1) * 14 + 13).toFixed(1)}">${esc(cap)}</text>`;
  return s + '</g>';
}
// labelPillCompact renders the compact variant at its planned
// slot: the name only, on one line at 11 px on a 20 px pill —
// short enough that devices in a dense column (~46 px apart) keep
// clear of each other's labels. A staggered pill (see
// planLabelStagger) hangs 24 px lower in the below slot.
function labelPillCompact(name, nameCls, budget, place) {
  const t = fitText(name, budget, LBL_COMPACT_CHAR_W, 10);
  const w = labelPillCompactW(name, budget);
  const h = LBL_COMPACT_REACH;
  const slot = place && place.slot ? place.slot : 'below';
  let px, py, anchor, tx;
  if (slot === 'above') { px = -w / 2; py = -LBL_SLOT_GAP - (place && place.stag ? 24 : 0) - h; anchor = 'middle'; tx = 0; }
  else if (slot === 'right') { px = LBL_SLOT_GAP; py = -h / 2; anchor = 'start'; tx = px + 5; }
  else if (slot === 'left') { px = -LBL_SLOT_GAP - w; py = -h / 2; anchor = 'end'; tx = px + w - 5; }
  else { px = -w / 2; py = LBL_SLOT_GAP + (place && place.stag ? 24 : 0); anchor = 'middle'; tx = 0; }
  const dx = place && place.dx ? place.dx : 0, dy = place && place.dy ? place.dy : 0;
  return `<g class="lbl-place" transform="translate(${dx.toFixed(1)} ${dy.toFixed(1)})">` +
    `<rect class="lbl-pill" x="${px.toFixed(1)}" y="${py.toFixed(1)}" width="${w}" height="${h}" rx="6"/>` +
    `<text class="${nameCls} lbl-c-name" text-anchor="${anchor}" x="${tx.toFixed(1)}" y="${(py + 14).toFixed(1)}">${esc(t)}</text></g>`;
}
// labelStack emits both label variants of a device, each at its
// planned slot; CSS + the scene's .labels-compact class decide
// which one is visible.
function labelStack(name, lines, caption, nameCls, capCls, budgetFull, budgetCompact, placeFull, placeCompact) {
  return `<g class="lbl-full">${labelPill(lines, caption, nameCls, capCls, budgetFull, placeFull)}</g>` +
    `<g class="lbl-compact">${labelPillCompact(name, nameCls, budgetCompact, placeCompact)}</g>`;
}
// iconUseIn draws a sprite glyph centred on (0,0) as nested SVG.
function iconUseIn(iconId, s) {
  return `<svg x="${(-s / 2).toFixed(1)}" y="${(-s / 2).toFixed(1)}" width="${s}" height="${s}" viewBox="0 0 48 48"><use href="#ng-icon-${esc(iconId || 'generic')}"/></svg>`;
}

// ---- diagram canvas theme (light like the legacy system by default) ----
let canvasTheme = 'light';
try { canvasTheme = localStorage.getItem('netgold.canvasTheme') || 'light'; } catch (e) { }
if (canvasTheme !== 'dark') canvasTheme = 'light';
function applyCanvasTheme() {
  for (const sel of ['#map-wrap', '#topo-wrap']) {
    const el = $(sel);
    if (el) { el.classList.toggle('canvas-light', canvasTheme === 'light'); el.classList.toggle('canvas-dark', canvasTheme === 'dark'); }
  }
  const label = canvasTheme === 'light' ? 'Dark Canvas' : 'Light Canvas';
  for (const sel of ['#map-theme-btn', '#topo-theme-btn']) {
    const b = $(sel);
    if (b) b.textContent = label;
  }
}
function toggleCanvasTheme() {
  canvasTheme = canvasTheme === 'light' ? 'dark' : 'light';
  try { localStorage.setItem('netgold.canvasTheme', canvasTheme); } catch (e) { }
  applyCanvasTheme();
  if (currentMapId != null && !$('#view-maps').classList.contains('hidden')) renderMap(currentMapId);
  if (topo && !$('#view-topoeditor').classList.contains('hidden')) renderTopo();
}
$('#map-theme-btn') && ($('#map-theme-btn').onclick = toggleCanvasTheme);
$('#topo-theme-btn') && ($('#topo-theme-btn').onclick = toggleCanvasTheme);
function lineStroke(o, extra) {
  if (o.line_state === 'up') return LINE_UP;
  if (o.line_state === 'down') return LINE_DOWN;
  return extra.base_color_hex || '#3d7bff';
}
function lineWidth(extra) { return Math.max(1.4, (extra.width || 2) * 0.85); }

let mapListCache = [], groupListCache = [], groupDeviceCounts = new Map();
async function loadMaps() {
  try {
    const maps = await api('/api/maps');
    mapListCache = maps;
    groupListCache = await api('/api/groups').catch(() => []);
    // Device counts per group for the tree (v3.6): direct counts
    // rolled up over the group tree, so a parent group shows every
    // device beneath it.
    groupDeviceCounts = new Map();
    const devs = await api('/api/devices').catch(() => []);
    const direct = new Map();
    for (const d of devs) {
      if (d.group_id) direct.set(d.group_id, (direct.get(d.group_id) || 0) + 1);
    }
    const childrenOf = new Map();
    for (const g of groupListCache) {
      if (g.parent_id) {
        if (!childrenOf.has(g.parent_id)) childrenOf.set(g.parent_id, []);
        childrenOf.get(g.parent_id).push(g.id);
      }
    }
    const rollup = id => {
      let n = direct.get(id) || 0;
      for (const c of childrenOf.get(id) || []) n += rollup(c);
      return n;
    };
    for (const g of groupListCache) groupDeviceCounts.set(g.id, rollup(g.id));
    if (!maps.length) {
      $('#map-tree').innerHTML = '<p class="muted" style="padding:4px 6px">No maps yet.</p>';
      $('#map-wrap').innerHTML = '';
      $('#map-meta').textContent = 'No maps yet — run a legacy database import to bring your diagrams across.';
      return;
    }
    renderMapTree();
    const keep = currentMapId != null && maps.some(m => m.id === currentMapId);
    renderMap(keep ? currentMapId : maps[0].id);
  } catch (e) { $('#map-meta').textContent = 'Error: ' + e.message; }
}
// The left-hand tree mirrors the legacy system's Device Groups explorer:
// My Network at the top, then ONE row per group — the group's name
// and its device count — with child groups nested under their
// parent. Clicking a group row opens that group's map. (v3.6: each
// group used to appear twice — once as a group heading and once
// more as a map row with the same name. The map belongs to the
// group, so the group row itself is the map's row.) Maps that
// belong to no group sit directly under My Network.
function renderMapTree() {
  const tree = $('#map-tree');
  if (!tree) return;
  const mapByGroup = new Map();
  const extraMapsByGroup = new Map();
  const loose = [];
  for (const m of mapListCache) {
    if (m.group_id) {
      if (!mapByGroup.has(m.group_id)) mapByGroup.set(m.group_id, m);
      else {
        if (!extraMapsByGroup.has(m.group_id)) extraMapsByGroup.set(m.group_id, []);
        extraMapsByGroup.get(m.group_id).push(m);
      }
    } else loose.push(m);
  }
  const mapRow = m => `<div class="map-tree-map ${m.id === currentMapId ? 'active' : ''}" data-treemap="${m.id}" title="${esc(m.name)}">
      <span class="mt-name">${esc(m.name)}</span><span class="mt-count">${m.devices || 0} devices</span></div>`;
  let html = '<div class="map-tree-root">My Network</div>';
  for (const m of loose) html += mapRow(m);
  const byParent = new Map();
  for (const g of groupListCache) {
    const key = g.parent_id || 0;
    if (!byParent.has(key)) byParent.set(key, []);
    byParent.get(key).push(g);
  }
  const emittedGroups = new Set();
  const groupRow = (g, depth) => {
    const m = mapByGroup.get(g.id);
    const count = groupDeviceCounts.get(g.id) || 0;
    return `<div class="map-tree-group ${m && m.id === currentMapId ? 'active' : ''}" style="padding-left:${8 + depth * 14}px" ${m ? `data-treemap="${m.id}" title="Open the ${esc(g.name)} map"` : ''}>
      <span class="mt-name">${esc(g.name)}</span><span class="mt-count">${count} devices</span></div>`;
  };
  const groupRows = (parentKey, depth) => {
    let out = '';
    for (const g of byParent.get(parentKey) || []) {
      emittedGroups.add(g.id);
      out += groupRow(g, depth);
      // A group with several maps: the group row opens the first;
      // the rest keep their own rows under it, named by map name.
      for (const m of extraMapsByGroup.get(g.id) || []) out += mapRow(m);
      out += groupRows(g.id, depth + 1);
    }
    return out;
  };
  html += groupRows(0, 0);
  // Any group not reachable from the roots (orphaned parent) still shows.
  for (const g of groupListCache) {
    if (emittedGroups.has(g.id)) continue;
    html += groupRow(g, 0);
  }
  tree.innerHTML = html;
  tree.querySelectorAll('[data-treemap]').forEach(row => row.onclick = () => {
    selectMap(+row.dataset.treemap);
  });
}
function selectMap(id) {
  renderMap(id);
  document.querySelectorAll('#map-tree [data-treemap]').forEach(row =>
    row.classList.toggle('active', +row.dataset.treemap === id));
}
$('#map-tree-toggle') && ($('#map-tree-toggle').onclick = () => {
  $('#map-tree-panel').classList.add('hidden');
  $('#map-tree-show').classList.remove('hidden');
});
$('#map-tree-show') && ($('#map-tree-show').onclick = () => {
  $('#map-tree-panel').classList.remove('hidden');
  $('#map-tree-show').classList.add('hidden');
});
let currentMapId = null;
let currentMapObjects = []; // last fetched objects of the open map (hover cards, refresh)
let currentMapMeta = null;  // the open map's record (stored extents), set by renderMap
let mapScene = null;        // padded scene origin of the last render: scene x = stored x - minX, scene y = maxY - stored y
let mapVB = null;   // current viewBox {x, y, w, h}
let mapFit = null;  // fitted viewBox
const reduceMotion = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;

function mapApplyView(svg) {
  if (!svg || !mapVB) return;
  svg.setAttribute('viewBox', `${mapVB.x} ${mapVB.y} ${mapVB.w} ${mapVB.h}`);
  const pct = $('#map-zoom-pct');
  if (pct && mapFit && mapVB.w > 0) pct.textContent = Math.round(100 * mapFit.w / mapVB.w) + '%';
  mapScaleDevices(svg);
}
// mapScaleDevices counter-scales every fixed-size device group by
// 1/zoom around its anchor, so markers and labels keep their exact
// on-screen size at any zoom (v3.2.1). The effective zoom is the
// rendered px per map unit under preserveAspectRatio="meet".
function mapScaleDevices(svg) {
  if (!svg || !mapVB || !mapVB.w || !mapVB.h) return;
  // Scene-space tidy scenes (v3.6) carry no fixed-size furniture:
  // markers and labels are part of the scene and scale with it,
  // so there is nothing to counter-scale and no compact variant
  // to switch to — zooming simply enlarges the whole scene.
  if (svg.dataset && svg.dataset.tidyScene === '1') {
    svg.classList.remove('labels-compact');
    return;
  }
  const rect = svg.getBoundingClientRect();
  if (!rect.width || !rect.height) return;
  const k = Math.min(rect.width / mapVB.w, rect.height / mapVB.h);
  if (!(k > 0) || !isFinite(k)) return;
  // A grid-tidy render solved with compact label geometry
  // pins the compact display: its reservations only fit the
  // compact stack, whatever the zoom scale says.
  const pinCompact = svg.dataset && svg.dataset.pinCompact === '1';
  svg.classList.toggle('labels-compact', k < LBL_COMPACT_K || pinCompact);
  const s = (1 / k).toFixed(4);
  svg.querySelectorAll('.dev-fixed').forEach(g => {
    const d = g.parentNode.dataset;
    g.setAttribute('transform', `translate(${d.cx},${d.cy}) scale(${s})`);
  });
}
window.addEventListener('resize', () => {
  const msvg = $('#map-svg');
  if (msvg && mapVB) mapScaleDevices(msvg);
  const tsvg = $('#topo-svg');
  if (tsvg) topoScaleNodes(tsvg);
});
function mapZoom(factor, cx, cy) {
  const svg = $('#map-svg');
  if (!svg || !mapVB || !mapFit) return;
  const nw = Math.min(Math.max(mapVB.w * factor, mapFit.w * 0.12), mapFit.w * 2.5);
  const f = nw / mapVB.w;
  const px = cx != null ? cx : mapVB.x + mapVB.w / 2;
  const py = cy != null ? cy : mapVB.y + mapVB.h / 2;
  mapVB = { x: px - (px - mapVB.x) * f, y: py - (py - mapVB.y) * f, w: nw, h: mapVB.h * f };
  mapApplyView(svg);
}
function wireMapView(svg) {
  const pt = svg.createSVGPoint();
  const toSvg = e => { pt.x = e.clientX; pt.y = e.clientY; return pt.matrixTransform(svg.getScreenCTM().inverse()); };
  svg.addEventListener('wheel', e => {
    e.preventDefault();
    const p = toSvg(e);
    mapZoom(e.deltaY < 0 ? 0.85 : 1.18, p.x, p.y);
  }, { passive: false });
  let pan = null;
  svg.addEventListener('pointerdown', e => {
    if (e.target.closest('[data-mapdev], [data-grpmap]')) return; // device and group-marker clicks stay clicks
    pan = { sx: e.clientX, sy: e.clientY, vb: { ...mapVB }, moved: false };
    svg.setPointerCapture(e.pointerId);
  });
  svg.addEventListener('pointermove', e => {
    if (!pan) return;
    const rect = svg.getBoundingClientRect();
    const dx = (e.clientX - pan.sx) / rect.width * pan.vb.w;
    const dy = (e.clientY - pan.sy) / rect.height * pan.vb.h;
    if (Math.abs(e.clientX - pan.sx) + Math.abs(e.clientY - pan.sy) > 3) pan.moved = true;
    if (pan.moved) { mapVB = { ...pan.vb, x: pan.vb.x - dx, y: pan.vb.y - dy }; mapApplyView(svg); }
  });
  svg.addEventListener('pointerup', () => { pan = null; });
  svg.addEventListener('dblclick', e => { if (!e.target.closest('[data-mapdev], [data-grpmap]')) { mapVB = { ...mapFit }; mapApplyView(svg); } });
}
$('#map-zoom-in') && ($('#map-zoom-in').onclick = () => mapZoom(0.7));
$('#map-zoom-out') && ($('#map-zoom-out').onclick = () => mapZoom(1.4));
$('#map-zoom-fit') && ($('#map-zoom-fit').onclick = () => { const svg = $('#map-svg'); if (svg && mapFit) { mapVB = { ...mapFit }; mapApplyView(svg); } });

// ---- Tidy View (v3.4) --------------------------------------------------
// An on-screen rearrangement of the open map: devices are spread so
// that no marker or label overlaps another, region squares grow to
// contain the devices they held, and captions follow their region.
// The stored the legacy system positions are NEVER changed — the tidy
// geometry lives only in this render; switching Tidy View off
// re-renders the faithful layout. The arrangement itself is computed
// by the server (POST /api/tidy, a deterministic pure function).
//
// The arrangement is grid-first: the server lays each region's
// devices out on a clean grid of cells that reserve the marker
// plus the full label stack below it (members ordered by a
// breadth-first walk over the drawing's link graph, so linked
// devices land in neighbouring cells), resizes every region box
// to fit its grid, and returns the boxes' drawn rectangles with
// the positions. Labels in Tidy View therefore always sit below
// their own marker inside their own cell — the renderer's slot
// planner is not consulted in this mode.
const TIDY_MIN_DX_PX = 64;  // marker plate is 44 px wide; 64 leaves air
const TIDY_MIN_DY_PX = 98;  // plate 44 + compact label stack below (47) + gap;
                           // fuller stacks raise it per device via footprints
// Grid cells reserve the FULL label width (v3.6): a grid column
// is as wide as its widest full label — names are never cut to
// fit a cell, the cell grows to fit the name. The tidy scene is
// then rendered in scene space at the solved scale (see
// renderMap): the device furniture scales with the scene, so
// what the layout reserved is exactly what the screen shows,
// and zooming in reads every name at full size.
// The visible slack kept between neighbouring label pills in
// grid Tidy View (screen px): a pill's width budget is its
// cell's pitch minus this gap, split evenly between the two
// neighbours, so pills keep a clear space between them at the
// fit scale instead of sitting edge to edge.
const TIDY_PILL_GAP_PX = 10;
const TIDY_SNAP_TOL_PX = 30; // grid-snap tolerance (v3.4.2): arranged centres
// within this many screen px of each other (inside one region) are pulled
// onto one shared row/column line, so tidy rows come out ruler-straight.
// 30 px stays safely below the 64 x 106 px separation minima, so the
// settle pass after snapping only ever has small corrections to make.
// Tidy View is ON by default (v3.6): a map always opens in its
// tidy arrangement; the toggle switches back to the stored
// positions, which are never changed either way.
let mapTidy = true;
function syncTidyBtns() {
  // Only the Maps toggle is synced here: this function runs at
  // script start, long before the topology section's state
  // exists. The topology toggle syncs its own label.
  const b = $('#map-tidy-btn');
  if (b) { b.textContent = 'Tidy View: ' + (mapTidy ? 'On' : 'Off'); b.classList.toggle('tidy-on', mapTidy); }
}
function syncTopoTidyBtn() {
  const tb = $('#topo-tidy-btn');
  if (tb) { tb.textContent = 'Tidy View: ' + (topoTidy ? 'On' : 'Off'); tb.classList.toggle('tidy-on', topoTidy); }
}
$('#map-tidy-btn') && ($('#map-tidy-btn').onclick = () => {
  mapTidy = !mapTidy;
  syncTidyBtns();
  if (currentMapId) renderMap(currentMapId);
});
syncTidyBtns();

// computeMapTidy derives the full tidy geometry for a map's objects,
// in legacy coordinates: devPos per device object, sqEnv per square,
// txtPos per caption, grpPos per device-group box.
//
// The layout runs in SCREEN-PIXEL space: stored positions are
// multiplied by a design scale (the stored drawing's own fit
// scale), each device carries its label footprint in px (see
// labelFootprintPx), the grid layout reserves a cell per device
// inside its region box, and the arranged positions come back
// in px. The footprints are screen sizes, so they are only
// honoured if the arranged drawing is RENDERED at the scale its
// geometry assumes. The grid scene's px size is fixed by the
// footprints alone — it does not shrink with the design scale —
// so the design scale cannot be iterated the way the old
// heuristic pipeline iterated it: a smaller design scale only
// inflates the arranged drawing in map units, and the fit falls
// forever. Instead the render scale is solved directly: map
// units are the px result divided by s, and s is chosen by the
// ratio fitOf/s — measured with fitOf itself (the same extent
// and padding arithmetic renderMap uses) — because that ratio
// is what sets every screen pitch: a layout pitch of P px
// renders P * (fit / s) px wide. Rescaling is exact arithmetic
// on the one layout result, so the geometry the renderer fits
// is the geometry that was designed, at a scale chosen by the
// renderer's own fit arithmetic.
async function computeMapTidy(objects, cw, ch) {
  const devs = objects.filter(o => o.type === 'Device');
  const geom = { devPos: new Map(), sqEnv: new Map(), txtPos: new Map(), grpPos: new Map(), looseEnv: null };
  if (!devs.length) {
    // Nothing to arrange — an overview map of group markers, or
    // an empty map. The tidy geometry is the stored geometry:
    // group markers stay exactly where the source drawing put
    // them, and the renderer's extents follow them (an empty
    // geometry here would collapse the view to nothing).
    for (const o of objects) {
      if (o.type === 'DeviceGroup') geom.grpPos.set(o.id, { x: o.x, y: o.y });
      else if (o.type === 'Text') geom.txtPos.set(o.id, { x: o.x, y: o.y });
      else if (o.type === 'Square') {
        let extra = {};
        try { extra = JSON.parse(o.extra || '{}'); } catch (e) { }
        let x1 = o.x - (o.w || 0) / 2, x2 = o.x + (o.w || 0) / 2, y1 = o.y - (o.h || 0) / 2, y2 = o.y + (o.h || 0) / 2;
        if (Array.isArray(extra.envelope)) { [x1, y1, x2, y2] = extra.envelope; }
        geom.sqEnv.set(o.id, [Math.min(x1, x2), Math.min(y1, y2), Math.max(x1, x2), Math.max(y1, y2)]);
      }
    }
    return geom;
  }
  const spanOf = pts => {
    let a = 1e18, b = -1e18, c = 1e18, d = -1e18;
    pts.forEach(p => { a = Math.min(a, p.x); b = Math.max(b, p.x); c = Math.min(c, p.y); d = Math.max(d, p.y); });
    return { w: Math.max(b - a, 1), h: Math.max(d - c, 1) };
  };
  const span = spanOf(devs);
  // The design scale for the layout request: the fit scale of
  // the stored device span. It only sets the proportions of the
  // request (stored positions and box envelopes in px); the
  // scale the arranged drawing is rendered at is solved from
  // the layout result (see solveGrid below).
  let k = Math.min((cw || 1100) / span.w, (ch || 600) / span.h);
  if (!(k > 0) || !isFinite(k)) k = 1;
  const footprintsFor = compact => new Map(devs.map(o => {
    const name = o.device_name || ('Device ' + o.source_device_id);
    const lines = wrapLabel(name, 15);
    const caption = [o.device_type, o.device_ip].filter(Boolean).join(' · ');
    return [o.id, labelFootprintPx(lines, caption, name, compact)];
  }));
  // Region squares and membership, from the ORIGINAL drawing (the
  // smallest square containing the device's original centre owns
  // it). Membership also defines the grid-snap groups (v3.4.2):
  // devices that share a region are straightened onto shared rows
  // and columns; devices in no region snap together as one group,
  // which the layout treats as its leftover group.
  const squares = objects.filter(o => o.type === 'Square').map(o => {
    let extra = {};
    try { extra = JSON.parse(o.extra || '{}'); } catch (e) { }
    let x1 = o.x - (o.w || 0) / 2, x2 = o.x + (o.w || 0) / 2, y1 = o.y - (o.h || 0) / 2, y2 = o.y + (o.h || 0) / 2;
    if (Array.isArray(extra.envelope)) { [x1, y1, x2, y2] = extra.envelope; }
    const env = [Math.min(x1, x2), Math.min(y1, y2), Math.max(x1, x2), Math.max(y1, y2)];
    return { o, env, area: (env[2] - env[0]) * (env[3] - env[1]), members: [] };
  }).sort((a, b) => a.area - b.area);
  const ownerOf = new Map(); // device object id -> square entry
  for (const d of devs) {
    for (const sq of squares) {
      if (d.x >= sq.env[0] && d.x <= sq.env[2] && d.y >= sq.env[1] && d.y <= sq.env[3]) {
        sq.members.push(d); ownerOf.set(d.id, sq); break;
      }
    }
  }
  const snapGroups = squares.map(sq => sq.members.map(d => d.id)).filter(g => g.length);
  // Region boxes for the grid layout: EVERY non-degenerate
  // square is sent, with its parent box (the smallest other
  // square that contains it). A square that holds no devices is
  // still drawn furniture: if it were left at its stored
  // envelope while the layout packs everything else, the two
  // could overlap on screen. Sent to the layout, it is packed
  // like any other box and its drawn rectangle comes back with
  // the rest. Degenerate squares (zero width or height — drawn
  // bands) cannot hold a grid and are not sent; they travel
  // with their host box instead (see build below). A square
  // whose envelope exactly duplicates another's is not sent
  // either: the stored drawing shows one outline there, and
  // the duplicate travels with its twin (build's host-stretch
  // lands it exactly on the twin's arranged rectangle), instead
  // of becoming a second, empty box beside it. The squares
  // list is in ascending-area order, so the first of a
  // duplicate pair — the one device ownership already picked —
  // is the one sent.
  const seenEnv = new Set();
  const boxSquares = squares.filter(sq => {
    if (!((sq.env[2] - sq.env[0]) > 0 && (sq.env[3] - sq.env[1]) > 0)) return false;
    const key = sq.env.map(v => Math.round(v * 2) / 2).join(',');
    if (seenEnv.has(key)) return false;
    seenEnv.add(key);
    return true;
  });
  const parentSqOf = sq => {
    let best = null;
    for (const t of boxSquares) {
      if (t === sq || t.area <= sq.area) continue;
      if (t.env[0] <= sq.env[0] && t.env[1] <= sq.env[1] && t.env[2] >= sq.env[2] && t.env[3] >= sq.env[3]) {
        if (!best || t.area < best.area) best = t;
      }
    }
    return best;
  };
  const parentOfSq = new Map(boxSquares.map(sq => [sq, parentSqOf(sq)]));
  const gridBoxes = boxSquares.map(sq => ({ sq, parent: parentOfSq.get(sq) }));
  // Resolved link pairs for the layout (v3.4.4): the server
  // resolved each drawn line's ends to devices; a device can own
  // several markers on one map, so each end maps to the marker
  // nearest the line's stored end — the same geometric rule the
  // renderer anchors lines with. The layout receives the pairs
  // and pulls partners together, so a tidy link is a short hop
  // between neighbours instead of a line spanning the map.
  const devObjsByDevice = new Map();
  devs.forEach(o => {
    if (!o.device_id) return;
    if (!devObjsByDevice.has(o.device_id)) devObjsByDevice.set(o.device_id, []);
    devObjsByDevice.get(o.device_id).push(o);
  });
  const nearestObj = (list, x, y) => {
    let best = null, bd = Infinity;
    for (const o of list) {
      const d = (o.x - x) * (o.x - x) + (o.y - y) * (o.y - y);
      if (d < bd) { bd = d; best = o; }
    }
    return best;
  };
  const linkPairs = [], seenLink = new Set();
  objects.forEach(o => {
    if (o.type !== 'Line' || !o.line_from_device || !o.line_to_device) return;
    const fromList = devObjsByDevice.get(o.line_from_device);
    const toList = devObjsByDevice.get(o.line_to_device);
    if (!fromList || !toList) return;
    const a = nearestObj(fromList, o.x, o.y);
    const b = nearestObj(toList, o.x2 || o.x, o.y2 || o.y);
    if (!a || !b || a.id === b.id) return;
    const key = Math.min(a.id, b.id) + '-' + Math.max(a.id, b.id);
    if (seenLink.has(key)) return;
    seenLink.add(key);
    linkPairs.push([a.id, b.id]);
  });
  // deriveRest builds the non-device geometry for one arranged
  // placement (positions in map units; kk is the design scale):
  // region boxes re-derived from the members' arranged positions,
  // captions and group boxes moved along. Region padding is
  // exact-fit (v3.4.3): each edge grows by just what the outermost
  // members need there — sideways and at the bottom, the label
  // stack's full extent (labels hang below their markers, so the
  // bottom edge carries them inside the box); at the top, the
  // plate plus a margin (the planner's region rule keeps any
  // flipped label inside). No label straddles the re-drawn
  // border, and no label is forced to face the row above it.
  //
  // In grid mode the layout returns the drawn rectangle of every
  // region box itself; sqOverride then carries those rectangles
  // (map units) and deriveRest only moves captions and group
  // boxes along by each box's centre delta.
  const deriveRest = (placedArr, kk, fpMap, sqOverride) => {
    const posOf = new Map(placedArr.map(p => [p.id, p]));
    const newPos = o => posOf.get(o.id) || { x: o.x, y: o.y };
    const sqEnv = new Map(), txtPos = new Map(), grpPos = new Map();
    const deltaOf = new Map(); // square object id -> centre delta
    for (const sq of squares) {
      if (sqOverride && sqOverride.has(sq.o.id)) {
        const env = sqOverride.get(sq.o.id);
        sqEnv.set(sq.o.id, env);
        deltaOf.set(sq.o.id, {
          dx: (env[0] + env[2]) / 2 - (sq.env[0] + sq.env[2]) / 2,
          dy: (env[1] + env[3]) / 2 - (sq.env[1] + sq.env[3]) / 2
        });
        continue;
      }
      // The devices this box must fit: its stored members, plus
      // (for a square that owns no device in the stored drawing)
      // any device the arrangement placed inside its stored
      // envelope — the box is drawn around them either way, and
      // the planner will hold their labels to it.
      let mem = sq.members;
      if (!mem.length) {
        mem = devs.filter(d => {
          const p = newPos(d);
          return p.x >= sq.env[0] && p.x <= sq.env[2] && p.y >= sq.env[1] && p.y <= sq.env[3];
        });
      }
      if (!mem.length) { sqEnv.set(sq.o.id, sq.env.slice()); deltaOf.set(sq.o.id, { dx: 0, dy: 0 }); continue; }
      let a = 1e18, b = -1e18, c = 1e18, e = -1e18;
      mem.forEach(d => {
        const p = newPos(d);
        a = Math.min(a, p.x); b = Math.max(b, p.x); c = Math.min(c, p.y); e = Math.max(e, p.y);
      });
      const mgn = 6 / kk;
      let padL = (DEV_PLATE_R + 8) / kk, padR = padL, padB = padL;
      const padT = padL;
      const grow = d => {
        const p = newPos(d);
        const fp = fpMap.get(d.id);
        if (!fp) return;
        padL = Math.max(padL, fp.fw / 2 / kk - (p.x - a) + mgn);
        padR = Math.max(padR, fp.fw / 2 / kk - (b - p.x) + mgn);
        padB = Math.max(padB, fp.ext / kk - (p.y - c) + mgn);
      };
      mem.forEach(grow);
      // The arrangement can move a device that is not a stored
      // member of this square into the box's area; the planner
      // will hold that device's label to this box (it is the box
      // on screen around its marker), so the box must reserve
      // room for its label too. Any device whose arranged
      // position lands inside the box as drawn so far grows the
      // pads the same way a member does; two passes catch the
      // chain of a box growing into a further device.
      for (let pass = 0; pass < 2; pass++) {
        const envNow = [a - padL, c - padB, b + padR, e + padT];
        let grew = false;
        for (const d of devs) {
          if (mem.includes(d)) continue;
          const p = newPos(d);
          if (p.x < envNow[0] || p.x > envNow[2] || p.y < envNow[1] || p.y > envNow[3]) continue;
          const before = padL + padR + padB;
          grow(d);
          if (padL + padR + padB > before) grew = true;
        }
        if (!grew) break;
      }
      const env = [a - padL, c - padB, b + padR, e + padT];
      sqEnv.set(sq.o.id, env);
      deltaOf.set(sq.o.id, {
        dx: (env[0] + env[2]) / 2 - (sq.env[0] + sq.env[2]) / 2,
        dy: (env[1] + env[3]) / 2 - (sq.env[1] + sq.env[3]) / 2
      });
    }
    // Captions follow the region that contained their anchor; a
    // caption outside every region follows its nearest device.
    const nearestDelta = (x, y) => {
      let best = null, bd = Infinity;
      for (const d of devs) {
        const dd = (d.x - x) * (d.x - x) + (d.y - y) * (d.y - y);
        if (dd < bd) { bd = dd; best = d; }
      }
      if (!best) return { dx: 0, dy: 0 };
      const p = newPos(best);
      return { dx: p.x - best.x, dy: p.y - best.y };
    };
    for (const o of objects) {
      if (o.type !== 'Text') continue;
      let extra = {};
      try { extra = JSON.parse(o.extra || '{}'); } catch (e) { }
      const ax = Array.isArray(extra.anchor) ? extra.anchor[0] : o.x;
      const ay = Array.isArray(extra.anchor) ? extra.anchor[1] : o.y;
      // In grid mode a caption stays attached to its own box
      // in the source style. Its host is the arranged box that
      // holds its anchor — or, for the captions a drawing sets
      // under (or over) a box, centred on it, the nearest box
      // directly above or below the anchor within the box's
      // horizontal span. The caption keeps its stored position
      // relative to that box: vertically the anchor's fraction
      // of the box height, horizontally the fraction of the
      // caption's own centre (from its stored text envelope),
      // with the caption's width unchanged — the renderer draws
      // it at the same font size. A caption centred under its
      // box in the stored drawing is centred under the drawn
      // box; a caption inside keeps its place inside.
      if (sqOverride) {
        let host = null;
        for (const sq of boxSquares) {
          if (ax >= sq.env[0] && ax <= sq.env[2] && ay >= sq.env[1] && ay <= sq.env[3]) {
            if (!host || sq.area < host.area) host = sq;
          }
        }
        if (!host) {
          let bd = Infinity;
          for (const sq of boxSquares) {
            if (ax < sq.env[0] || ax > sq.env[2]) continue;
            const g = ay < sq.env[1] ? sq.env[1] - ay : (ay > sq.env[3] ? ay - sq.env[3] : 0);
            if (g < bd) { bd = g; host = sq; }
          }
        }
        if (host && sqOverride.has(host.o.id)) {
          const se = host.env, de = sqEnv.get(host.o.id);
          const fy = (ay - se[1]) / Math.max(se[3] - se[1], 1e-9);
          let nx;
          if (Array.isArray(extra.envelope) && extra.envelope.length === 4) {
            const tcx = (extra.envelope[0] + extra.envelope[2]) / 2;
            const tw = Math.abs(extra.envelope[2] - extra.envelope[0]);
            const fx = (tcx - se[0]) / Math.max(se[2] - se[0], 1e-9);
            nx = de[0] + fx * (de[2] - de[0]) - tw / 2;
          } else {
            const fx = (ax - se[0]) / Math.max(se[2] - se[0], 1e-9);
            nx = de[0] + fx * (de[2] - de[0]);
          }
          txtPos.set(o.id, { x: nx, y: de[1] + fy * (de[3] - de[1]) });
          continue;
        }
      }
      let hostSq = null;
      for (const sq of squares) {
        if (ax >= sq.env[0] && ax <= sq.env[2] && ay >= sq.env[1] && ay <= sq.env[3]) { hostSq = sq; break; }
      }
      let d = hostSq ? deltaOf.get(hostSq.o.id) : null;
      if (!d) d = nearestDelta(ax, ay);
      txtPos.set(o.id, { x: ax + d.dx, y: ay + d.dy });
    }
    // Device-group boxes follow the devices they framed.
    for (const o of objects) {
      if (o.type !== 'DeviceGroup') continue;
      const w = Math.max(o.w || 32, 110), h = Math.max(o.h || 32, 34);
      const inside = devs.filter(d => Math.abs(d.x - o.x) <= w / 2 && Math.abs(d.y - o.y) <= h / 2);
      if (!inside.length) { const d = nearestDelta(o.x, o.y); grpPos.set(o.id, { x: o.x + d.dx, y: o.y + d.dy }); continue; }
      let a = 1e18, b = -1e18, c = 1e18, e = -1e18;
      inside.forEach(d => { const p = newPos(d); a = Math.min(a, p.x); b = Math.max(b, p.x); c = Math.min(c, p.y); e = Math.max(e, p.y); });
      grpPos.set(o.id, { x: (a + b) / 2, y: (c + e) / 2 });
    }
    return { sqEnv, txtPos, grpPos };
  };
  // fitOf estimates the scale renderMap will fit a tidy drawing
  // at: the union of the arranged devices, the re-derived region
  // boxes, the moved captions and group boxes, and the stored
  // line ends, with renderMap's fit padding applied on top.
  const linePts = [];
  objects.forEach(o => { if (o.type === 'Line') { linePts.push({ x: o.x, y: o.y }, { x: o.x2 || o.x, y: o.y2 || o.y }); } });
  const fitOf = (devPosMap, rest) => {
    let minX = 1e18, minY = 1e18, maxX = -1e18, maxY = -1e18;
    const eat = (x, y) => { if (x < minX) minX = x; if (x > maxX) maxX = x; if (y < minY) minY = y; if (y > maxY) maxY = y; };
    devPosMap.forEach(p => eat(p.x, p.y));
    rest.sqEnv.forEach(e => { eat(e[0], e[1]); eat(e[2], e[3]); });
    if (rest.looseEnv) { eat(rest.looseEnv[0], rest.looseEnv[1]); eat(rest.looseEnv[2], rest.looseEnv[3]); }
    rest.txtPos.forEach(p => eat(p.x, p.y));
    rest.grpPos.forEach(p => eat(p.x, p.y));
    linePts.forEach(p => eat(p.x, p.y));
    if (!(maxX > minX) || !(maxY > minY)) return 0;
    const k0 = Math.min(cw / (maxX - minX), ch / (maxY - minY));
    if (!(k0 > 0) || !isFinite(k0)) return 0;
    // Mirror renderMap's grid-tidy fit pads. The arranged
    // scene already reserves every label's room inside its
    // box (the extents above include the boxes, the loose
    // frame and the caption anchors), so the pads only cover
    // what can still stick out: the loose frame's caption
    // above it, a caption's text below its anchor, and the
    // marker glow at the edges.
    const padSide = Math.max(24, 16 / k0);
    const padTop = Math.max(24, 30 / k0);
    const padBottom = Math.max(24, 24 / k0);
    return Math.min(cw / (maxX - minX + 2 * padSide), ch / (maxY - minY + padTop + padBottom));
  };
  // Single-scale construction (see the header note): the layout
  // runs once at the design scale k, and the render scale is
  // solved from its px result — map units are px divided by s,
  // and s is chosen by the fit ratio fitOf/s, the quantity that
  // sets every screen pitch. The label variant follows the
  // solved fit: when the full-label layout's fit lands below
  // the renderer's compact switch (LBL_COMPACT_K), the layout
  // runs once more reserving compact footprints — a smaller
  // scene that fits at a larger scale.
  const solveGridAt = async (compact, dk, useCanvas) => {
    const fpOf = footprintsFor(compact);
    // Grid-first layout: each point carries its grid footprint —
    // fw is the width of the label pill the renderer will draw
    // under the marker (capped per TIDY_GRID_FW_CAP_*), fh is
    // how far the label stack reaches below the marker centre
    // (labelFootprintPx's ext). The layout places every device
    // in a grid cell that reserves exactly that, inside its
    // region box, and returns the drawn rectangle of every box
    // plus each point's cell width.
    const res = await post('/api/tidy', {
      mode: 'grid',
      points: devs.map(o => {
        const name = o.device_name || ('Device ' + o.source_device_id);
        const lines = wrapLabel(name, 15);
        const caption = [o.device_type, o.device_ip].filter(Boolean).join(' · ');
        const fp = fpOf.get(o.id);
        return {
          id: o.id, x: o.x * dk, y: o.y * dk,
          // The full label width, uncapped (v3.6): the grid
          // reserves room for the whole name, always.
          fw: labelPillW(lines, caption, Infinity),
          fh: fp.ext
        };
      }),
      min_dx: TIDY_MIN_DX_PX, min_dy: TIDY_MIN_DY_PX,
      boxes: gridBoxes.map(({ sq, parent }) => ({
        id: sq.o.id,
        ids: sq.members.map(d => d.id),
        env: [sq.env[0] * dk, sq.env[1] * dk, sq.env[2] * dk, sq.env[3] * dk],
        parent: parent ? parent.o.id : 0
      })),
      links: linkPairs,
      aspect: (cw > 0 && ch > 0) ? cw / ch : 0,
      box_w: useCanvas ? cw : 0, box_h: useCanvas ? ch : 0
    });
    if (!res || !res.points || !res.points.length) return null;
    // build(s) converts the px result to map units at render
    // scale s and derives the rest of the geometry for it.
    const build = s => {
      const placed = res.points.map(p => ({ id: p.id, x: p.x / s, y: p.y / s }));
      const devPos = new Map(placed.map(p => [p.id, { x: p.x, y: p.y }]));
      const cellW = new Map(res.points.map((p, i) => [p.id, (res.cell_w && res.cell_w[i] ? res.cell_w[i] : 0) / s]));
      const sqOverride = new Map();
      (res.boxes || []).forEach(b => sqOverride.set(b.id, b.env.map(v => v / s)));
      // The loose group's drawn rectangle (devices in no
      // region), in map units at this scale; the renderer frames
      // them with it, and the fit counts it.
      const looseEnv = (res.loose && res.loose.env) ? res.loose.env.map(v => v / s) : null;
      // Squares the layout did not take (they hold no devices:
      // an empty duplicate outline, a divider band) are still
      // part of the drawing's furniture: they travel with the
      // box that held their stored centre, stretched by that
      // box's own growth, so in the arranged scene they sit —
      // and size — where they belong instead of at their
      // stored coordinates. (A duplicate outline identical to
      // its host's stored envelope lands exactly on the host's
      // arranged rectangle.)
      const sentSq = squares.filter(sq => sqOverride.has(sq.o.id));
      for (const sq of squares) {
        if (sqOverride.has(sq.o.id)) continue;
        const cx = (sq.env[0] + sq.env[2]) / 2, cy = (sq.env[1] + sq.env[3]) / 2;
        let host = null;
        for (const t of sentSq) {
          if (cx >= t.env[0] && cx <= t.env[2] && cy >= t.env[1] && cy <= t.env[3] && (!host || t.area < host.area)) host = t;
        }
        if (!host) {
          let bd = Infinity;
          for (const t of sentSq) {
            const d = (cx - (t.env[0] + t.env[2]) / 2) ** 2 + (cy - (t.env[1] + t.env[3]) / 2) ** 2;
            if (d < bd) { bd = d; host = t; }
          }
        }
        if (!host) { sqOverride.set(sq.o.id, sq.env.slice()); continue; }
        const he = sqOverride.get(host.o.id), hs = host.env;
        const sx = (he[2] - he[0]) / Math.max(hs[2] - hs[0], 1e-9);
        const sy = (he[3] - he[1]) / Math.max(hs[3] - hs[1], 1e-9);
        sqOverride.set(sq.o.id, [
          he[0] + (sq.env[0] - hs[0]) * sx, he[1] + (sq.env[1] - hs[1]) * sy,
          he[0] + (sq.env[2] - hs[0]) * sx, he[1] + (sq.env[3] - hs[1]) * sy
        ]);
      }
      const rest = deriveRest(placed, s, fpOf, sqOverride);
      rest.looseEnv = looseEnv;
      return { devPos, sqEnv: rest.sqEnv, txtPos: rest.txtPos, grpPos: rest.grpPos, grid: true, cellW, fit: fitOf(devPos, rest), looseEnv };
    };
    // What the screen actually shows is governed by the ratio
    // r = fit / s: a cell pitch of P px in the layout renders
    // P * r px wide, whatever s is. When the scene fits the
    // canvas, a fixed point (fit == s, r == 1) exists and the
    // damped walk below finds it. When the scene is larger than
    // the canvas no fixed point exists — the fit stays a fixed
    // fraction of any s — and r only improves toward smaller s,
    // so the walk's candidates are extended with halving probes
    // and the best measured ratio wins (ties go to the larger
    // scale). Every candidate is measured with fitOf itself, so
    // the choice is the renderer's own arithmetic, not a model
    // of it.
    const candidates = [];
    const consider = s => {
      const g = build(s);
      if (!(g.fit > 0) || !isFinite(g.fit)) return null;
      const c = { geom: g, s, r: g.fit / s };
      candidates.push(c);
      return c;
    };
    let s = dk;
    for (let it = 0; it < 6; it++) {
      const c = consider(s);
      if (!c) break;
      if (Math.abs(c.geom.fit - s) <= 0.005 * s) break; // fixed point reached
      s += 0.7 * (c.geom.fit - s);
    }
    let probe = candidates.length ? candidates[candidates.length - 1].s : 0;
    for (let it = 0; it < 4 && probe > 0; it++) {
      probe /= 2;
      if (!consider(probe)) break;
    }
    if (!candidates.length) return null;
    const maxR = Math.max(...candidates.map(c => c.r));
    const chosen = candidates.filter(c => c.r >= maxR - 0.002).sort((a, b) => b.s - a.s)[0];
    return { geom: chosen.geom, s: chosen.s, fit: chosen.geom.fit, r: chosen.r };
  };
  // The layout's px scene grows with the design scale: block
  // sizes are footprint-driven, but blocks start at the stored
  // centres (scaled by the design scale) and are pushed apart
  // from there, so a drawing stored with generous spacing keeps
  // much of that spacing at a large design scale. Trying a few
  // design scales and keeping the best fit ratio costs one
  // layout call each; ties go to the larger scale, whose blocks
  // stay closest to their stored arrangement.
  // Two layout flavours per design scale: with the canvas
  // named, the layout also runs its column optimisation
  // (boxes may trade columns for a better scene fit); without
  // it, columns come from the boxes' stored shapes alone.
  // The optimisation scores its own scene measure, which can
  // diverge from the fit this page computes (captions and
  // re-derived regions count here), so both flavours are
  // built and the fit ratio measured HERE — the renderer's
  // own — decides between them.
  const solveGrid = async compact => {
    let best = null;
    for (const dk of [k, k * 0.65, k * 0.42]) {
      for (const useCanvas of [true, false]) {
        const got = await solveGridAt(compact, dk, useCanvas);
        if (got && (!best || got.r > best.r + 0.002)) best = got;
      }
    }
    return best;
  };
  // Variant choice (v3.6): there is only the full-label solve
  // now. It used to fall back to compact (name-only, truncated)
  // labels whenever the render ratio dropped below ~1, because
  // the device furniture was fixed-size on screen while the
  // cells shrank with the scene — full pills then outgrew their
  // reservations. The tidy scene is now rendered in scene space
  // at the solved scale (geom.scaleS): furniture and cells scale
  // together by the same ratio, so the reservations hold at ANY
  // ratio and full labels are always safe. On the densest maps
  // the names at whole-map fit are complete and clear of each
  // other but small; zooming in reads them at full size.
  const solved = await solveGrid(false);
  if (!solved) throw new Error('tidy layout unavailable');
  geom.devPos = solved.geom.devPos;
  geom.sqEnv = solved.geom.sqEnv;
  geom.txtPos = solved.geom.txtPos;
  geom.grpPos = solved.geom.grpPos;
  geom.grid = true;
  geom.cellW = solved.geom.cellW;
  geom.looseEnv = solved.geom.looseEnv || null;
  geom.compactVariant = false;
  geom.scaleS = solved.s;
  return geom;
}

async function renderMap(id) {
  currentMapId = id;
  const { map, objects } = await api('/api/maps/' + id);
  currentMapObjects = objects;
  const titleEl = $('#map-title');
  if (titleEl) titleEl.textContent = map.name || '';
  $('#map-meta').textContent = `${objects.length} objects · the source drawing, faithfully rendered · marker = live status · ` +
    `green flowing line = path up · red pulsing line = a device on it is down · drag to pan · scroll to zoom · double-click to fit · hover a device for its status card · click a device to open it` +
    (mapTidy ? ' · Tidy View is ON: devices rearranged on screen so nothing overlaps — stored positions unchanged' : '');
  // Canvas size first: both the fit padding and the tidy layout are
  // computed against it.
  const wrapEl0 = $('#map-wrap');
  const cw0 = wrapEl0 ? wrapEl0.clientWidth : 0;
  const ch0 = Math.max(window.innerHeight * 0.72, 430); // matches #map-svg CSS height
  // In Tidy View the whole arranged scene must be visible at
  // once — that is the point of the view. The CSS canvas height
  // (72% of the window) starts partway down the page, so on
  // shorter windows its bottom rows would fall below the fold
  // and read as clipped. The tidy canvas is therefore sized to
  // the visible part of the window (and the layout below is
  // solved against that same height); the faithful view keeps
  // the standard canvas.
  let tidyCh = ch0;
  if (mapTidy && wrapEl0) {
    const visH = Math.floor(window.innerHeight - Math.max(wrapEl0.getBoundingClientRect().top, 0) - 10);
    if (visH >= 430 && visH < tidyCh) tidyCh = visH;
  }
  // Tidy View: arranged geometry for this render only (stored
  // positions are never touched).
  let tidyGeom = null;
  if (mapTidy) {
    try { tidyGeom = await computeMapTidy(objects, cw0, tidyCh); }
    catch (e) { tidyGeom = null; }
  }
  // Scene-space tidy rendering (v3.6): in grid Tidy View the
  // device furniture is drawn in scene space at the layout's
  // solved scale (1/s), so markers and full labels scale with
  // the scene exactly as the grid reserved them — see
  // computeMapTidy. sceneK is the local-units-per-scene-unit
  // factor the device groups are drawn with.
  const sceneMode = !!(tidyGeom && tidyGeom.grid && tidyGeom.scaleS > 0);
  const sceneK = sceneMode ? 1 / tidyGeom.scaleS : 0;
  let minX = map.min_x, maxX = map.max_x, minY = map.min_y, maxY = map.max_y;
  if (!(maxX > minX) || !(maxY > minY)) { // fall back to object extents
    minX = 1e18; minY = 1e18; maxX = -1e18; maxY = -1e18;
    objects.forEach(o => { minX = Math.min(minX, o.x - (o.w || 0) / 2, o.x2 || 1e18); maxX = Math.max(maxX, o.x + (o.w || 0) / 2); minY = Math.min(minY, o.y - (o.h || 0) / 2); maxY = Math.max(maxY, o.y + (o.h || 0) / 2); });
    if (minX > maxX) { minX = 0; minY = 0; maxX = 100; maxY = 100; }
  }
  if (tidyGeom) {
    // The arranged drawing gets its own extents: arranged devices,
    // grown region squares, moved captions and group boxes, plus
    // the stored geometry of any unresolved line ends.
    minX = 1e18; minY = 1e18; maxX = -1e18; maxY = -1e18;
    const eat = (x, y) => { if (x < minX) minX = x; if (x > maxX) maxX = x; if (y < minY) minY = y; if (y > maxY) maxY = y; };
    tidyGeom.devPos.forEach(p => eat(p.x, p.y));
    tidyGeom.sqEnv.forEach(e => { eat(e[0], e[1]); eat(e[2], e[3]); });
    if (tidyGeom.looseEnv) { const le = tidyGeom.looseEnv; eat(le[0], le[1]); eat(le[2], le[3]); }
    tidyGeom.txtPos.forEach(p => eat(p.x, p.y));
    tidyGeom.grpPos.forEach(p => eat(p.x, p.y));
    objects.forEach(o => { if (o.type === 'Line') { eat(o.x, o.y); eat(o.x2 || o.x, o.y2 || o.y); } });
    if (!(maxX > minX)) { minX = 0; maxX = 100; }
    if (!(maxY > minY)) { minY = 0; maxY = 100; }
  }
  // Fit padding (v3.2.1): markers and labels are fixed-size on
  // screen, so the margin the fit must leave is a screen distance,
  // converted back to map units at the fit scale — room beside an
  // edge device for its label pill, above it for the marker, and
  // below it for the label stack, so edge labels are never clipped
  // by the canvas border. (Stored-Y grows upward: the label stack
  // hangs toward minY, the marker top toward maxY.)
  let padSide = 70, padTop = 70, padBottom = 70;
  const wrapEl = wrapEl0;
  const cw = cw0;
  const ch = ch0;
  if (cw > 0 && maxX > minX && maxY > minY) {
    const k0 = Math.min(cw / (maxX - minX), ch / (maxY - minY));
    if (k0 > 0 && isFinite(k0)) {
      // In Tidy View the arranged geometry already reserves
      // each label's room (region boxes are exact-fit around
      // their members' stacks), so the fit pads only need to
      // cover what sticks out past the arranged extents: for
      // the compact labels a small fit shows, that is a narrow
      // pill half-width and the short stack; for full labels
      // the faithful pads apply unchanged.
      const tidyCompact = tidyGeom && k0 < LBL_COMPACT_K;
      if (tidyGeom && tidyGeom.grid) {
        // Grid Tidy: the arranged scene reserves every
        // label's room inside its box, so the pads only
        // cover what can still stick out (mirrors fitOf):
        // the loose frame's caption, a caption's text below
        // its anchor, and the marker glow at the edges.
        padSide = Math.max(24, 16 / k0);
        padTop = Math.max(24, 30 / k0);
        padBottom = Math.max(24, 24 / k0);
      } else {
        padSide = Math.max(70, (tidyCompact ? 68 : 92) / k0);
        padTop = Math.max(70, (tidyCompact ? 30 : 40) / k0);
        padBottom = Math.max(70, (tidyCompact ? 56 : 84) / k0);
      }
    }
  }
  minX -= padSide; maxX += padSide; minY -= padBottom; maxY += padTop;
  mapScene = { minX, maxY };
  currentMapMeta = map;
  const W = maxX - minX, H = maxY - minY;
  const X = x => (x - minX);
  const Y = y => (maxY - y); // legacy's Y grows upward; SVG's grows downward
  const F = n => (+n).toFixed(1);
  const light = canvasTheme === 'light';
  let p = `<defs>
    <radialGradient id="ng-mapbg" cx="0.5" cy="0.38" r="0.95">
      <stop offset="0" stop-color="#16222f"/><stop offset="0.6" stop-color="#0d1622"/><stop offset="1" stop-color="#080e16"/>
    </radialGradient>
    <pattern id="ng-grid" width="50" height="50" patternUnits="userSpaceOnUse">
      <path d="M50 0H0V50" fill="none" stroke="${light ? 'rgba(70,100,140,.10)' : 'rgba(120,165,225,.08)'}" stroke-width="1"/>
    </pattern></defs>
    <rect x="0" y="0" width="${F(W)}" height="${F(H)}" fill="${light ? '#f7f9fb' : 'url(#ng-mapbg)'}"/>
    <rect x="0" y="0" width="${F(W)}" height="${F(H)}" fill="url(#ng-grid)"/>`;
  // Tidy View: devices that belong to no region get a frame of
  // their own — a neutral dashed box the layout packed like the
  // region boxes, with a small caption above it — so they read
  // as a group instead of floating at the canvas edge. Drawn
  // first, under everything else. Its furniture is sized in
  // screen px at the fit scale, like the device furniture.
  if (tidyGeom && tidyGeom.looseEnv) {
    const le = tidyGeom.looseEnv;
    const kEst = Math.max((cw > 0 && W > 0 && H > 0) ? Math.min(cw / W, tidyCh / H) : 1, 0.05);
    const lx = X(Math.min(le[0], le[2])), ly = Y(Math.max(le[1], le[3]));
    p += `<rect class="map-region map-region-loose" x="${F(lx)}" y="${F(ly)}" width="${F(Math.abs(le[2] - le[0]))}" height="${F(Math.abs(le[3] - le[1]))}" rx="${(8 / kEst).toFixed(1)}" fill="rgba(130,150,175,.08)" stroke="rgba(130,150,175,.6)" stroke-width="${(1.4 / kEst).toFixed(2)}" stroke-dasharray="${(7 / kEst).toFixed(1)} ${(5 / kEst).toFixed(1)}"/>` +
      `<text class="map-loose-lbl" x="${F(lx + 2 / kEst)}" y="${F(ly - 6 / kEst)}" font-size="${(12.5 / kEst).toFixed(1)}" font-weight="600" fill="${light ? '#5a6b7d' : '#8fa5bd'}">Ungrouped devices</text>`;
  }
  // Marker centres by device, for line anchoring (v3.2.1): a
  // resolved line end is drawn to the device centre, and the opaque
  // marker plate — painted above the lines, in the device pass
  // below — clips it visually at the plate edge. Because the plate
  // is fixed-size on screen, the line stops at the plate edge at
  // every zoom with no per-zoom geometry work. A device merged from
  // several the legacy system markers keeps one marker each; the anchor
  // is the marker nearest the stored endpoint, the same geometric
  // rule the server used to attribute the endpoint.
  const markersByDevice = new Map();
  // Effective device position for this render (tidy geometry when
  // Tidy View is on, the stored position otherwise).
  const devPosOf = o => (tidyGeom && tidyGeom.devPos.get(o.id)) || { x: o.x, y: o.y };
  for (const o of objects) {
    if (o.type !== 'Device' || !o.device_id) continue;
    const dp0 = devPosOf(o);
    if (!markersByDevice.has(o.device_id)) markersByDevice.set(o.device_id, []);
    markersByDevice.get(o.device_id).push({ x: X(dp0.x), y: Y(dp0.y) });
  }
  const anchorFor = (deviceId, rawX, rawY) => {
    const pts = deviceId ? markersByDevice.get(deviceId) : null;
    if (!pts || !pts.length) return null;
    let best = pts[0], bd = Infinity;
    for (const pt of pts) {
      const d = (pt.x - rawX) * (pt.x - rawX) + (pt.y - rawY) * (pt.y - rawY);
      if (d < bd) { bd = d; best = pt; }
    }
    return best;
  };
  // Region geometry for the smart label placement (v3.4.2): the
  // drawn squares' envelopes — the stored envelope, or the tidy
  // re-derived one when Tidy View is on. A device's region is the
  // smallest square holding its STORED position (the same
  // containment rule the tidy layout uses for its snap groups);
  // the label planner keeps the pill inside that region's drawn
  // rectangle. Squares with a zero width or height render as 3 px
  // bands, so their region rectangles get the same 3 px extent.
  const squareRegions = [];
  for (const o of objects) {
    if (o.type !== 'Square') continue;
    let extra = {};
    try { extra = JSON.parse(o.extra || '{}'); } catch (e) { }
    let ex1 = o.x - (o.w || 0) / 2, ex2 = o.x + (o.w || 0) / 2, ey1 = o.y - (o.h || 0) / 2, ey2 = o.y + (o.h || 0) / 2;
    if (Array.isArray(extra.envelope)) { [ex1, ey1, ex2, ey2] = extra.envelope; }
    const storedEnv = [Math.min(ex1, ex2), Math.min(ey1, ey2), Math.max(ex1, ex2), Math.max(ey1, ey2)];
    let drawnEnv = storedEnv;
    if (tidyGeom && tidyGeom.sqEnv.has(o.id)) {
      const te = tidyGeom.sqEnv.get(o.id);
      drawnEnv = [Math.min(te[0], te[2]), Math.min(te[1], te[3]), Math.max(te[0], te[2]), Math.max(te[1], te[3])];
    }
    squareRegions.push({ storedEnv, drawnEnv, area: (storedEnv[2] - storedEnv[0]) * (storedEnv[3] - storedEnv[1]) });
  }
  squareRegions.sort((a, b) => a.area - b.area);
  const regionSceneOf = (px, py) => {
    for (const sq of squareRegions) {
      // A square with a zero width or height is a drawn band or
      // line, not a box a label can belong to — it owns no region.
      if (sq.storedEnv[2] - sq.storedEnv[0] <= 0 || sq.storedEnv[3] - sq.storedEnv[1] <= 0) continue;
      // Membership is tested at the position the device is DRAWN
      // at (the arranged position in Tidy View, the stored one
      // otherwise — they coincide in faithful mode), against the
      // envelope that will actually be drawn, so the region the
      // planner constrains a label to is the box on screen.
      const e = tidyGeom ? sq.drawnEnv : sq.storedEnv;
      if (px >= e[0] && px <= e[2] && py >= e[1] && py <= e[3]) {
        const d = sq.drawnEnv;
        const x0 = d[0] === d[2] ? d[0] - 1.5 : d[0], x1 = d[0] === d[2] ? d[2] + 1.5 : d[2];
        const y0 = d[1] === d[3] ? d[1] - 1.5 : d[1], y1 = d[1] === d[3] ? d[3] + 1.5 : d[3];
        return { x0: X(x0), y0: Y(y1), x1: X(x1), y1: Y(y0) };
      }
    }
    return null;
  };
  // Objects arrive ordered by ZOrder. Non-device objects render in
  // that order; devices render in a second pass on top, so a line
  // always tucks under the marker plates it connects. Device label
  // jobs are collected during the pass and built afterwards, once
  // every device position is known and the label width budgets
  // (v3.2.2) can be resolved against the neighbours.
  const devJobs = [];
  const grpJobs = [];
  // Parallel links (v3.6): two stored lines between the same pair
  // of markers anchor at the same two centres and would draw
  // exactly on top of each other — one of them invisible. Each
  // extra line of a pair is shifted sideways (perpendicular to
  // the link) by a few scene units, alternating sides, so every
  // stored line is visibly its own line.
  const linePairSeen = new Map();
  const parallelOffset = (x1, y1, x2, y2) => {
    const a = `${x1.toFixed(1)},${y1.toFixed(1)}`, b = `${x2.toFixed(1)},${y2.toFixed(1)}`;
    const key = a < b ? a + '|' + b : b + '|' + a;
    const n = linePairSeen.get(key) || 0;
    linePairSeen.set(key, n + 1);
    if (n === 0) return [0, 0];
    const step = 9; // scene units between parallel lines
    const mag = (n % 2 === 1 ? Math.ceil(n / 2) : -n / 2) * step;
    const dx = x2 - x1, dy = y2 - y1, len = Math.hypot(dx, dy);
    if (!(len > 0)) return [0, 0];
    return [-dy / len * mag, dx / len * mag];
  };
  for (const o of objects) {
    let extra = {};
    try { extra = JSON.parse(o.extra || '{}'); } catch (e) { }
    if (o.type === 'Line') {
      // Anchors are already in scene coordinates (anchorFor returns
      // transformed centres); unresolved ends fall back to the
      // stored endpoints, transformed here.
      const fromC = anchorFor(o.line_from_device, X(o.x), Y(o.y));
      const toC = anchorFor(o.line_to_device, X(o.x2), Y(o.y2));
      let x1 = fromC ? fromC.x : X(o.x), y1 = fromC ? fromC.y : Y(o.y);
      let x2 = toC ? toC.x : X(o.x2), y2 = toC ? toC.y : Y(o.y2);
      const [ox, oy] = parallelOffset(x1, y1, x2, y2);
      x1 += ox; x2 += ox; y1 += oy; y2 += oy;
      const st = o.line_state || 'unknown';
      p += `<line class="map-line map-line-${st}" data-line="${o.id}" x1="${F(x1)}" y1="${F(y1)}" x2="${F(x2)}" y2="${F(y2)}" stroke="${lineStroke(o, extra)}" stroke-width="${lineWidth(extra).toFixed(1)}" stroke-linecap="round"/>`;
    } else if (o.type === 'Square') {
      let ex1 = o.x - (o.w || 0) / 2, ex2 = o.x + (o.w || 0) / 2, ey1 = o.y - (o.h || 0) / 2, ey2 = o.y + (o.h || 0) / 2;
      if (Array.isArray(extra.envelope)) { [ex1, ey1, ex2, ey2] = extra.envelope; }
      if (tidyGeom && tidyGeom.sqEnv.has(o.id)) { [ex1, ey1, ex2, ey2] = tidyGeom.sqEnv.get(o.id); }
      let rw = Math.abs(ex2 - ex1), rh = Math.abs(ey2 - ey1);
      if (rw === 0) rw = 3; // a few legacy squares are stored as zero-width bands
      if (rh === 0) rh = 3;
      const fill = extra.filled === false ? 'none' : (extra.fill_color_hex || 'rgba(90,120,160,.25)');
      // The stored region colours were chosen by the legacy system for its
      // light canvas: on light they are shown stronger; on the dark
      // canvas they are kept translucent so the scene stays readable.
      const stroke = extra.base_color_hex && extra.base_color_hex !== '#000000' ? extra.base_color_hex
        : (light ? 'rgba(90,110,140,.55)' : 'rgba(130,165,210,.55)');
      p += `<rect class="map-region" x="${F(X(Math.min(ex1, ex2)))}" y="${F(Y(Math.max(ey1, ey2)))}" width="${F(rw)}" height="${F(rh)}" rx="${extra.rounded ? 10 : 2}" fill="${fill}" fill-opacity="${light ? 0.35 : 0.30}" stroke="${stroke}" stroke-width="${Math.max(1, (extra.width || 1))}"/>`;
    } else if (o.type === 'Text') {
      const font = extra.font || {};
      const size = font.size || 21;
      const anchored = Array.isArray(extra.anchor);
      let tx = anchored ? X(extra.anchor[0]) : X(o.x);
      let ty = anchored ? Y(extra.anchor[1]) : Y(o.y);
      if (tidyGeom && tidyGeom.txtPos.has(o.id)) { const tp = tidyGeom.txtPos.get(o.id); tx = X(tp.x); ty = Y(tp.y); }
      const style = `${font.underline ? 'text-decoration:underline;' : ''}${font.italic ? 'font-style:italic;' : ''}`;
      // the legacy system draws map captions in the annotation's base
      // colour (black) on its light canvas; the stored fill colour is
      // the label's highlight colour, used on the dark canvas.
      const textFill = light
        ? (extra.base_color_hex && extra.base_color_hex !== '#000000' ? extra.base_color_hex : '#26313d')
        : (extra.fill_color_hex && extra.fill_color_hex !== '#000000' ? extra.fill_color_hex : '#dbe7f5');
      p += `<text class="map-text" x="${F(tx)}" y="${F(ty)}" font-size="${size}" font-weight="${font.bold ? 700 : (font.weight || 600)}" text-anchor="${anchored ? 'start' : 'middle'}" fill="${textFill}" style="${style}">${esc(o.text)}</text>`;
    } else if (o.type === 'DeviceGroup') {
      // Group marker (v3.6): a DeviceGroup object is drawn as a
      // group marker — the folder pictogram on the Design-A
      // diamond plate, coloured by the group's aggregate status,
      // with the group's full name in a label beneath — exactly
      // like a device marker, and it drills into that group's own
      // map on click. (It used to be drawn as a large dashed box
      // whose name collided with its neighbours on the root map.)
      const gp = (tidyGeom && tidyGeom.grpPos.get(o.id)) || { x: o.x, y: o.y };
      const target = (o.group_id && mapListCache.find(m => m.group_id === o.group_id)) ||
        mapListCache.find(m => m.name === (o.group_name || ''));
      grpJobs.push({
        o, cx: X(gp.x), cy: Y(gp.y),
        label: o.group_name || 'Group',
        st: o.group_status || 'unknown',
        caption: (o.group_devices || 0) + ((o.group_devices || 0) === 1 ? ' device' : ' devices'),
        lines: wrapLabel(o.group_name || 'Group', 15),
        targetId: target ? target.id : null,
      });
    } else if (o.type === 'Device') {
      const dp0 = devPosOf(o);
      devJobs.push({
        o, cx: X(dp0.x), cy: Y(dp0.y),
        label: o.device_name || ('Device ' + o.source_device_id),
        st: o.device_status || 'unknown',
        lines: wrapLabel(o.device_name || ('Device ' + o.source_device_id), 15),
        caption: [o.device_type, o.device_ip].filter(Boolean).join(' · '),
      });
    }
  }
  // Label width budgets (v3.2.2): the full labels are designed for
  // the scale they appear at (the fit scale, or LBL_COMPACT_K when
  // the map fits smaller than that and full labels only return on
  // zoom-in); the compact labels for the fit scale itself (capped
  // at LBL_COMPACT_K for maps that fit larger).
  const kFit = (cw > 0 && W > 0 && H > 0) ? Math.min(cw / W, (tidyGeom ? tidyCh : ch) / H) : 0;
  const devPts = devJobs.map(j => ({ x: j.cx, y: j.cy }));
  const budFull = resolveLabelWidths(devPts, Math.max(kFit, LBL_COMPACT_K),
    devJobs.map(j => labelNaturalW(j.lines, j.caption)),
    devJobs.map(j => labelReach(j.lines, j.caption)));
  const budCompact = resolveLabelWidths(devPts, Math.min(kFit, LBL_COMPACT_K),
    devJobs.map(j => Math.ceil(j.label.length * LBL_COMPACT_CHAR_W) + 10),
    devJobs.map(() => LBL_COMPACT_REACH));
  const stagCompact = planLabelStagger(devPts, Math.min(kFit, LBL_COMPACT_K), budCompact);
  // Region width caps (v3.4.2): a label wider than its region box
  // can never be placed inside it, so when a device's pill is
  // wider than the box (at the variant's design scale), the
  // budget is capped at the box width — the name truncates a
  // little more (the hover card and title still carry it in full)
  // and the pill fits its box. Boxes narrower than the minimum
  // readable pill are left alone: no placement could fit those.
  devJobs.forEach((j, i) => {
    if (tidyGeom && tidyGeom.grid) return; // grid cells already reserve the label's space
    const dpR = devPosOf(j.o);
    const r = regionSceneOf(dpR.x, dpR.y);
    if (!r) return;
    const wU = r.x1 - r.x0;
    const capF = Math.floor(wU * Math.max(kFit, LBL_COMPACT_K)) - 2 * LBL_REGION_MARGIN;
    if (capF >= LBL_MIN_W && labelPillW(j.lines, j.caption, budFull[i]) > capF) budFull[i] = capF;
    const capC = Math.floor(wU * Math.min(kFit, LBL_COMPACT_K)) - 2 * LBL_REGION_MARGIN;
    if (capC >= LBL_MIN_W && labelPillCompactW(j.label, budCompact[i]) > capC) budCompact[i] = capC;
  });
  // Smart label placement (v3.4.2): per device and variant, the
  // slot planner (see planLabelSlots) decides which side of the
  // marker the pill is drawn on, so a pill never crosses its
  // region's border while its device sits inside the region, and
  // crowded below-pills spill to free slots instead of each other.
  // Each variant is planned at its own design scale — the same
  // scales the budgets above use.
  const mapCanvas = { x0: 0, y0: 0, x1: W, y1: H };
  // In Tidy View (v3.4.3) the planner runs in hard mode against
  // the re-derived region boxes: the tidy layout reserved every
  // label's footprint, so a fully clean placement always exists,
  // and the planner treats marker contact as a hard constraint —
  // the outermost rows' labels flip inward rather than straddle
  // the redrawn border, and no label is left on a marker.
  let placeFull, placeCompact;
  if (tidyGeom && tidyGeom.grid) {
    // Grid Tidy View: every device sits in a grid cell that
    // reserves its marker plus the full label stack beneath it,
    // so the label simply goes below its marker, centred, with
    // its width budget set to the cell's pitch. No slot planner
    // is consulted — by construction the pill touches nothing.
    devJobs.forEach((j, i) => {
      const cwU = tidyGeom.cellW.get(j.o.id) || 0;
      if (cwU > 0) {
        // The budget is the cell's screen width minus the pill
        // gap (see TIDY_PILL_GAP_PX): the tidy construction
        // solved the render scale from the layout, so a pill
        // truncated to the budget keeps a clear space to its
        // neighbours at that scale instead of touching them.
        // Both variants share the budget — the variant only
        // changes the label text (full stack vs one line), never
        // how wide it may grow.
        budFull[i] = Math.max(LBL_MIN_W, Math.floor(cwU * kFit) - TIDY_PILL_GAP_PX);
        budCompact[i] = Math.max(LBL_MIN_W, Math.floor(cwU * kFit) - TIDY_PILL_GAP_PX);
      }
    });
    placeFull = devJobs.map(() => ({ slot: 'below', dx: 0, dy: 0, stag: false }));
    placeCompact = placeFull.map(p => ({ ...p }));
  } else {
    placeFull = planLabelSlots(devJobs.map((j, i) => ({
      ax: j.cx, ay: j.cy, w: labelPillW(j.lines, j.caption, budFull[i]), h: labelReach(j.lines, j.caption),
      stag: false, region: regionSceneOf(devPosOf(j.o).x, devPosOf(j.o).y),
    })), Math.max(kFit, LBL_COMPACT_K), mapCanvas, tidyGeom ? { hard: true } : null);
    placeCompact = planLabelSlots(devJobs.map((j, i) => ({
      ax: j.cx, ay: j.cy, w: labelPillCompactW(j.label, budCompact[i]), h: LBL_COMPACT_REACH,
      stag: !!stagCompact[i], region: regionSceneOf(devPosOf(j.o).x, devPosOf(j.o).y),
    })), Math.min(kFit, LBL_COMPACT_K), mapCanvas, tidyGeom ? { hard: true } : null);
  }
  // Two-pass painting (v3.4.3): every marker is painted first and
  // every label stack afterwards, in a layer above all markers —
  // a label can never be buried under a neighbour's plate, however
  // the devices interleave. Fixed-size device furniture (v3.2.1):
  // plate, glyph and label stack live in .dev-fixed groups, whose
  // geometry is screen px; mapApplyView counter-scales each by
  // 1/zoom around its anchor (both passes carry the anchor), so
  // the device renders at exactly the same size at any zoom.
  let dpM = '', dpL = '';
  const belowPlace = { slot: 'below', dx: 0, dy: 0, stag: false };
  devJobs.forEach((j, i) => {
    const o = j.o, st = j.st;
    if (sceneMode) {
      // Scene-space furniture (v3.6): the marker and its full
      // label live in a group scaled by the layout's solved
      // scale, so they occupy exactly the grid cell the layout
      // reserved — no counter-scaling, no compact variant.
      const tr = `translate(${F(j.cx)},${F(j.cy)}) scale(${sceneK.toFixed(5)})`;
      dpM += `<g class="map-dev st-${st}" data-mapdev="${o.device_id || ''}" data-mapobj="${o.id}" data-cx="${F(j.cx)}" data-cy="${F(j.cy)}" data-st="${st}">` +
           `<title>${esc(j.label)}${o.device_ip ? ' · ' + esc(o.device_ip) : ''}${o.device_type ? ' · ' + esc(o.device_type) : ''} · ${esc(st)}</title>` +
           `<g class="dev-scene" transform="${tr}">` +
           `<circle class="dev-glow" r="30"/>` +
           `<g class="dev-plate">${markerSVG(st)}</g>` +
           iconUseIn(o.icon_id, DEV_GLYPH_PX) +
           `</g></g>`;
      dpL += `<g class="map-dev-lbl" data-mapdev="${o.device_id || ''}" data-mapobj="${o.id}" data-cx="${F(j.cx)}" data-cy="${F(j.cy)}">` +
           `<g class="dev-scene" transform="${tr}">` +
           labelPill(j.lines, j.caption, 'map-lbl', 'map-type-lbl', Infinity, belowPlace) +
           `</g></g>`;
      return;
    }
    dpM += `<g class="map-dev st-${st}" data-mapdev="${o.device_id || ''}" data-mapobj="${o.id}" data-cx="${F(j.cx)}" data-cy="${F(j.cy)}" data-st="${st}">` +
         `<title>${esc(j.label)}${o.device_ip ? ' · ' + esc(o.device_ip) : ''}${o.device_type ? ' · ' + esc(o.device_type) : ''} · ${esc(st)}</title>` +
         `<g class="dev-fixed" transform="translate(${F(j.cx)},${F(j.cy)})">` +
         `<circle class="dev-glow" r="30"/>` +
         `<g class="dev-plate">${markerSVG(st)}</g>` +
         iconUseIn(o.icon_id, DEV_GLYPH_PX) +
         `</g></g>`;
    dpL += `<g class="map-dev-lbl" data-mapdev="${o.device_id || ''}" data-mapobj="${o.id}" data-cx="${F(j.cx)}" data-cy="${F(j.cy)}">` +
         `<g class="dev-fixed" transform="translate(${F(j.cx)},${F(j.cy)})">` +
         labelStack(j.label, j.lines, j.caption, 'map-lbl', 'map-type-lbl', budFull[i], budCompact[i], placeFull[i], placeCompact[i]) +
         `</g></g>`;
  });
  // Group markers (v3.6) paint with the devices: plate + folder
  // pictogram in the marker pass, the full group-name label in
  // the label pass, so a label can never be buried either.
  grpJobs.forEach(j => {
    const st = j.st;
    const inner = sceneMode
      ? `<g class="dev-scene" transform="translate(${F(j.cx)},${F(j.cy)}) scale(${sceneK.toFixed(5)})">`
      : `<g class="dev-fixed" transform="translate(${F(j.cx)},${F(j.cy)})">`;
    dpM += `<g class="map-grp-marker st-${st}" data-grpobj="${j.o.id}" data-cx="${F(j.cx)}" data-cy="${F(j.cy)}" data-st="${st}" ${j.targetId ? `data-grpmap="${j.targetId}"` : ''}>` +
         `<title>${esc(j.label)} · ${esc(j.caption)} · group ${esc(st)}</title>` +
         inner +
         `<circle class="dev-glow" r="30"/>` +
         `<g class="dev-plate">${markerSVG(st)}</g>` +
         iconUseIn('folder', DEV_GLYPH_PX) +
         `</g></g>`;
    dpL += `<g class="map-grp-lbl-g" data-grpobj="${j.o.id}" data-cx="${F(j.cx)}" data-cy="${F(j.cy)}">` +
         inner +
         (sceneMode
           ? labelPill(j.lines, j.caption, 'map-lbl', 'map-type-lbl', Infinity, belowPlace)
           : labelStack(j.label, j.lines, j.caption, 'map-lbl', 'map-type-lbl', Infinity, Infinity, belowPlace, belowPlace)) +
         `</g></g>`;
  });
  $('#map-wrap').innerHTML = `<svg id="map-svg" class="${reduceMotion ? 'reduce-motion' : ''}" viewBox="0 0 ${W.toFixed(0)} ${H.toFixed(0)}" preserveAspectRatio="xMidYMid meet">${p}${dpM}${dpL}</svg>`;
  if (tidyGeom) { const s0 = $('#map-svg'); if (s0) s0.style.height = tidyCh + 'px'; }
  if (sceneMode) { const s0 = $('#map-svg'); if (s0) s0.dataset.tidyScene = '1'; }
  if (tidyGeom && tidyGeom.compactVariant) { const s0 = $('#map-svg'); if (s0) s0.dataset.pinCompact = '1'; }
  mapFit = { x: 0, y: 0, w: W, h: H };
  mapVB = { ...mapFit };
  mapApplyView($('#map-svg'));
  const svg = $('#map-svg');
  wireMapView(svg);
  document.querySelectorAll('[data-mapdev]').forEach(g => {
    if (g.dataset.mapdev) g.onclick = () => mapDeviceClicked(g);
  });
  document.querySelectorAll('[data-grpmap]').forEach(g => {
    g.style.cursor = 'pointer';
    g.onclick = () => selectMap(+g.dataset.grpmap);
  });
  wireMapHover(svg);
  renderMapLegend(objects);
  wireMapEdit(svg);
}

// ---- map hover card (the legacy system shows a brief status message
// when the cursor rests on a device: host name, address, status) ----
function showMapTip(o, clientX, clientY) {
  const tip = $('#map-tip');
  if (!tip) return;
  const st = o.device_status || 'unknown';
  const stLabel = st === 'up' ? 'Up' : st === 'down' ? 'Down' : 'Unknown';
  tip.innerHTML = `<div class="tip-name">${esc(o.device_name || 'Device')}<span class="tip-status st-${st}">${stLabel}</span></div>` +
    `<div>${esc(o.device_ip || '')}${o.device_type ? ' · ' + esc(o.device_type) : ''}</div>` +
    `<div class="muted">Last change: ${o.last_change ? fmtTime(o.last_change) : 'no state change recorded yet'}</div>` +
    `<div class="muted">Click to open the device</div>`;
  tip.classList.remove('hidden');
  const x = Math.min(clientX + 16, window.innerWidth - 300);
  const y = Math.min(clientY + 16, window.innerHeight - 150);
  tip.style.left = Math.max(4, x) + 'px';
  tip.style.top = Math.max(4, y) + 'px';
}
function hideMapTip() { const tip = $('#map-tip'); if (tip) tip.classList.add('hidden'); }
function wireMapHover(svg) {
  svg.addEventListener('pointermove', e => {
    // Labels live in their own layer above the markers (v3.4.3);
    // both layers carry data-mapdev and both answer the hover.
    const g = e.target && e.target.closest ? e.target.closest('[data-mapdev]') : null;
    if (!g || !g.dataset.mapdev) { hideMapTip(); return; }
    const o = currentMapObjects.find(x => x.type === 'Device' && x.device_id === +g.dataset.mapdev);
    if (o) showMapTip(o, e.clientX, e.clientY); else hideMapTip();
  });
  svg.addEventListener('pointerleave', hideMapTip);
  svg.addEventListener('pointerdown', hideMapTip);
}

function renderMapLegend(objects) {
  const el = $('#map-legend');
  if (!el) return;
  const seen = new Map();
  for (const o of objects) {
    if (o.type === 'Device' && o.icon_id && !seen.has(o.icon_id)) {
      seen.set(o.icon_id, o.device_type || 'Device');
    }
  }
  let html = '<span class="legend-title">Legend</span>';
  const markerSample = (status, label) =>
    `<span class="legend-item"><svg width="26" height="26" viewBox="-30 -30 60 60">${markerSVG(status)}</svg> ${label}</span>`;
  html += markerSample('up', 'Device up');
  html += markerSample('down', 'Device down');
  html += markerSample('unknown', 'State not known yet');
  for (const [icon, label] of seen) {
    html += `<span class="legend-item">${iconChip(icon, 20)} ${esc(label)}</span>`;
  }
  const lineSample = (cls, col) => `<svg width="30" height="10" viewBox="0 0 30 10"><line x1="2" y1="5" x2="28" y2="5" stroke="${col}" stroke-width="2.6" stroke-linecap="round"/></svg>`;
  html += `<span class="legend-item">${lineSample('', LINE_UP)} Link up — traffic flowing</span>`;
  html += `<span class="legend-item">${lineSample('', LINE_DOWN)} A device on the link is down</span>`;
  html += `<span class="legend-item">${lineSample('', '#3d7bff')} Drawn link (as in the legacy system)</span>`;
  el.innerHTML = html;
}

// ---- Map Edit Mode (v3.7) --------------------------------------
// An administrator can edit the open map itself: place a device on
// it (an existing device, or a brand-new one created in the panel),
// drag a device marker to a new stored position (the lines
// attached to it follow — the server rewrites their stored
// endpoints), remove a device from the map without deleting it,
// edit / disable / delete the device itself, and add or delete
// link lines between two devices. Editing always acts on the
// stored (faithful) positions, so entering Edit Mode switches Tidy
// View off. Viewers never see any of this: the Edit Map button is
// admin-only in the markup, the handlers below refuse to run for a
// viewer, and the server refuses viewer mutations regardless.
const mapEdit = {
  on: false,
  mapId: null,        // the map the current selection belongs to
  selDevice: null,    // {objId, deviceId} of the selected marker
  selDeviceData: null, // the selected device's fresh record
  selLine: null,      // object id of the selected line
  linkMode: false,    // Add Link is armed
  linkFirst: null,    // first device object picked for a new link
};

function setMapEditStatus(msg) {
  const el = $('#map-edit-status');
  if (el) el.textContent = msg || '';
}
function syncLinkBtn() {
  const b = $('#map-add-link-btn');
  if (b) {
    b.classList.toggle('active', mapEdit.linkMode);
    b.textContent = mapEdit.linkMode ? 'Cancel Add Link' : 'Add Link';
  }
}
// The stored coordinates of the centre of the current view — the
// placement point for a device added to the map.
function mapViewCentreStored() {
  if (mapVB && mapScene) {
    return { x: mapVB.x + mapVB.w / 2 + mapScene.minX, y: mapScene.maxY - (mapVB.y + mapVB.h / 2) };
  }
  if (currentMapMeta) {
    return { x: (currentMapMeta.min_x + currentMapMeta.max_x) / 2, y: (currentMapMeta.min_y + currentMapMeta.max_y) / 2 };
  }
  return { x: 0, y: 0 };
}

async function enterMapEdit() {
  if (!IS_ADMIN() || currentMapId == null) return;
  mapEdit.on = true;
  mapEdit.selDevice = null; mapEdit.selLine = null;
  mapEdit.linkFirst = null; mapEdit.linkMode = false; syncLinkBtn();
  if (mapTidy) { mapTidy = false; syncTidyBtns(); }
  setMapEditStatus('');
  renderMap(currentMapId);
}
function exitMapEdit() {
  mapEdit.on = false;
  mapEdit.selDevice = null; mapEdit.selLine = null;
  mapEdit.linkFirst = null; mapEdit.linkMode = false; syncLinkBtn();
  const panel = $('#map-sel-panel');
  if (panel) { panel.classList.add('hidden'); panel.innerHTML = ''; }
  if (currentMapId != null) renderMap(currentMapId);
}
$('#map-edit-btn') && ($('#map-edit-btn').onclick = () => { mapEdit.on ? exitMapEdit() : enterMapEdit(); });
$('#map-edit-done') && ($('#map-edit-done').onclick = () => exitMapEdit());
$('#map-add-link-btn') && ($('#map-add-link-btn').onclick = () => {
  mapEdit.linkMode = !mapEdit.linkMode;
  mapEdit.linkFirst = null;
  document.querySelectorAll('.map-link-first').forEach(el => el.classList.remove('map-link-first'));
  syncLinkBtn();
  setMapEditStatus(mapEdit.linkMode ? 'Click the first device of the new link.' : '');
});

// Click dispatcher for device markers: outside Edit Mode a click
// opens the device, exactly as before; in Edit Mode it selects the
// device (or picks a link endpoint while Add Link is armed).
function mapDeviceClicked(g) {
  const deviceId = +g.dataset.mapdev;
  if (!mapEdit.on) { if (deviceId) openDevice(deviceId); return; }
  const objId = +(g.dataset.mapobj || 0);
  const obj = currentMapObjects.find(o => o.id === objId && o.type === 'Device') ||
    currentMapObjects.find(o => o.type === 'Device' && o.device_id === deviceId);
  if (!obj) return;
  if (mapEdit.linkMode) { mapLinkPick(obj); return; }
  mapEdit.selDevice = { objId: obj.id, deviceId: obj.device_id };
  mapEdit.selLine = null;
  renderMapSelPanel();
}

function mapLinkPick(obj) {
  if (!mapEdit.linkFirst) {
    mapEdit.linkFirst = obj;
    const marker = document.querySelector(`g.map-dev[data-mapobj="${obj.id}"]`);
    if (marker) marker.classList.add('map-link-first');
    setMapEditStatus(`Link start: ${obj.device_name || 'device'} — now click the second device.`);
    return;
  }
  const first = mapEdit.linkFirst;
  mapEdit.linkFirst = null;
  document.querySelectorAll('.map-link-first').forEach(el => el.classList.remove('map-link-first'));
  if (first.device_id === obj.device_id) {
    setMapEditStatus('A link needs two different devices.');
    return;
  }
  (async () => {
    try {
      await api(`/api/maps/${currentMapId}/objects`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ type: 'Line', from_device_id: first.device_id, to_device_id: obj.device_id }),
      });
      mapEdit.linkMode = false; syncLinkBtn();
      setMapEditStatus('Link added.');
      renderMap(currentMapId);
    } catch (err) { alert(err.message); }
  })();
}

function selectMapLine(lineId) {
  mapEdit.selLine = lineId;
  mapEdit.selDevice = null;
  renderMapSelPanel();
}

// The selection panel inside the edit panel: the selected device's
// actions, or the selected link's delete action.
async function renderMapSelPanel() {
  const panel = $('#map-sel-panel');
  if (!panel) return;
  document.querySelectorAll('.map-dev-selected').forEach(el => el.classList.remove('map-dev-selected'));
  document.querySelectorAll('line.map-line-selected').forEach(el => el.classList.remove('map-line-selected'));
  if (mapEdit.selDevice) {
    const { objId, deviceId } = mapEdit.selDevice;
    const marker = document.querySelector(`g.map-dev[data-mapobj="${objId}"]`);
    if (marker) marker.classList.add('map-dev-selected');
    let d = null;
    try { d = await api('/api/devices/' + deviceId); } catch (e) { /* panel stays hidden */ }
    if (!d || !mapEdit.selDevice || mapEdit.selDevice.objId !== objId) { panel.classList.add('hidden'); return; }
    mapEdit.selDeviceData = d;
    panel.innerHTML = `<span class="sel-name">${esc(d.name)}</span>` +
      `<span class="muted">${esc(d.ip)}${d.monitoring_disabled ? ' · Monitoring off' : ''}</span>` +
      `<button type="button" id="msel-open">Open Device</button>` +
      `<button type="button" id="msel-edit">Edit Device</button>` +
      `<button type="button" id="msel-mon">${d.monitoring_disabled ? 'Enable Monitoring' : 'Disable Monitoring'}</button>` +
      `<button type="button" id="msel-remove">Remove from Map</button>` +
      `<button type="button" id="msel-delete">Delete Device</button>`;
    panel.classList.remove('hidden');
    $('#msel-open').onclick = () => openDevice(deviceId);
    $('#msel-edit').onclick = async () => { await openDevice(deviceId); $('#edit-panel').classList.remove('hidden'); };
    $('#msel-mon').onclick = toggleMapDeviceMonitoring;
    $('#msel-remove').onclick = removeSelectedFromMap;
    $('#msel-delete').onclick = deleteSelectedDevice;
    return;
  }
  if (mapEdit.selLine != null) {
    const o = currentMapObjects.find(x => x.id === mapEdit.selLine && x.type === 'Line');
    const el = document.querySelector(`line.map-line[data-line="${mapEdit.selLine}"]`);
    if (el) el.classList.add('map-line-selected');
    const nameOf = id => {
      const dev = currentMapObjects.find(x => x.type === 'Device' && x.device_id === id);
      return dev ? (dev.device_name || ('Device ' + id)) : null;
    };
    const a = o && o.line_from_device ? nameOf(o.line_from_device) : null;
    const b = o && o.line_to_device ? nameOf(o.line_to_device) : null;
    panel.innerHTML = `<span class="sel-name">Link</span>` +
      `<span class="muted">${a && b ? esc(a) + ' — ' + esc(b) : 'Drawn link (its ends are not matched to devices)'}</span>` +
      `<button type="button" id="msel-dellink">Delete Link</button>`;
    panel.classList.remove('hidden');
    $('#msel-dellink').onclick = deleteSelectedLink;
    return;
  }
  panel.classList.add('hidden');
  panel.innerHTML = '';
}

// Disable/Enable Monitoring from the map: the device's fresh
// record is read back and saved with only the flag flipped — the
// same semantics as the Devices page and the device page switch.
async function toggleMapDeviceMonitoring() {
  const sel = mapEdit.selDevice;
  if (!sel) return;
  let d = null;
  try { d = await api('/api/devices/' + sel.deviceId); } catch (e) { return; }
  const body = {
    name: d.name, ip: d.ip, type: d.type, group_id: d.group_id || null,
    community: d.community || 'public', snmp_disabled: !!d.snmp_disabled,
    snmp_version: d.snmp_version || 'v2c',
    v3_sec_name: d.v3_sec_name || '', v3_sec_level: d.v3_sec_level || '',
    v3_auth_proto: d.v3_auth_proto || '', v3_auth_pass: d.v3_auth_pass || '',
    v3_priv_proto: d.v3_priv_proto || '', v3_priv_pass: d.v3_priv_pass || '',
    monitoring_disabled: !d.monitoring_disabled,
  };
  try {
    await api('/api/devices/' + sel.deviceId, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    setMapEditStatus(d.monitoring_disabled ? `Monitoring enabled for ${d.name}.` : `Monitoring disabled for ${d.name}.`);
    updateLiveSummary();
    renderMap(currentMapId);
  } catch (err) { alert(err.message); }
}

async function removeSelectedFromMap() {
  const sel = mapEdit.selDevice;
  if (!sel) return;
  if (!confirm('Remove this device from this map? The device itself is not deleted.')) return;
  try {
    await api(`/api/maps/${currentMapId}/objects/${sel.objId}`, { method: 'DELETE' });
    mapEdit.selDevice = null;
    setMapEditStatus('Device removed from the map. The device itself was not deleted.');
    renderMap(currentMapId);
  } catch (err) { alert(err.message); }
}

async function deleteSelectedDevice() {
  const sel = mapEdit.selDevice;
  if (!sel) return;
  if (!confirm('Delete this device and its history? This cannot be undone.')) return;
  try {
    await api('/api/devices/' + sel.deviceId, { method: 'DELETE' });
    mapEdit.selDevice = null;
    setMapEditStatus('Device deleted.');
    updateLiveSummary();
    renderMap(currentMapId);
  } catch (err) { alert(err.message); }
}

async function deleteSelectedLink() {
  if (mapEdit.selLine == null) return;
  if (!confirm('Delete this link?')) return;
  try {
    await api(`/api/maps/${currentMapId}/objects/${mapEdit.selLine}`, { method: 'DELETE' });
    mapEdit.selLine = null;
    setMapEditStatus('Link deleted.');
    renderMap(currentMapId);
  } catch (err) { alert(err.message); }
}

// The Add Device controls: the dropdown lists exactly the devices
// that are not on this map yet; the group dropdown of the
// create-new form mirrors the Devices page's.
async function refreshMapAddControls() {
  if (!mapEdit.on) return;
  try {
    const devs = await api('/api/devices');
    const onMap = new Set(currentMapObjects.filter(o => o.type === 'Device' && o.device_id).map(o => o.device_id));
    const sel = $('#map-add-select');
    if (sel) {
      const off = devs.filter(d => !onMap.has(d.id));
      sel.innerHTML = off.length
        ? '<option value="">Choose a device…</option>' + off.map(d => `<option value="${d.id}">${esc(d.name)} (${esc(d.ip)})</option>`).join('')
        : '<option value="">Every device is already on this map</option>';
    }
    const gsel = $('#map-new-group-select');
    if (gsel && gsel.options.length <= 1) {
      const gs = await api('/api/groups').catch(() => []);
      gsel.innerHTML = '<option value="">No group</option>' + gs.map(g => `<option value="${g.id}">${esc(g.name)}</option>`).join('');
    }
  } catch (e) { /* best effort: the panel's other controls still work */ }
}

$('#map-add-existing-form') && ($('#map-add-existing-form').onsubmit = async e => {
  e.preventDefault();
  const id = +($('#map-add-select').value || 0);
  if (!id) { setMapEditStatus('Choose a device first.'); return; }
  const c = mapViewCentreStored();
  try {
    await api(`/api/maps/${currentMapId}/objects`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ type: 'Device', device_id: id, x: c.x, y: c.y }),
    });
    setMapEditStatus('Device added to the map — drag it to where it belongs.');
    renderMap(currentMapId);
  } catch (err) { alert(err.message); }
});

$('#map-new-device-form') && ($('#map-new-device-form').onsubmit = async e => {
  e.preventDefault();
  const f = new FormData(e.target);
  const body = { name: f.get('name'), ip: f.get('ip'), type: f.get('type'), ...snmpFieldsFromForm(f) };
  const g = f.get('group_id'); if (g) body.group_id = +g;
  try {
    const dev = await api('/api/devices', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    const c = mapViewCentreStored();
    await api(`/api/maps/${currentMapId}/objects`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ type: 'Device', device_id: dev.id, x: c.x, y: c.y }),
    });
    e.target.reset();
    const cv = e.target.elements.namedItem('community'); if (cv) cv.value = 'public';
    $('#map-new-snmp-version').value = 'v2c';
    $('#map-new-v3-fields').classList.add('hidden');
    setMapEditStatus(`Device ${dev.name || dev.ip} created and placed on the map — drag it to where it belongs.`);
    updateLiveSummary();
    renderMap(currentMapId);
  } catch (err) { alert(err.message); }
});
wireSnmpVersion('#map-new-snmp-version', '#map-new-v3-fields');

// Dragging a device marker in Edit Mode. The pan handler already
// leaves device markers alone (it skips [data-mapdev] targets), so
// a pointer-down on a marker or its label starts a drag instead;
// a press without movement stays a plain click (selection). The
// marker, its label and the attached lines follow the pointer in
// scene coordinates; on drop the new stored position is saved and
// the map re-renders from the database.
function startMapDeviceDrag(e, g, svg) {
  if (!mapEdit.on || e.button !== 0) return;
  const objId = +(g.dataset.mapobj || 0);
  const obj = currentMapObjects.find(o => o.id === objId && o.type === 'Device');
  if (!obj || !mapScene) return;
  e.preventDefault();
  hideMapTip();
  const pt = svg.createSVGPoint();
  const toScene = ev => { pt.x = ev.clientX; pt.y = ev.clientY; return pt.matrixTransform(svg.getScreenCTM().inverse()); };
  const start = toScene(e);
  const marker = svg.querySelector(`g.map-dev[data-mapobj="${objId}"]`);
  const label = svg.querySelector(`g.map-dev-lbl[data-mapobj="${objId}"]`);
  if (!marker) return;
  const origCx = +marker.dataset.cx, origCy = +marker.dataset.cy;
  const lines = currentMapObjects
    .filter(o => o.type === 'Line' && (o.line_from_device === obj.device_id || o.line_to_device === obj.device_id))
    .map(o => {
      const el = svg.querySelector(`line.map-line[data-line="${o.id}"]`);
      return el ? { o, el, x1: +el.getAttribute('x1'), y1: +el.getAttribute('y1'), x2: +el.getAttribute('x2'), y2: +el.getAttribute('y2') } : null;
    }).filter(Boolean);
  let moved = false, lastDx = 0, lastDy = 0;
  const onMove = ev => {
    const p = toScene(ev);
    const dx = p.x - start.x, dy = p.y - start.y;
    if (!moved && Math.hypot(dx, dy) < 2) return;
    moved = true; lastDx = dx; lastDy = dy;
    for (const grp of [marker, label]) {
      if (!grp) continue;
      grp.dataset.cx = (origCx + dx).toFixed(1);
      grp.dataset.cy = (origCy + dy).toFixed(1);
    }
    mapScaleDevices(svg);
    for (const L of lines) {
      if (L.o.line_from_device === obj.device_id) { L.el.setAttribute('x1', L.x1 + dx); L.el.setAttribute('y1', L.y1 + dy); }
      if (L.o.line_to_device === obj.device_id) { L.el.setAttribute('x2', L.x2 + dx); L.el.setAttribute('y2', L.y2 + dy); }
    }
  };
  const onUp = async () => {
    window.removeEventListener('pointermove', onMove);
    window.removeEventListener('pointerup', onUp);
    if (!moved) return;
    const nx = obj.x + lastDx, ny = obj.y - lastDy; // scene Y is the stored Y flipped
    try {
      await api(`/api/maps/${currentMapId}/objects/${objId}`, {
        method: 'PUT', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ x: nx, y: ny }),
      });
      setMapEditStatus(`Moved ${obj.device_name || 'device'} — the new position is saved.`);
    } catch (err) { alert(err.message); }
    renderMap(currentMapId);
  };
  window.addEventListener('pointermove', onMove);
  window.addEventListener('pointerup', onUp);
}

// wireMapEdit runs after every map render: it applies the editing
// affordances when Edit Mode is on for the open map, and makes
// sure they are nowhere to be seen when it is off.
function wireMapEdit(svg) {
  const wrap = $('#map-wrap');
  if (wrap) wrap.classList.toggle('editing', mapEdit.on);
  const panel = $('#map-edit-panel');
  if (panel) panel.classList.toggle('hidden', !mapEdit.on);
  const btn = $('#map-edit-btn');
  if (btn) btn.classList.toggle('active', mapEdit.on);
  if (!mapEdit.on || !svg) return;
  if (mapEdit.mapId !== currentMapId) {
    mapEdit.mapId = currentMapId;
    mapEdit.selDevice = null; mapEdit.selLine = null;
    mapEdit.linkFirst = null; mapEdit.linkMode = false; syncLinkBtn();
    const sp = $('#map-sel-panel');
    if (sp) { sp.classList.add('hidden'); sp.innerHTML = ''; }
  }
  const meta = $('#map-meta');
  if (meta && !meta.textContent.includes('Edit Mode is ON')) {
    meta.textContent += ' · Edit Mode is ON: changes are saved to the stored map.';
  }
  svg.querySelectorAll('line.map-line').forEach(l => {
    l.onclick = e => { e.stopPropagation(); selectMapLine(+l.dataset.line); };
  });
  svg.querySelectorAll('[data-mapobj]').forEach(g => {
    g.addEventListener('pointerdown', e => startMapDeviceDrag(e, g, svg));
  });
  renderMapSelPanel();
  refreshMapAddControls();
}

// Refresh only the status-driven styling on the visible map (no
// re-render, so pan/zoom and the flow animations keep running).
async function refreshMapStatus() {
  if (currentMapId == null || $('#view-maps').classList.contains('hidden')) return;
  try {
    const { objects } = await api('/api/maps/' + currentMapId);
    currentMapObjects = objects;
    for (const o of objects) {
      let extra = {};
      try { extra = JSON.parse(o.extra || '{}'); } catch (e) { }
      if (o.type === 'Line') {
        const el = document.querySelector(`line.map-line[data-line="${o.id}"]`);
        if (!el) continue;
        const st = o.line_state || 'unknown';
        el.setAttribute('class', `map-line map-line-${st}`);
        if (mapEdit.selLine === o.id) el.classList.add('map-line-selected');
        el.setAttribute('stroke', lineStroke(o, extra));
        continue;
      }
      if (o.type === 'DeviceGroup') {
        // Group markers follow their group's aggregate status.
        const g = document.querySelector(`g.map-grp-marker[data-grpobj="${o.id}"]`);
        if (!g) continue;
        const st = o.group_status || 'unknown';
        if ((g.dataset.st || '') !== st) {
          g.dataset.st = st;
          g.setAttribute('class', `map-grp-marker st-${st}`);
          const holder = g.querySelector('.dev-plate');
          if (holder) holder.innerHTML = markerSVG(st);
        }
        continue;
      }
      if (o.type !== 'Device' || !o.device_id) continue;
      const g = document.querySelector(`g.map-dev[data-mapdev="${o.device_id}"]`);
      if (!g) continue;
      const st = o.device_status || 'unknown';
      const wasDown = g.classList.contains('st-down');
      g.classList.toggle('st-down', st === 'down');
      g.classList.toggle('st-up', st === 'up');
      g.classList.toggle('st-unknown', st !== 'up' && st !== 'down');
      // Swap the status plate itself without re-rendering the
      // map, so pan/zoom and animations survive.
      const prevSt = g.dataset.st || '';
      if (prevSt !== st) {
        g.dataset.st = st;
        const holder = g.querySelector('.dev-plate');
        if (holder) holder.innerHTML = markerSVG(st);
      }
      if (st === 'down' && !wasDown) {
        // One-time expanding ring for a device that has just gone
        // down, inside its fixed-size group (radius in screen px).
        const fix = g.querySelector('.dev-fixed, .dev-scene');
        if (fix) {
          const ring = document.createElementNS('http://www.w3.org/2000/svg', 'circle');
          ring.classList.add('ring-once');
          ring.setAttribute('r', 30);
          fix.insertBefore(ring, fix.firstChild);
          setTimeout(() => ring.remove(), 1500);
        }
      }
    }
  } catch (e) { /* keep the current map on transient errors */ }
}
setInterval(refreshMapStatus, 15000);

// ---------- legacy import ----------
function legacyBody() {
  const f = new FormData($('#legacy-form'));
  return { server: f.get('server'), instance: f.get('instance'), database: f.get('database') || '',
           user: f.get('user'), password: f.get('password'), windows_auth: !!f.get('windows_auth') };
}
$('#legacy-preview-btn').onclick = async () => {
  $('#legacy-result').textContent = 'Connecting…';
  try { const p = await post('/api/legacy/preview', legacyBody()); $('#legacy-result').textContent = JSON.stringify(p, null, 2); }
  catch (e) { $('#legacy-result').textContent = 'Error: ' + e.message; }
};
$('#legacy-import-btn').onclick = async () => {
  if (!confirm('Import groups/devices/interfaces from this legacy database now?')) return;
  $('#legacy-result').textContent = 'Importing…';
  try { const rep = await post('/api/legacy/import', legacyBody()); uiTick(true); $('#legacy-result').textContent = JSON.stringify(rep, null, 2); }
  catch (e) { uiTick(false); $('#legacy-result').textContent = 'Error: ' + e.message; }
};

// ---------- legacy direct import (auto-detect) + backup file import ----------
let autoTarget = null;      // target whose preview is currently shown
let lastCandidates = [];    // last /api/legacy/detect result

$('#legacy-detect-btn').onclick = async () => {
  const list = $('#legacy-detect-list');
  list.textContent = 'Looking for SQL Server on this computer…';
  $('#legacy-auto-actions').classList.add('hidden');
  autoTarget = null;
  try {
    const res = await api('/api/legacy/detect');
    lastCandidates = res.candidates || [];
    renderDetectList();
    fillBakInstances();
  } catch (e) { list.textContent = 'Error: ' + e.message; }
};

function renderDetectList() {
  const list = $('#legacy-detect-list');
  list.innerHTML = '';
  if (!lastCandidates.length) {
    list.innerHTML = '<p class="muted">No SQL Server was found on this computer. The legacy system may not be ' +
      'installed here, or its SQL Server may be stopped. You can still import from a backup (.bak) file ' +
      'below, or use the Advanced manual connection.</p>';
    return;
  }
  let anyReady = false;
  for (const c of lastCandidates) {
    const row = document.createElement('div');
    row.className = 'detect-row';
    const grow = document.createElement('span');
    grow.className = 'grow';
    const b = document.createElement('b');
    b.textContent = c.display_name;
    const s = document.createElement('span');
    s.className = 'muted';
    s.textContent = ' — ' + (c.has_legacy_db ? 'legacy database found — ready to import' : (c.detail || ''));
    grow.appendChild(b); grow.appendChild(s);
    row.appendChild(grow);
    if (c.has_legacy_db) {
      anyReady = true;
      const btn = document.createElement('button');
      btn.textContent = 'Preview';
      btn.onclick = () => autoPreview(c.target);
      row.appendChild(btn);
    }
    list.appendChild(row);
  }
  if (!anyReady) {
    const p = document.createElement('p');
    p.className = 'muted';
    p.textContent = 'No legacy database could be opened automatically. You can import from a backup (.bak) ' +
      'file below, or use the Advanced manual connection with a SQL login.';
    list.appendChild(p);
    refreshBakEngine();
  }
}

async function autoPreview(target) {
  autoTarget = target;
  $('#legacy-result').textContent = 'Connecting…';
  $('#legacy-auto-actions').classList.add('hidden');
  try {
    const p = await post('/api/legacy/import-auto', { auto_target: target });
    $('#legacy-result').textContent = JSON.stringify(p, null, 2);
    $('#legacy-auto-actions').classList.remove('hidden');
  } catch (e) { $('#legacy-result').textContent = 'Error: ' + e.message; }
}

$('#legacy-auto-import-btn').onclick = async () => {
  if (!autoTarget) return;
  if (!confirm('Import groups, devices, interfaces, monitors and maps from this legacy database now?')) return;
  $('#legacy-result').textContent = 'Importing…';
  try { const rep = await post('/api/legacy/import', { auto_target: autoTarget }); uiTick(true); $('#legacy-result').textContent = JSON.stringify(rep, null, 2); }
  catch (e) { uiTick(false); $('#legacy-result').textContent = 'Error: ' + e.message; }
};

function fillBakInstances() {
  const sel = $('#legacy-bak-instance');
  sel.innerHTML = '<option value="">Automatic — first SQL Server found</option>';
  for (const c of lastCandidates) {
    if (!c.can_connect && !c.reachable) continue;
    const o = document.createElement('option');
    o.value = JSON.stringify(c.target);
    o.textContent = c.display_name;
    sel.appendChild(o);
  }
}

$('#legacy-bak-btn').onclick = async () => {
  const path = $('#legacy-bak-path').value.trim();
  if (!path) { $('#legacy-result').textContent = 'Please enter the full path of the backup (.bak) file first.'; return; }
  const body = { path };
  const manualServer = $('#legacy-bak-server').value.trim();
  if (manualServer) {
    // A typed server wins over auto-detection; the connection details
    // are used by the server for this request only and never stored.
    body.manual = { server: manualServer, auth: $('#legacy-bak-auth').value,
                    user: $('#legacy-bak-user').value.trim(), password: $('#legacy-bak-password').value };
  } else {
    const v = $('#legacy-bak-instance').value;
    if (v) { try { body.auto_target = JSON.parse(v); } catch (e) { /* keep automatic */ } }
  }
  if (!confirm('Restore this backup into a temporary database and import from it now? The temporary database is deleted again afterwards.')) return;
  $('#legacy-result').textContent = 'Restoring and importing… a large backup can take a minute or two.';
  // Direct fetch (not post()) so a structured situation error keeps its
  // per-candidate detail lines; they are rendered multi-line below.
  try {
    const r = await fetch('/api/legacy/import-bak', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    const data = await r.json().catch(() => ({}));
    if (!r.ok) {
      uiTick(false);
      let txt = 'Error: ' + (data.error || r.statusText);
      if (Array.isArray(data.candidates) && data.candidates.length) {
        txt += '\n\nWhat NetGold found on this computer:';
        for (const c of data.candidates) txt += '\n- ' + c.display_name + (c.detail ? ' — ' + c.detail : '');
      }
      $('#legacy-result').textContent = txt;
      // ANY backup-import failure re-checks the engine situation: if
      // automatic setup is available on this computer, its panel is
      // (re)shown — never gated on one specific error kind.
      refreshBakEngine();
      return;
    }
    uiTick(true);
    $('#legacy-result').textContent = JSON.stringify(data, null, 2);
    refreshBakEngine();
  } catch (e) { uiTick(false); $('#legacy-result').textContent = 'Error: ' + e.message; refreshBakEngine(); }
};

// ---------- automatic SQL engine setup (backup import) ----------
// The Backup File card checks the SQL engine situation PROACTIVELY —
// as soon as the Import view opens, and again after detection and
// after every backup import attempt — and renders the engine status
// line plus, when no usable SQL Server exists, the setup panel (the
// "Set Up Engine and Import" offer on Windows, or the honest guidance
// where setup cannot run). It never waits for one specific failure
// first (v2.2.1: in v2.2 the offer only appeared deep inside one
// error-handling path, so any break in that chain — for example a
// stale cached copy of this very script — made it invisible).
const ENGINE_STEPS = [
  ['checking', 'Checking this computer'],
  ['downloading', 'Downloading the SQL engine from Microsoft'],
  ['verifying', 'Verifying the download (Microsoft signature)'],
  ['installing', 'Installing the SQL engine'],
  ['repairing', 'Repairing the SQL engine installation'],
  ['creating_instance', 'Creating the NetGold database instance'],
  ['restoring', 'Restoring the backup file'],
  ['importing', 'Importing into NetGold'],
];
let enginePollTimer = null;

// engineStatusText renders the one-line engine state shown at the
// top of the Backup File card, so the situation is visible (and
// screenshot-able) without clicking anything.
function engineStatusText(st) {
  if (st.usable_sql) {
    return 'SQL engine: ready — a usable SQL Server was found on this computer.';
  }
  if (st.localdb_installed) {
    const s = st.localdb_instance_state ? ' — instance "NetGold" is ' + st.localdb_instance_state.toLowerCase() : '';
    return 'SQL engine: no general SQL Server found; Microsoft LocalDB is installed' + s + '.';
  }
  return 'SQL engine: no usable SQL Server was found on this computer.';
}

async function refreshBakEngine() {
  const statusEl = $('#legacy-engine-status');
  let st;
  try { st = await api('/api/legacy/bak-engine'); }
  catch (e) {
    // Say so on the status line instead of failing silently, and
    // leave the panel as it is — a later refresh may succeed.
    if (statusEl) statusEl.textContent = 'SQL engine: the status check failed (' + e.message + ').';
    return;
  }
  if (statusEl) statusEl.textContent = engineStatusText(st);
  const panel = $('#legacy-engine-panel');
  const btn = $('#legacy-engine-btn');
  const repairManual = $('#legacy-engine-repair-manual');
  // The manual repair action belongs wherever engine setup can run.
  if (repairManual) {
    if (st.setup_supported && !st.running_as_service) repairManual.classList.remove('hidden');
    else repairManual.classList.add('hidden');
  }
  if (st.usable_sql) { panel.classList.add('hidden'); return; }
  // No usable SQL Server: the panel belongs on screen — with the
  // setup offer where setup can run, or the honest guidance where
  // it cannot (non-Windows platform, or the Windows service).
  panel.classList.remove('hidden');
  if (!st.setup_supported) {
    $('#legacy-engine-text').textContent = 'No usable SQL Server was found. Automatic engine setup is only available ' +
      'in the Windows program. On this platform, start any local SQL Server and try the import again, or use the ' +
      'Advanced manual connection below to import from a live legacy database.';
    btn.classList.add('hidden');
    return;
  }
  if (st.running_as_service) {
    $('#legacy-engine-text').textContent = 'No usable SQL Server was found. Automatic engine setup cannot run while ' +
      'NetGold runs as a Windows service, because a service cannot show installer or administrator (UAC) prompts. ' +
      'Run the NetGold desktop program once and start the setup from there — afterwards the service can use the ' +
      'installed engine for normal imports.';
    btn.classList.add('hidden');
    return;
  }
  const mb = Math.round((st.download_bytes || 0) / 1000000);
  $('#legacy-engine-text').textContent = 'No usable SQL Server was found. NetGold can set up Microsoft\'s free SQL engine ' +
    'and import your backup automatically: the lightweight LocalDB engine (about ' + mb + ' MB download from Microsoft) when this ' +
    'Windows sign-in can be used to prepare it, or the full SQL Server Express engine running as a Windows service otherwise. ' +
    'Windows will ask for administrator approval during the install.' +
    (st.bundled_installer ? ' This installed copy of NetGold also carries the LocalDB engine installer, so that setup works even without an internet connection.' : '') +
    ' If an earlier setup left the engine broken, "Repair SQL Engine" below removes the LocalDB leftovers and installs it cleanly.';
  btn.classList.remove('hidden');
}

$('#legacy-engine-btn').onclick = async () => {
  const path = $('#legacy-bak-path').value.trim();
  if (!path) { $('#legacy-result').textContent = 'Please enter the full path of the backup (.bak) file first.'; return; }
  $('#legacy-engine-btn').disabled = true;
  $('#legacy-engine-steps').innerHTML = '';
  $('#legacy-engine-note').textContent = '';
  try {
    await post('/api/legacy/bak-setup', { path, accept_download: true });
    pollEngineStatus();
  } catch (e) {
    $('#legacy-engine-note').textContent = 'Error: ' + e.message;
    $('#legacy-engine-btn').disabled = false;
  }
};

function pollEngineStatus() {
  clearTimeout(enginePollTimer);
  const tick = async () => {
    let st;
    try { st = await api('/api/legacy/bak-setup/status'); }
    catch (e) { enginePollTimer = setTimeout(tick, 2000); return; }
    renderEngineSteps(st);
    if (st.step === 'done') {
      uiTick(true);
      $('#legacy-engine-repair').classList.add('hidden');
      if (st.report) $('#legacy-result').textContent = JSON.stringify(st.report, null, 2);
      $('#legacy-engine-note').textContent = st.note || '';
      if (st.can_remove_instance) $('#legacy-engine-remove').classList.remove('hidden');
      $('#legacy-engine-btn').disabled = false;
      return;
    }
    if (st.step === 'failed') {
      uiTick(false);
      $('#legacy-engine-note').textContent = st.error || 'Setup failed.';
      $('#legacy-result').textContent = 'Error: ' + (st.error || 'Setup failed.');
      $('#legacy-engine-btn').disabled = false;
      // A failed install or a broken engine is what the inbuilt
      // repair exists for: offer it right here, one click, and the
      // import continues automatically after a successful repair.
      if (st.can_repair) {
        $('#legacy-engine-repair').classList.remove('hidden');
        $('#legacy-engine-note').textContent = (st.error || 'Setup failed.') +
          '\nNetGold can repair this itself: "Repair and Retry" removes the LocalDB leftovers from earlier setups, installs the engine cleanly (one administrator approval), and then continues your import automatically.';
      }
      return;
    }
    enginePollTimer = setTimeout(tick, 1200);
  };
  tick();
}

function renderEngineSteps(st) {
  const ul = $('#legacy-engine-steps');
  ul.innerHTML = '';
  const effStep = st.step === 'failed' ? st.failed_step : st.step;
  const curIdx = ENGINE_STEPS.findIndex(([k]) => k === effStep);
  for (let i = 0; i < ENGINE_STEPS.length; i++) {
    const [key, label] = ENGINE_STEPS[i];
    const li = document.createElement('li');
    let mark = '•', cls = 'muted';
    if (st.step === 'done' || i < curIdx) { mark = '✓'; cls = ''; }
    else if (st.step === 'failed' && key === st.failed_step) { mark = '✗'; cls = ''; }
    else if (i === curIdx) { mark = '…'; cls = ''; }
    li.className = cls;
    li.textContent = mark + ' ' + label +
      (key === 'downloading' && i === curIdx && st.download_percent ? ' — ' + st.download_percent + '%' : '');
    ul.appendChild(li);
  }
  // The repair reports its current action as a live detail line.
  if (st.detail) {
    const li = document.createElement('li');
    li.textContent = '… ' + st.detail;
    ul.appendChild(li);
  }
  // Pre-flight warnings (pending restart, low disk space) stay
  // visible under the steps for the whole job.
  for (const w of (st.warnings || [])) {
    const li = document.createElement('li');
    li.textContent = '⚠ ' + w;
    ul.appendChild(li);
  }
}

// startRepairJob launches the repair-and-retry job: with the backup
// path from the form when one is entered (the import then continues
// automatically), or as an engine-only repair when it is empty.
async function startRepairJob() {
  const path = $('#legacy-bak-path').value.trim();
  $('#legacy-engine-repair').classList.add('hidden');
  $('#legacy-engine-btn').disabled = true;
  $('#legacy-engine-steps').innerHTML = '';
  $('#legacy-engine-note').textContent = '';
  try {
    await post('/api/legacy/bak-repair', { path });
    pollEngineStatus();
  } catch (e) {
    $('#legacy-engine-note').textContent = 'Error: ' + e.message;
    $('#legacy-engine-btn').disabled = false;
  }
}

$('#legacy-engine-repair').onclick = startRepairJob;
$('#legacy-engine-repair-manual').onclick = () => {
  if (!confirm('Repair the SQL engine installation now? NetGold removes leftover LocalDB pieces from earlier setups (your legacy database and any real SQL Server are not touched), installs the engine cleanly, and recreates its instance. Windows will ask for administrator approval once.')) return;
  startRepairJob();
};

$('#legacy-engine-remove').onclick = async () => {
  if (!confirm('Stop and remove the NetGold LocalDB instance? The LocalDB program itself stays installed (removable via Windows Settings > Apps).')) return;
  try {
    const res = await post('/api/legacy/bak-engine/remove-instance', {});
    $('#legacy-engine-note').textContent = res.note;
    $('#legacy-engine-remove').classList.add('hidden');
  } catch (e) { $('#legacy-engine-note').textContent = 'Error: ' + e.message; }
};

// ---------- alarms: sound + desktop notification on device down/up ----------
// Sounds are generated with the WebAudio API (no audio files). Browsers only
// allow audio after a first user gesture, so the context unlocks on the
// first click/keypress anywhere; until then a hint is shown in the header.
const ALARM = {
  ctx: null, unlocked: false,
  soundOn: localStorage.getItem('netgold.alarmSound') !== 'off', // default ON
  notifOn: localStorage.getItem('netgold.notif') === 'on',
  pack: localStorage.getItem('netgold.soundPack') || 'siren', // siren | chime | beeps
  volume: (() => { const v = parseFloat(localStorage.getItem('netgold.soundVol')); return isNaN(v) ? 0.8 : Math.min(1, Math.max(0, v)); })(),
  badgeCount: 0,
  lastId: 0,
  downSince: JSON.parse(localStorage.getItem('netgold.downSince') || '{}'),
  acked: new Set(JSON.parse(localStorage.getItem('netgold.acked') || '[]')),
  activeDown: new Map(), // eventId -> deviceId: unacknowledged outages still down
  sirenTimer: null,
  recent: [],            // recent events (ascending) for the alarms panel
};
const saveDownSince = () => localStorage.setItem('netgold.downSince', JSON.stringify(ALARM.downSince));
const saveAcked = () => localStorage.setItem('netgold.acked', JSON.stringify([...ALARM.acked].slice(-300)));

function fmtDur(sec) {
  if (sec == null || sec < 0) sec = 0;
  const d = Math.floor(sec / 86400), h = Math.floor(sec % 86400 / 3600), m = Math.floor(sec % 3600 / 60), s = Math.floor(sec % 60);
  return d ? `${d}d ${h}h` : h ? `${h}h ${m}m` : m ? `${m}m ${s}s` : `${s}s`;
}

// legacy-style escalation labels for an ongoing outage, from the
// seeded MonitorState tiers (Down / at least 2 min / 5 min / 20 min).
function downTier(sec) {
  if (sec >= 1200) return 'Down at least 20 min';
  if (sec >= 300) return 'Down at least 5 min';
  if (sec >= 120) return 'Down at least 2 min';
  return 'Down';
}

function alarmTone(freq, at, dur, vol = 0.22, type = 'sine') {
  if (!ALARM.ctx) return;
  const t = ALARM.ctx.currentTime + at;
  const o = ALARM.ctx.createOscillator(), g = ALARM.ctx.createGain();
  o.type = type; o.frequency.value = freq;
  const v = Math.max(0.0001, vol * ALARM.volume);
  g.gain.setValueAtTime(0.0001, t);
  g.gain.exponentialRampToValueAtTime(v, t + 0.02);
  g.gain.exponentialRampToValueAtTime(0.0001, t + dur);
  o.connect(g).connect(ALARM.ctx.destination);
  o.start(t); o.stop(t + dur + 0.05);
}
// Down alarm, by selected sound pack (all WebAudio-synthesised):
//  siren — the classic repeating two-tone siren;
//  chime — a gentle two-note sine chime, repeated softly;
//  beeps — short square-wave digital beeps.
function playDownSiren() {
  if (!ALARM.unlocked || !ALARM.soundOn) return;
  if (ALARM.pack === 'chime') {
    for (let c = 0; c < 2; c++) {
      alarmTone(660, c * 0.85, 0.38, 0.16);
      alarmTone(494, c * 0.85 + 0.4, 0.42, 0.16);
    }
  } else if (ALARM.pack === 'beeps') {
    for (let c = 0; c < 3; c++) {
      alarmTone(1180, c * 0.3, 0.11, 0.13, 'square');
      alarmTone(1180, c * 0.3 + 0.15, 0.11, 0.13, 'square');
    }
  } else {
    for (let c = 0; c < 3; c++) {
      alarmTone(880, c * 0.44, 0.21);
      alarmTone(659, c * 0.44 + 0.22, 0.21);
    }
  }
}
// Up sound: distinct and pleasant in every pack.
function playUpSound() {
  if (!ALARM.unlocked || !ALARM.soundOn) return;
  if (ALARM.pack === 'chime') {
    alarmTone(587, 0, 0.3, 0.15);
    alarmTone(880, 0.22, 0.45, 0.15);
  } else if (ALARM.pack === 'beeps') {
    alarmTone(988, 0, 0.1, 0.12, 'square');
    alarmTone(1319, 0.13, 0.16, 0.12, 'square');
  } else {
    alarmTone(740, 0, 0.13, 0.18);
    alarmTone(988, 0.16, 0.2, 0.18);
  }
}
// Very quiet, very short UI feedback sounds (success tick / error tone).
// Only when sound is on and audio is unlocked; never on page load.
function uiTick(ok) {
  if (!ALARM.unlocked || !ALARM.soundOn || !ALARM.ctx) return;
  if (ok) {
    alarmTone(1250, 0, 0.06, 0.05);
  } else {
    alarmTone(220, 0, 0.09, 0.07);
    alarmTone(174, 0.11, 0.13, 0.07);
  }
}
function ensureSiren() {
  const shouldRun = ALARM.soundOn && ALARM.activeDown.size > 0;
  if (shouldRun && !ALARM.sirenTimer) {
    playDownSiren();
    ALARM.sirenTimer = setInterval(playDownSiren, 5000);
  } else if (!shouldRun && ALARM.sirenTimer) {
    clearInterval(ALARM.sirenTimer);
    ALARM.sirenTimer = null;
  }
}
function unlockAudio() {
  if (ALARM.unlocked) return;
  try {
    ALARM.ctx = ALARM.ctx || new (window.AudioContext || window.webkitAudioContext)();
    if (ALARM.ctx.state === 'suspended') ALARM.ctx.resume();
    ALARM.unlocked = ALARM.ctx.state === 'running';
  } catch (e) { /* no audio available — toasts/notifications still work */ }
  updateAlarmButtons();
  if (ALARM.unlocked) ensureSiren();
}
window.addEventListener('pointerdown', unlockAudio);
window.addEventListener('keydown', unlockAudio);

function updateAlarmButtons() {
  const sb = $('#alarm-sound-btn'), nb = $('#alarm-notif-btn'), hint = $('#sound-hint');
  sb.textContent = ALARM.soundOn ? '🔔 Alarm sound: On' : '🔕 Alarm sound: Off';
  sb.classList.toggle('off', !ALARM.soundOn);
  nb.textContent = ALARM.notifOn ? 'Desktop alerts: On' : 'Desktop alerts: Off';
  nb.classList.toggle('off', !ALARM.notifOn);
  hint.classList.toggle('hidden', !(ALARM.soundOn && !ALARM.unlocked));
  const pack = $('#sound-pack'), vol = $('#sound-vol');
  if (pack) pack.value = ALARM.pack;
  if (vol) vol.value = Math.round(ALARM.volume * 100);
}

// Alarm badge in the top bar: unacknowledged outage count, with a brief
// scale bounce when it increases.
function updateAlarmBadge() {
  const badge = $('#alarm-badge');
  if (!badge) return;
  const n = ALARM.activeDown.size;
  badge.textContent = n;
  badge.classList.toggle('hidden', n === 0);
  if (n > ALARM.badgeCount) {
    badge.classList.remove('bounce');
    void badge.offsetWidth; // restart the animation
    badge.classList.add('bounce');
  }
  ALARM.badgeCount = n;
}
function notify(title, body) {
  if (!ALARM.notifOn || !('Notification' in window) || Notification.permission !== 'granted') return;
  try { new Notification(title, { body }); } catch (e) { /* ignore */ }
}

// --- toasts ---
function dismissToast(eventId) {
  const el = document.querySelector(`.toast[data-event="${eventId}"]`);
  if (el) el.remove();
}
function showToast(kind, title, body, eventId, sticky) {
  const box = $('#toasts');
  const el = document.createElement('div');
  el.className = 'toast ' + kind;
  if (eventId != null) el.dataset.event = eventId;
  el.innerHTML = `<div class="t-body"><div class="t-title">${esc(title)}</div><div>${esc(body)}</div></div>`;
  if (sticky) {
    const b = document.createElement('button');
    b.textContent = 'Acknowledge';
    b.className = 'admin-only';
    b.onclick = () => ackAlarm(eventId);
    el.appendChild(b);
  } else {
    const b = document.createElement('button');
    b.textContent = '✕';
    b.onclick = () => el.remove();
    el.appendChild(b);
    setTimeout(() => el.remove(), 12000);
  }
  box.appendChild(el);
  return el;
}
function ackAlarm(eventId) {
  ALARM.acked.add(eventId);
  saveAcked();
  ALARM.activeDown.delete(eventId);
  dismissToast(eventId);
  ensureSiren();
  updateAlarmBadge();
  renderAlarms();
}

// --- event processing ---
const eventLabel = e => e.device_name || e.message || ('Device ' + e.device_id);
function handleAlarmEvent(e) {
  if (e.kind !== 'down' && e.kind !== 'up') return;
  if (e.kind === 'down') {
    if (e.device_id != null && !ALARM.downSince[e.device_id]) { ALARM.downSince[e.device_id] = e.ts; saveDownSince(); }
    // A duplicate down event for a device that already has an active alarm
    // (possible when two polls race) must not stack a second banner/siren.
    let alreadyAlarming = false;
    for (const did of ALARM.activeDown.values()) if (did === e.device_id) alreadyAlarming = true;
    if (!ALARM.acked.has(e.id) && !alreadyAlarming) {
      ALARM.activeDown.set(e.id, e.device_id);
      showToast('down', `🔴 ${eventLabel(e)} is DOWN`, `Since ${fmtTime(e.ts)}`, e.id, true);
      ensureSiren();
      notify(`🔴 ${eventLabel(e)} is DOWN`, `Down since ${fmtTime(e.ts)}`);
    }
  } else {
    let durTxt = '';
    if (e.device_id != null && ALARM.downSince[e.device_id]) {
      durTxt = ` — was down for ${fmtDur(e.ts - ALARM.downSince[e.device_id])}`;
      delete ALARM.downSince[e.device_id];
      saveDownSince();
    }
    for (const [eid, did] of [...ALARM.activeDown]) {
      if (did === e.device_id) { ALARM.activeDown.delete(eid); dismissToast(eid); }
    }
    ensureSiren();
    showToast('up', `🟢 ${eventLabel(e)} is back UP`, `${fmtTime(e.ts)}${durTxt}`, null, false);
    playUpSound();
    notify(`🟢 ${eventLabel(e)} is back UP`, `Recovered at ${fmtTime(e.ts)}${durTxt}`);
  }
}

// --- alarms panel (dashboard) + alarms page ---
function renderAlarmsInto(el, evsAsc) {
  if (!el) return;
  const evs = evsAsc.filter(e => e.kind === 'down' || e.kind === 'up');
  const open = {}, rows = [], standaloneUps = [];
  for (const e of evs) {
    if (e.kind === 'down') open[e.device_id] = e;
    else if (open[e.device_id]) { rows.push({ down: open[e.device_id], up: e }); delete open[e.device_id]; }
    else standaloneUps.push(e);
  }
  for (const did in open) rows.push({ down: open[did], up: null });
  rows.sort((a, b) => b.down.id - a.down.id);
  const now = Math.floor(Date.now() / 1000);
  let html = rows.slice(0, 30).map(r => {
    const name = esc(eventLabel(r.down));
    if (r.up) {
      return `<div class="alarmrow"><span>🟢</span><div class="grow"><b>${name}</b> — down ${fmtTime(r.down.ts)} → up ${fmtTime(r.up.ts)} · downtime <b>${fmtDur(r.up.ts - r.down.ts)}</b></div></div>`;
    }
    const acked = ALARM.acked.has(r.down.id);
    return `<div class="alarmrow ${acked ? 'acked' : ''}"><span>🔴</span><div class="grow"><b>${name}</b> — down since ${fmtTime(r.down.ts)} · <span class="tier">${downTier(now - r.down.ts)}</span> (<b>${fmtDur(now - r.down.ts)}</b> so far)</div>` +
      (acked ? '<span class="muted">Acknowledged</span>' : IS_ADMIN() ? `<button data-ack="${r.down.id}">Acknowledge</button>` : '') + '</div>';
  }).join('');
  html += standaloneUps.slice(-5).map(e =>
    `<div class="alarmrow"><span>🟢</span><div class="grow"><b>${esc(eventLabel(e))}</b> came up at ${fmtTime(e.ts)} <span class="muted">(start of outage outside this window)</span></div></div>`).join('');
  el.innerHTML = html || '<p class="muted">No outages recorded yet</p>';
  el.querySelectorAll('[data-ack]').forEach(b => b.onclick = () => ackAlarm(+b.dataset.ack));
}
function renderAlarms() { renderAlarmsInto($('#alarms'), ALARM.recent); }

async function loadAlarmsPage() {
  try {
    const evs = await api('/api/events?since_id=0&limit=200');
    renderAlarmsInto($('#alarms-page'), evs);
    $('#alarms-events').innerHTML = [...evs].reverse().map(e =>
      `<div class="event ${e.kind}">${e.kind === 'down' ? '🔴' : e.kind === 'up' ? '🟢' : e.kind === 'monitor' ? '🔌' : 'ℹ️'} ${esc(e.message)}<br><span class="muted">${fmtTime(e.ts)}</span></div>`).join('') || '<p class="muted">No events yet</p>';
  } catch (e) { $('#alarms-events').textContent = 'Error: ' + e.message; }
}

async function pollAlarms(first) {
  try {
    const evs = await api('/api/events?since_id=' + ALARM.lastId + '&limit=200');
    if (first) {
      // Seed silently: rebuild who is currently down (and since when) from the
      // recent window, and restore unacknowledged down banners — no sounds
      // for history (audio is locked until the first click anyway).
      const open = {};
      for (const e of evs) {
        ALARM.lastId = Math.max(ALARM.lastId, e.id);
        ALARM.recent.push(e);
        if (e.kind === 'down') { open[e.device_id] = e; ALARM.downSince[e.device_id] = e.ts; }
        else if (e.kind === 'up') { delete open[e.device_id]; delete ALARM.downSince[e.device_id]; }
      }
      saveDownSince();
      for (const did in open) {
        const e = open[did];
        if (!ALARM.acked.has(e.id)) {
          ALARM.activeDown.set(e.id, e.device_id);
          showToast('down', `🔴 ${eventLabel(e)} is DOWN`, `Since ${fmtTime(e.ts)}`, e.id, true);
        }
      }
    } else {
      for (const e of evs) {
        ALARM.lastId = Math.max(ALARM.lastId, e.id);
        ALARM.recent.push(e);
        handleAlarmEvent(e);
      }
    }
    if (ALARM.recent.length > 200) ALARM.recent = ALARM.recent.slice(-200);
    updateAlarmBadge();
    renderAlarms();
  } catch (e) { /* transient — next poll retries */ }
}

function initAlarms() {
  updateAlarmButtons();
  const packSel = $('#sound-pack');
  if (packSel) packSel.onchange = () => {
    ALARM.pack = packSel.value;
    localStorage.setItem('netgold.soundPack', ALARM.pack);
    playUpSound(); // short preview of the selected pack's recovery tone
  };
  const volSlider = $('#sound-vol');
  if (volSlider) volSlider.oninput = () => {
    ALARM.volume = volSlider.value / 100;
    localStorage.setItem('netgold.soundVol', String(ALARM.volume));
  };
  $('#alarm-sound-btn').onclick = () => {
    ALARM.soundOn = !ALARM.soundOn;
    localStorage.setItem('netgold.alarmSound', ALARM.soundOn ? 'on' : 'off');
    updateAlarmButtons();
    ensureSiren();
  };
  $('#alarm-notif-btn').onclick = async () => {
    if (!ALARM.notifOn) {
      if (!('Notification' in window)) { alert('This browser does not support desktop notifications.'); return; }
      const perm = await Notification.requestPermission();
      if (perm !== 'granted') { alert('Notification permission was not granted.'); return; }
      ALARM.notifOn = true;
    } else {
      ALARM.notifOn = false;
    }
    localStorage.setItem('netgold.notif', ALARM.notifOn ? 'on' : 'off');
    updateAlarmButtons();
  };
  pollAlarms(true);
  setInterval(() => pollAlarms(false), 12000);
}

// ---------- reports: downtime ----------
let repPeriod = null; // {from, to} in unix seconds
function defaultPeriod() { const now = Math.floor(Date.now() / 1000); return { from: now - 7 * 86400, to: now }; }
async function loadReport() {
  if (!repPeriod) repPeriod = defaultPeriod();
  try {
    const rep = await api(`/api/reports/downtime?from=${repPeriod.from}&to=${repPeriod.to}`);
    $('#rep-meta').textContent = `Period: ${fmtTime(rep.from)} → ${fmtTime(rep.to)} · ${rep.totals.devices} devices · ${rep.totals.outages} outages · total downtime ${fmtDur(rep.totals.total_downtime_sec)} · overall uptime ${rep.totals.uptime_pct.toFixed(2)}%`;
    $('#rep-summary tbody').innerHTML = rep.devices.map(d => `<tr>
      <td><b>${esc(d.device_name)}</b></td><td>${esc(d.group)}</td><td>${esc(d.ip)}</td>
      <td>${d.outages}</td><td>${fmtDur(d.total_downtime_sec)}</td><td>${fmtDur(d.longest_outage_sec)}</td>
      <td>${d.uptime_pct.toFixed(2)}%</td><td>${d.currently_down ? '🔴 Down' : '🟢 Up'}</td></tr>`).join('')
      + `<tr><td colspan="3"><b>TOTAL (all devices)</b></td><td><b>${rep.totals.outages}</b></td><td><b>${fmtDur(rep.totals.total_downtime_sec)}</b></td><td><b>${fmtDur(rep.totals.longest_outage_sec)}</b></td><td><b>${rep.totals.uptime_pct.toFixed(2)}%</b></td><td></td></tr>`;
    $('#rep-details tbody').innerHTML = rep.outages.map(o => `<tr>
      <td>${esc(o.device_name)}</td><td>${esc(o.group)}</td><td>${esc(o.ip)}</td>
      <td>${fmtTime(o.down_ts)}</td><td>${o.up_ts ? fmtTime(o.up_ts) : '—'}</td><td>${fmtDur(o.duration_sec)}</td>
      <td>${o.ongoing ? '🔴 Ongoing' : '🟢 Restored'}${o.clipped ? ' <span class="muted">(began before period)</span>' : ''}</td></tr>`).join('')
      || '<tr><td colspan="7" class="muted">No outages in this period — every device stayed up</td></tr>';
  } catch (e) { $('#rep-meta').textContent = 'Error: ' + e.message; }
}
function setPreset(name) {
  document.querySelectorAll('#rep-presets [data-preset]').forEach(b => b.classList.toggle('active', b.dataset.preset === name));
  $('#rep-custom').classList.toggle('hidden', name !== 'custom');
  const now = new Date();
  const dayStart = new Date(now); dayStart.setHours(0, 0, 0, 0);
  const nowS = Math.floor(now.getTime() / 1000), dayS = Math.floor(dayStart.getTime() / 1000);
  if (name === 'today') repPeriod = { from: dayS, to: nowS };
  else if (name === 'yesterday') repPeriod = { from: dayS - 86400, to: dayS };
  else if (name === '7d') repPeriod = { from: nowS - 7 * 86400, to: nowS };
  else if (name === '30d') repPeriod = { from: nowS - 30 * 86400, to: nowS };
  else if (name === 'custom') {
    // Pre-fill the inputs with the current period; Apply loads the report.
    const toLocal = ts => { const d = new Date(ts * 1000); d.setMinutes(d.getMinutes() - d.getTimezoneOffset()); return d.toISOString().slice(0, 16); };
    if (!repPeriod) repPeriod = defaultPeriod();
    $('#rep-from').value = toLocal(repPeriod.from);
    $('#rep-to').value = toLocal(repPeriod.to);
    return;
  }
  loadReport();
}
document.querySelectorAll('#rep-presets [data-preset]').forEach(b => b.onclick = () => setPreset(b.dataset.preset));
$('#rep-apply').onclick = () => {
  const f = new Date($('#rep-from').value), t = new Date($('#rep-to').value);
  if (isNaN(f) || isNaN(t) || f >= t) { alert('Please pick a valid from/to range.'); return; }
  repPeriod = { from: Math.floor(f.getTime() / 1000), to: Math.floor(t.getTime() / 1000) };
  loadReport();
};
$('#rep-xlsx').onclick = () => { if (!repPeriod) repPeriod = defaultPeriod(); window.open(`/api/reports/downtime.xlsx?from=${repPeriod.from}&to=${repPeriod.to}`, '_blank'); };
$('#rep-pdf').onclick = () => { if (!repPeriod) repPeriod = defaultPeriod(); window.open(`/api/reports/downtime.pdf?from=${repPeriod.from}&to=${repPeriod.to}`, '_blank'); };

// ---------- topology ----------
let topo = null;          // {topology, nodes, links}
let topoSel = null;       // selected node id
let topoLinkMode = false;
let topoLinkFrom = null;  // first node clicked in link mode

// Tidy View for topologies (v3.4): the same on-screen-only
// rearrangement as on the Maps page — stored node positions are
// never written back; topoTidyPos holds the arranged positions for
// the currently open topology while the toggle is on.
let topoTidy = false;
let topoTidyPos = null; // Map<nodeId, {x, y}> while Tidy View is on
let topoTidyCellW = null; // Map<nodeId, cell pitch in scene units> in grid Tidy View
let topoTidyLoose = null; // the loose group's drawn rectangle (scene units) in grid Tidy View
let topoTidyScaleS = 0; // the solved layout scale of the current tidy scene (v3.6 scene-space rendering)
function topoPosOf(n) {
  return (topoTidy && topoTidyPos && topoTidyPos.get(n.id)) || { x: n.x, y: n.y };
}
// topoLabelOf is a node's label content: the name wrapped into
// display lines (never truncated, v3.6) and the caption shown
// under it — shared by the renderer and the tidy footprint
// computation.
function topoLabelOf(n) {
  return {
    lines: wrapLabel(n.label || '', 15),
    caption: n.ip || n.device_type || n.type || '',
  };
}
// The visible height available to the topology canvas: from
// the wrap's top edge to the bottom of the window. In Tidy
// View the arrangement is solved against this height (and the
// canvas is capped at it), so the whole scene is on screen at
// once — the same rule as on the Maps page.
const topoVisibleH = wrap => {
  if (!wrap) return 0;
  const h = Math.floor(window.innerHeight - Math.max(wrap.getBoundingClientRect().top, 0) - 10);
  return h >= 320 ? h : 0;
};
async function computeTopoTidy() {
  topoTidyPos = null;
  topoTidyCellW = null;
  topoTidyLoose = null;
  topoTidyScaleS = 0;
  if (!topo || !topo.nodes.length) return;
  // The topology's link pairs for the layout (v3.4.4), so linked
  // nodes are arranged next to each other (see computeMapTidy).
  const topoLinkPairs = [], seenTopoLink = new Set();
  (topo.links || []).forEach(l => {
    if (l.from_node === l.to_node) return;
    const key = Math.min(l.from_node, l.to_node) + '-' + Math.max(l.from_node, l.to_node);
    if (seenTopoLink.has(key)) return;
    seenTopoLink.add(key);
    topoLinkPairs.push([l.from_node, l.to_node]);
  });
  const wrap = $('#topo-wrap');
  const w = (wrap && wrap.clientWidth) || 1100;
  const extentOf = pts => {
    let mx = 900, my = 560;
    pts.forEach(p => { mx = Math.max(mx, p.x + 130); my = Math.max(my, p.y + 130); });
    return { mx, my };
  };
  const svgH = (mx, my) => Math.max(320, w * my / mx);
  const ext = extentOf(topo.nodes);
  // The canvas the tidy scene is solved for: the visible part
  // of the window, not the stored drawing's aspect height (a
  // near-square scene solved against a tall fictional canvas
  // renders as a page the reader must scroll).
  const canvasH = topoVisibleH(wrap) || svgH(ext.mx, ext.my);
  // The design scale of the drawing as stored. The layout runs
  // in screen-px space at this scale, with the single-scale
  // construction of the Maps page (see computeMapTidy): the
  // layout runs once, and the render scale is solved from its px
  // result against the renderer's own extent fit.
  let k = Math.min(w / ext.mx, svgH(ext.mx, ext.my) / ext.my);
  if (!(k > 0) || !isFinite(k)) k = 1;
  const solveTopoGridAt = async (compact, dk, useCanvas) => {
    // Grid-first layout, exactly as on the Maps page: fw is the
    // label pill's width (capped per TIDY_GRID_FW_CAP_*), fh is
    // how far the label stack reaches below the marker centre;
    // the layout returns each node's grid cell width with the
    // positions.
    const res = await post('/api/tidy', {
      mode: 'grid',
      points: topo.nodes.map(n => {
        const l = topoLabelOf(n);
        const fp = labelFootprintPx(l.lines, l.caption, n.label, compact);
        return {
          id: n.id, x: n.x * dk, y: n.y * dk,
          // The full label width, uncapped (v3.6).
          fw: labelPillW(l.lines, l.caption, Infinity),
          fh: fp.ext
        };
      }),
      min_dx: TIDY_MIN_DX_PX, min_dy: TIDY_MIN_DY_PX,
      links: topoLinkPairs,
      aspect: canvasH > 0 ? w / canvasH : 0,
      box_w: useCanvas ? w : 0, box_h: useCanvas ? canvasH : 0
    });
    if (!res || !res.points || !res.points.length) return null;
    // The topology canvas is anchored at the origin (its viewBox
    // starts at 0,0 and its frame is drawn from the nodes'
    // maximum extents), so the arrangement is re-based onto the
    // origin: its minimum corner lands at (0,0). A pure shift,
    // identical at every scale: relative positions, and
    // therefore every link, are unchanged — but no part of the
    // scene can hide outside the frame or below a stretch of
    // empty canvas.
    let mnX = Infinity, mnY = Infinity;
    res.points.forEach(p => { mnX = Math.min(mnX, p.x); mnY = Math.min(mnY, p.y); });
    // The loose group's rectangle wraps every node (a topology
    // sends no boxes, so all its nodes are loose): its corners
    // bound the scene, so they take part in the re-basing too.
    if (res.loose && res.loose.env) {
      mnX = Math.min(mnX, res.loose.env[0]);
      mnY = Math.min(mnY, res.loose.env[1]);
    }
    // The margin leaves room at the frame's near edges for the
    // fixed-size node furniture (plate and label pill are drawn
    // around the node's point, so a node sitting exactly on the
    // frame edge would spill half its plate outside it).
    const shX = -mnX + 110, shY = -mnY + 110;
    const looseUnits = s => (res.loose && res.loose.env)
      ? [(res.loose.env[0] + shX) / s, (res.loose.env[1] + shY) / s, (res.loose.env[2] + shX) / s, (res.loose.env[3] + shY) / s]
      : null;
    const fitAt = s => {
      const units = res.points.map(p => ({ x: (p.x + shX) / s, y: (p.y + shY) / s }));
      const e = extentOf(units);
      const lu = looseUnits(s);
      if (lu) { e.mx = Math.max(e.mx, lu[2] + 20); e.my = Math.max(e.my, lu[3] + 20); }
      // The rendered canvas is the aspect height capped at the
      // visible height (see renderTopo), so the fit is too.
      const eh = Math.min(svgH(e.mx, e.my), canvasH);
      return { units, fit: Math.min(w / e.mx, eh / e.my), loose: lu };
    };
    // Same scale choice as on the Maps page (see computeMapTidy):
    // the screen pitches are set by the ratio fit / s, so the
    // best measured ratio wins — the fixed point when the scene
    // fits the canvas, the small-scale plateau when it does not.
    const candidates = [];
    const consider = s => {
      const r = fitAt(s);
      if (!(r.fit > 0) || !isFinite(r.fit)) return null;
      const c = { s, fit: r.fit, r: r.fit / s, units: r.units, loose: r.loose };
      candidates.push(c);
      return c;
    };
    let s = dk;
    for (let it = 0; it < 6; it++) {
      const c = consider(s);
      if (!c) break;
      if (Math.abs(c.fit - s) <= 0.005 * s) break; // fixed point reached
      s += 0.7 * (c.fit - s);
    }
    let probe = candidates.length ? candidates[candidates.length - 1].s : 0;
    for (let it = 0; it < 4 && probe > 0; it++) {
      probe /= 2;
      if (!consider(probe)) break;
    }
    if (!candidates.length) return null;
    // The pitch ratio alone would creep toward tiny scales:
    // the frame's fixed unit margins cost relatively less
    // there, so the ratio keeps rising as s shrinks — but the
    // renderer's node furniture is counter-scaled by 1/fit and
    // clamped, so a candidate whose fit is too small shrinks
    // every plate and label regardless of its ratio. Among the
    // candidates whose fit keeps the furniture at full size,
    // the best pitch ratio wins (ties to the larger scale);
    // if none qualifies, the largest fit is the least-bad.
    const sane = candidates.filter(c => c.fit >= 0.45);
    const pool = sane.length ? sane : candidates.slice().sort((a, b) => b.fit - a.fit).slice(0, 1);
    const maxR = Math.max(...pool.map(c => c.r));
    const chosen = pool.filter(c => c.r >= maxR - 0.002).sort((a, b) => b.s - a.s)[0];
    return {
      s: chosen.s,
      fit: chosen.fit,
      r: chosen.r,
      pos: new Map(res.points.map((p, i) => [p.id, chosen.units[i]])),
      cellW: new Map(res.points.map((p, i) => [p.id, (res.cell_w && res.cell_w[i] ? res.cell_w[i] : 0) / chosen.s])),
      loose: chosen.loose || null
    };
  };
  // Same design-scale sweep as on the Maps page (see
  // computeMapTidy): a few design scales, best fit ratio wins,
  // ties go to the larger scale.
  // Both layout flavours per design scale, as on the Maps
  // page: with the canvas named the layout runs its column
  // optimisation, without it the columns come from the stored
  // shape alone; the fit ratio measured here decides.
  const solveTopoGrid = async compact => {
    let best = null;
    for (const dk of [k, k * 0.65, k * 0.42]) {
      for (const useCanvas of [true, false]) {
        const got = await solveTopoGridAt(compact, dk, useCanvas);
        if (got && (!best || got.r > best.r + 0.002)) best = got;
      }
    }
    return best;
  };
  // Full labels only (v3.6): the tidy scene renders in scene
  // space at the solved scale (topoTidyScaleS), like the Maps
  // page, so no compact fallback is needed at any fit.
  const solved = await solveTopoGrid(false);
  if (solved) {
    topoTidyPos = solved.pos; topoTidyCellW = solved.cellW; topoTidyLoose = solved.loose;
    topoTidyScaleS = solved.s;
  }
}
$('#topo-tidy-btn') && ($('#topo-tidy-btn').onclick = async () => {
  topoTidy = !topoTidy;
  syncTopoTidyBtn();
  if (topoTidy) { await computeTopoTidy(); } else { topoTidyPos = null; topoTidyCellW = null; topoTidyLoose = null; topoTidyScaleS = 0; }
  renderTopo();
});

async function loadTopologies() {
  const tbody = $('#topo-table tbody');
  if (tbody && !tbody.children.length) tbody.innerHTML = '<tr><td colspan="6"><div class="skeleton"></div></td></tr>';
  try {
    const tops = await api('/api/topologies');
    $('#topo-table tbody').innerHTML = tops.map(t => `<tr>
      <td><a href="#" data-topo="${t.id}"><b>${esc(t.name)}</b></a>${t.description ? `<br><span class="muted">${esc(t.description)}</span>` : ''}</td>
      <td><span class="badge src-${esc(t.source)}">${esc(t.source)}</span></td>
      <td>${t.nodes}</td><td>${t.links}</td><td>${fmtTime(t.created_at)}</td>
      <td><button data-topo-open="${t.id}">Open</button>${IS_ADMIN() ? ` <button data-topo-del="${t.id}">🗑</button>` : ''}</td></tr>`).join('')
      || '<tr><td colspan="6" class="muted">No topologies yet — create one above</td></tr>';
    document.querySelectorAll('[data-topo]').forEach(a => a.onclick = e => { e.preventDefault(); openTopology(+a.dataset.topo); });
    document.querySelectorAll('[data-topo-open]').forEach(b => b.onclick = () => openTopology(+b.dataset.topoOpen));
    document.querySelectorAll('[data-topo-del]').forEach(b => b.onclick = async () => {
      if (confirm('Delete this topology and all its nodes and links?')) { await api('/api/topologies/' + b.dataset.topoDel, { method: 'DELETE' }); loadTopologies(); }
    });
    const maps = await api('/api/maps').catch(() => []);
    $('#topo-map-select').innerHTML = maps.length
      ? maps.map(m => `<option value="${m.id}">${esc(m.name)} (${m.objects} objects)</option>`).join('')
      : '<option value="">No imported maps yet</option>';
  } catch (e) { $('#topo-msg').textContent = 'Error: ' + e.message; }
}
$('#topo-create-form').onsubmit = async e => {
  e.preventDefault();
  const f = new FormData(e.target);
  try {
    const res = await post('/api/topologies', { name: f.get('name'), source: 'manual' });
    e.target.reset();
    openTopology(res.id);
  } catch (err) { alert(err.message); }
};
// Rebuild the automatic topologies (v3.6) from the current data.
$('#topo-rebuild-auto-btn') && ($('#topo-rebuild-auto-btn').onclick = async () => {
  try {
    const res = await post('/api/topologies/auto/rebuild', {});
    const parts = [];
    if (res.hub_built) parts.push(`hub topology rebuilt (${res.hub_nodes} nodes, ${res.hub_links} links)`);
    if (res.combined_built) parts.push(`combined topology rebuilt (${res.combined_nodes} nodes, ${res.combined_links} links)`);
    $('#topo-msg').textContent = parts.length
      ? 'Automatic topologies: ' + parts.join(' · ') + '.'
      : 'No automatic topologies could be built from the current data (the configured hub group, an ASR 9K device, or the configured source maps were not found).';
    loadTopologies();
  } catch (err) { alert(err.message); }
});
$('#topo-frommap-form').onsubmit = async e => {
  e.preventDefault();
  const mid = $('#topo-map-select').value;
  if (!mid) { alert('No imported map is available. Import a legacy database first.'); return; }
  try {
    const res = await post('/api/topologies/from-map', { map_id: +mid });
    $('#topo-msg').textContent = res.note;
    loadTopologies();
  } catch (err) { alert(err.message); }
};
$('#topo-import-form').onsubmit = async e => {
  e.preventDefault();
  const file = $('#topo-file').files[0];
  if (!file) { alert('Choose an .xlsx file first.'); return; }
  const fd = new FormData();
  fd.append('file', file);
  const name = new FormData(e.target).get('name');
  if (name) fd.append('name', name);
  $('#topo-msg').textContent = 'Importing…';
  try {
    const r = await fetch('/api/topologies/import', { method: 'POST', body: fd });
    const data = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(data.error || r.statusText);
    $('#topo-msg').textContent = `Imported ${data.nodes} nodes and ${data.links} links.`;
    uiTick(true);
    e.target.reset();
    loadTopologies();
  } catch (err) { uiTick(false); $('#topo-msg').textContent = 'Error: ' + err.message; }
};

async function openTopology(id) {
  try {
    topo = await api('/api/topologies/' + id);
  } catch (e) { alert(e.message); return; }
  topoSel = null; topoLinkFrom = null; topoLinkMode = false;
  updateLinkModeBtn();
  // The automatic topologies open in Tidy View (v3.6): their
  // stored layout is a design grid that the tidy arrangement
  // (and its scene-space labels) is built for.
  if (topo.topology.source === 'auto' && !topoTidy) { topoTidy = true; syncTopoTidyBtn(); }
  if (topoTidy) { await computeTopoTidy(); } else { topoTidyPos = null; topoTidyLoose = null; topoTidyScaleS = 0; }
  show('topoeditor');
  document.querySelectorAll('.navbtn').forEach(b => b.classList.toggle('active', b.dataset.view === 'topology'));
  $('#topo-name').value = topo.topology.name;
  $('#topo-export-link').href = '/api/topologies/' + id + '/export.xlsx';
  $('#topo-node-form').classList.add('hidden');
  renderTopo();
}
$('#topo-back-btn').onclick = () => show('topology');
$('#topo-rename-btn').onclick = async () => {
  const name = $('#topo-name').value.trim();
  if (!name) return;
  await api('/api/topologies/' + topo.topology.id, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name }) });
  topo.topology.name = name;
  $('#topo-editor-msg').textContent = 'Renamed.';
};
$('#topo-refresh-btn').onclick = () => openTopology(topo.topology.id);
$('#topo-addnode-btn').onclick = async () => {
  const n = (topo.nodes.length % 6);
  const res = await post(`/api/topologies/${topo.topology.id}/nodes`, { label: 'New Node', x: 110 + n * 40, y: 100 + n * 40 });
  await openTopology(topo.topology.id);
  selectTopoNode(res.id);
};
function updateLinkModeBtn() {
  const b = $('#topo-linkmode-btn');
  b.textContent = 'Add Link: ' + (topoLinkMode ? 'On — click two nodes' : 'Off');
  b.classList.toggle('linkmode-on', topoLinkMode);
}
$('#topo-linkmode-btn').onclick = () => { topoLinkMode = !topoLinkMode; topoLinkFrom = null; updateLinkModeBtn(); };

function topoNodeById(id) { return topo.nodes.find(n => n.id === id); }
const topoPrevStatus = {}; // node id -> last rendered device status
// The state of a topology link follows its end nodes' devices, exactly
// like the map lines: any end down -> down; both up -> up; else calm.
function topoLinkState(a, b) {
  const sa = a.device_status || '', sb = b.device_status || '';
  if (sa === 'down' || sb === 'down') return 'down';
  if (sa === 'up' && sb === 'up') return 'up';
  return 'unknown';
}
// topoScaleNodes counter-scales every topology node's fixed-size
// furniture by 1/fit-scale (v3.2.1), the same fixed-symbol technique
// as the Maps page: nodes keep their exact on-screen size however
// large the topology drawing is. The scale is clamped so extreme
// fit scales stay sane.
function topoScaleNodes(svg) {
  if (!svg) return;
  // Scene-space tidy scenes (v3.6): the node furniture is scaled
  // by the layout's solved scale (1/s) — a constant of the scene,
  // not of the zoom — and full labels are always shown.
  if (svg.dataset && svg.dataset.topoScene === '1' && topoTidyScaleS > 0) {
    svg.classList.remove('labels-compact');
    const s = (1 / topoTidyScaleS).toFixed(4);
    svg.querySelectorAll('.tnode-fixed').forEach(g => g.setAttribute('transform', `scale(${s})`));
    return;
  }
  const rect = svg.getBoundingClientRect();
  const vb = svg.viewBox && svg.viewBox.baseVal;
  if (!rect.width || !vb || !vb.width) return;
  let k = rect.width / vb.width;
  if (rect.height && vb.height) k = Math.min(k, rect.height / vb.height);
  if (!(k > 0) || !isFinite(k)) return;
  svg.classList.toggle('labels-compact', k < LBL_COMPACT_K);
  const s = Math.min(Math.max(1 / k, 0.3), 2.5).toFixed(4);
  svg.querySelectorAll('.tnode-fixed').forEach(g => g.setAttribute('transform', `scale(${s})`));
}
function renderTopo() {
  const wrap = $('#topo-wrap');
  if (!topo) { wrap.innerHTML = ''; return; }
  // The topology's stored description (v3.6) — the automatic
  // topologies use it to say where they came from.
  const desc = $('#topo-desc');
  if (desc) {
    const txt = (topo.topology && topo.topology.description) || '';
    desc.textContent = txt;
    desc.classList.toggle('hidden', !txt);
  }
  const note = $('#topo-origin-note');
  if (note) {
    const rec = topo.links.filter(l => l.origin === 'imported-map').length;
    if (topo.topology.source === 'map' || rec > 0) {
      note.textContent = 'Links reconstructed from the imported map drawing — each drawn line\'s endpoints were matched to the nearest device icons. The source database records no port-to-port links, so adjust any link that looks wrong.';
      note.classList.remove('hidden');
    } else {
      note.classList.add('hidden');
    }
  }
  // The viewBox follows the nodes' own extents (v3.6): a
  // topology whose stored positions are negative — the
  // automatic hub is laid out around the origin, and copied
  // maps keep their source coordinates — used to open with
  // every node left of or above the origin outside the frame.
  // The margins are screen-true: the fixed-size (or scene)
  // furniture around an edge node needs the same screen room
  // whatever the scene's scale, so the pads are computed from
  // the fit scale of the extents themselves.
  let rawMinX = Infinity, rawMinY = Infinity, rawMaxX = -Infinity, rawMaxY = -Infinity;
  for (const n of topo.nodes) {
    const np = topoPosOf(n);
    rawMinX = Math.min(rawMinX, np.x); rawMaxX = Math.max(rawMaxX, np.x);
    rawMinY = Math.min(rawMinY, np.y); rawMaxY = Math.max(rawMaxY, np.y);
  }
  // In grid Tidy View the loose group's frame wraps every node;
  // the viewBox must contain it too.
  if (topoTidy && topoTidyLoose) {
    rawMinX = Math.min(rawMinX, topoTidyLoose[0]); rawMaxX = Math.max(rawMaxX, topoTidyLoose[2]);
    rawMinY = Math.min(rawMinY, topoTidyLoose[1]); rawMaxY = Math.max(rawMaxY, topoTidyLoose[3]);
  }
  if (!isFinite(rawMinX)) { rawMinX = 0; rawMinY = 0; rawMaxX = 900; rawMaxY = 560; }
  const cwT = (wrap && wrap.clientWidth) || 1500;
  const chT = (wrap && wrap.clientHeight) || Math.floor(window.innerHeight * 0.62) || 700;
  const k0 = Math.min(cwT / Math.max(rawMaxX - rawMinX, 1), chT / Math.max(rawMaxY - rawMinY, 1));
  // In the faithful view the label planner may put an edge
  // node's pill in a side slot, extending a full pill width
  // from its anchor; the tidy scene always labels below.
  const padX = (topoTidy ? 115 : 190) / k0, padTop = 65 / k0, padBottom = 125 / k0;
  let minX = rawMinX - padX, minY = rawMinY - padTop;
  let maxX = rawMaxX + padX, maxY = rawMaxY + padBottom;
  if (maxX - minX < 900) maxX = minX + 900;
  if (maxY - minY < 560) maxY = minY + 560;
  const byId = {};
  topo.nodes.forEach(n => byId[n.id] = n);
  let p = '';
  // The loose group's frame (grid Tidy View): a neutral dashed
  // box around the arranged nodes, drawn under the links and
  // nodes, with its caption added below once the fit scale is
  // known.
  if (topoTidy && topoTidyLoose) {
    const le = topoTidyLoose;
    p += `<rect class="topo-loose-region" x="${le[0].toFixed(1)}" y="${le[1].toFixed(1)}" width="${(le[2] - le[0]).toFixed(1)}" height="${(le[3] - le[1]).toFixed(1)}" rx="8" fill="rgba(130,150,175,.08)" stroke="rgba(130,150,175,.6)" stroke-width="1.4" stroke-dasharray="7 5"/>`;
  }
  // Parallel links (v3.6): links sharing the same node pair are
  // shifted sideways, alternating sides, so each one is drawn as
  // its own visible line instead of hiding under the first.
  const topoPairSeen = new Map();
  for (const l of topo.links) {
    const a = byId[l.from_node], b = byId[l.to_node];
    if (!a || !b) continue;
    const pa = topoPosOf(a), pb = topoPosOf(b);
    const pairKey = Math.min(l.from_node, l.to_node) + '-' + Math.max(l.from_node, l.to_node);
    const pn = topoPairSeen.get(pairKey) || 0;
    topoPairSeen.set(pairKey, pn + 1);
    let ox = 0, oy = 0;
    if (pn > 0) {
      const mag = (pn % 2 === 1 ? Math.ceil(pn / 2) : -pn / 2) * 10;
      const dx = pb.x - pa.x, dy = pb.y - pa.y, len = Math.hypot(dx, dy);
      if (len > 0) { ox = -dy / len * mag; oy = dx / len * mag; }
    }
    const x1 = pa.x + ox, y1 = pa.y + oy, x2 = pb.x + ox, y2 = pb.y + oy;
    const mx = ((x1 + x2) / 2).toFixed(1), my = ((y1 + y2) / 2 - 6).toFixed(1);
    const st = topoLinkState(a, b);
    p += `<line class="topo-hit" data-link="${l.id}" x1="${x1}" y1="${y1}" x2="${x2}" y2="${y2}" stroke="rgba(0,0,0,0)" stroke-width="14"><title>Click to delete this link</title></line>`;
    p += `<line class="topo-link st-${st}" data-link="${l.id}" x1="${x1}" y1="${y1}" x2="${x2}" y2="${y2}"/>`;
    const lbl = [l.label, l.bandwidth].filter(Boolean).join(' · ');
    if (lbl) p += `<text class="topo-lbl" x="${mx}" y="${my}">${esc(lbl)}</text>`;
  }
  // Label width budgets (v3.2.2), resolved against each node's
  // neighbours at the topology's fit scale — the same zoom-aware
  // compact-label treatment as the Maps page. The svg is as wide
  // as the wrap and as tall as its aspect ratio (min 320 px).
  const topoWrapW = wrap.clientWidth || 0;
  let topoSvgH = topoWrapW > 0 ? Math.max(320, topoWrapW * (maxY - minY) / Math.max(maxX - minX, 1)) : 0;
  // In Tidy View the canvas is capped at the visible height, so
  // the whole arranged scene is on screen at once (the layout
  // was solved against the same height in computeTopoTidy).
  if (topoTidy) {
    const vh = topoVisibleH(wrap);
    if (vh > 0) topoSvgH = Math.min(topoSvgH, vh);
  }
  const kTopo = topoWrapW > 0 ? Math.min(topoWrapW / Math.max(maxX - minX, 1), topoSvgH / Math.max(maxY - minY, 1)) : 0;
  const topoPts = topo.nodes.map(n => topoPosOf(n));
  const topoBudF = resolveLabelWidths(topoPts, kTopo,
    topo.nodes.map(n => { const l = topoLabelOf(n); return labelNaturalW(l.lines, l.caption); }),
    topo.nodes.map(n => { const l = topoLabelOf(n); return labelReach(l.lines, l.caption); }));
  const topoBudC = resolveLabelWidths(topoPts, kTopo,
    topo.nodes.map(n => Math.ceil(n.label.length * LBL_COMPACT_CHAR_W) + 10),
    topo.nodes.map(() => LBL_COMPACT_REACH));
  const topoBudById = new Map(topo.nodes.map((n, i) => [n.id, { f: topoBudF[i], c: topoBudC[i] }]));
  // The same smart label placement as the maps (v3.4.2), against
  // the canvas only — topology has no regions: a label flips
  // above/beside its node whenever below would hit another node
  // or label. In Tidy View the planner runs in hard mode
  // (v3.4.3): the layout reserved every label's footprint, so
  // marker contact is a hard constraint there too.
  const topoCanvas = { x0: 0, y0: 0, x1: maxX, y1: maxY };
  const topoPlaceF = planLabelSlots(topo.nodes.map((n, i) => {
    const l = topoLabelOf(n), p = topoPosOf(n);
    return { ax: p.x, ay: p.y, w: labelPillW(l.lines, l.caption, topoBudF[i]), h: labelReach(l.lines, l.caption), stag: false, region: null };
  }), kTopo, topoCanvas, topoTidy ? { hard: true } : null);
  const topoPlaceC = planLabelSlots(topo.nodes.map((n, i) => {
    const p = topoPosOf(n);
    return { ax: p.x, ay: p.y, w: labelPillCompactW(n.label, topoBudC[i]), h: LBL_COMPACT_REACH, stag: false, region: null };
  }), kTopo, topoCanvas, topoTidy ? { hard: true } : null);
  const topoPlaceById = new Map(topo.nodes.map((n, i) => [n.id, { f: topoPlaceF[i], c: topoPlaceC[i] }]));
  // Grid Tidy View: as on the Maps page, each node sits in a
  // grid cell that reserves its label, so budgets come from the
  // cell pitch and labels simply go below their markers.
  if (topoTidy && topoTidyCellW && kTopo > 0) {
    const below = () => ({ slot: 'below', dx: 0, dy: 0, stag: false });
    topo.nodes.forEach(n => {
      const cwU = topoTidyCellW.get(n.id) || 0;
      if (cwU > 0) {
        // Cell width minus the pill gap, as on the Maps page
        // (see TIDY_PILL_GAP_PX): neighbouring pills keep a
        // clear space at the fit scale.
        const b = Math.max(LBL_MIN_W, Math.floor(cwU * kTopo) - TIDY_PILL_GAP_PX);
        topoBudById.set(n.id, { f: b, c: b });
      }
      topoPlaceById.set(n.id, { f: below(), c: below() });
    });
  }
  // The loose frame's caption, sized in screen px at the fit
  // scale and set above the frame's top edge.
  if (topoTidy && topoTidyLoose && kTopo > 0) {
    const le = topoTidyLoose;
    p += `<text class="topo-loose-lbl" x="${(le[0] + 2 / kTopo).toFixed(1)}" y="${(le[1] - 6 / kTopo).toFixed(1)}" font-size="${(12.5 / kTopo).toFixed(1)}" font-weight="600" fill="#8fa5bd">Ungrouped devices</text>`;
  }
  // Two-pass painting (v3.4.3), exactly as on the Maps page: all
  // markers first, then all label stacks in a layer above them,
  // so a label is never buried under a neighbouring node.
  // Same status-marker grammar as the Maps page: a diamond
  // plate under the device-type glyph, name and caption below.
  // The furniture is fixed-size on screen (v3.2.1), exactly as on
  // the Maps page: it lives in a counter-scaled inner group, so a
  // node's plate, glyph and label keep their size whatever the
  // topology's fit scale. Link lines run centre to centre and
  // tuck under the opaque plates at any scale.
  let pM = '', pL = '';
  // Scene-space tidy rendering (v3.6): node furniture drawn at
  // the layout's solved scale, exactly as on the Maps page.
  const topoScene = !!(topoTidy && topoTidyPos && topoTidyScaleS > 0);
  const topoBelow = { slot: 'below', dx: 0, dy: 0, stag: false };
  for (const n of topo.nodes) {
    const st = n.device_status || '';
    const justDown = st === 'down' && topoPrevStatus[n.id] !== undefined && topoPrevStatus[n.id] !== 'down';
    topoPrevStatus[n.id] = st;
    const sub = n.ip || n.device_type || n.type || '';
    const bud = topoBudById.get(n.id) || { f: Infinity, c: Infinity };
    const plc = topoPlaceById.get(n.id) || {};
    const np = topoPosOf(n);
    const nlabel = topoLabelOf(n);
    pM += `<g class="topo-node ${st === 'down' ? 'down' : ''} ${n.id === topoSel ? 'selected' : ''} ${n.id === topoLinkFrom ? 'selected' : ''}" data-node="${n.id}" transform="translate(${np.x},${np.y})">` +
      `<title>${esc(n.label)}${n.ip ? ' · ' + esc(n.ip) : ''}${n.device_type ? ' · ' + esc(n.device_type) : ''}${st ? ' · ' + esc(st) : ''}</title>` +
      `<g class="tnode-fixed">` +
      (justDown ? `<circle class="ring-once" r="30"/>` : '') +
      `<circle class="tnode-sel" r="30"/>` +
      `<g class="dev-plate">${markerSVG(st || 'unknown')}</g>` +
      iconUseIn(n.icon_id, DEV_GLYPH_PX) +
      `</g></g>`;
    pL += `<g class="topo-node-lbl" data-node="${n.id}" transform="translate(${np.x},${np.y})">` +
      `<g class="tnode-fixed">` +
      (topoScene
        ? labelPill(nlabel.lines, sub, 'topo-name', 'topo-sub', Infinity, topoBelow)
        : labelStack(n.label, nlabel.lines, sub, 'topo-name', 'topo-sub', bud.f, bud.c, plc.f, plc.c)) +
      `</g></g>`;
  }
  wrap.innerHTML = `<svg id="topo-svg" class="${reduceMotion ? 'reduce-motion' : ''}" viewBox="${minX.toFixed(0)} ${minY.toFixed(0)} ${(maxX - minX).toFixed(0)} ${(maxY - minY).toFixed(0)}" preserveAspectRatio="xMidYMid meet">${p}${pM}${pL}</svg>`;
  if (topoScene) { const s0 = $('#topo-svg'); if (s0) s0.dataset.topoScene = '1'; }
  const svg = $('#topo-svg');
  if (topoTidy && topoSvgH > 0) svg.style.height = topoSvgH + 'px';
  topoScaleNodes(svg);
  svg.querySelectorAll('.topo-node, .topo-node-lbl').forEach(g => {
    g.addEventListener('pointerdown', ev => topoNodePointerDown(ev, g, svg));
    g.addEventListener('dblclick', () => {
      const n = topoNodeById(+g.dataset.node);
      if (n && n.device_id) openDevice(n.device_id);
    });
  });
  svg.querySelectorAll('.topo-hit').forEach(l => {
    l.addEventListener('click', async () => {
      if (!IS_ADMIN()) return; // viewers look; they do not delete
      if (confirm('Delete this link?')) {
        await api(`/api/topologies/${topo.topology.id}/links/${l.dataset.link}`, { method: 'DELETE' });
        openTopology(topo.topology.id);
      }
    });
  });
}

function topoSvgPoint(svg, evt) {
  const pt = svg.createSVGPoint();
  pt.x = evt.clientX; pt.y = evt.clientY;
  return pt.matrixTransform(svg.getScreenCTM().inverse());
}
function topoNodePointerDown(ev, g, svg) {
  const id = +g.dataset.node;
  const node = topoNodeById(id);
  if (!node) return;
  ev.preventDefault();
  const start = topoSvgPoint(svg, ev);
  const orig = { x: node.x, y: node.y };
  let moved = false;
  g.setPointerCapture(ev.pointerId);
  const onMove = e2 => {
    // Dragging repositions a node (a change): administrators only,
    // and never while Tidy View shows its arranged positions.
    if (!IS_ADMIN() || topoTidy) return;
    const pnt = topoSvgPoint(svg, e2);
    const dx = pnt.x - start.x, dy = pnt.y - start.y;
    if (Math.abs(dx) + Math.abs(dy) > 3) moved = true;
    if (moved) {
      node.x = Math.round(orig.x + dx); node.y = Math.round(orig.y + dy);
      // Marker and label live in separate layers (v3.4.3); both
      // groups of this node follow the drag.
      svg.querySelectorAll(`[data-node="${id}"]`).forEach(el =>
        el.setAttribute('transform', `translate(${node.x},${node.y})`));
    }
  };
  const onUp = async () => {
    g.removeEventListener('pointermove', onMove);
    g.removeEventListener('pointerup', onUp);
    if (moved) {
      await api(`/api/topologies/${topo.topology.id}/nodes/${id}`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(node) }).catch(() => {});
      renderTopo(); // re-draw links at the new position
    } else {
      handleTopoNodeClick(id);
    }
  };
  g.addEventListener('pointermove', onMove);
  g.addEventListener('pointerup', onUp);
}
async function handleTopoNodeClick(id) {
  if (topoLinkMode) {
    if (topoLinkFrom == null) { topoLinkFrom = id; renderTopo(); return; }
    if (topoLinkFrom === id) { topoLinkFrom = null; renderTopo(); return; }
    const label = prompt('Link label (optional):', '') || '';
    const bandwidth = prompt('Bandwidth (optional, e.g. 1 Gbps):', '') || '';
    try {
      await post(`/api/topologies/${topo.topology.id}/links`, { from_node: topoLinkFrom, to_node: id, label, bandwidth });
    } catch (e) { alert(e.message); }
    topoLinkFrom = null;
    openTopology(topo.topology.id);
    return;
  }
  selectTopoNode(id);
}
function selectTopoNode(id) {
  topoSel = id;
  const n = topoNodeById(id);
  const form = $('#topo-node-form');
  if (!n) { form.classList.add('hidden'); renderTopo(); return; }
  form.classList.remove('hidden');
  $('#topo-node-title').textContent = 'Node: ' + n.label;
  const openBtn = $('#topo-node-open');
  if (openBtn) {
    openBtn.classList.toggle('hidden', !n.device_id);
    openBtn.onclick = () => { if (n.device_id) openDevice(n.device_id); };
  }
  const el = nm => form.elements.namedItem(nm);
  el('label').value = n.label || '';
  el('ip').value = n.ip || '';
  el('type').value = n.type || '';
  el('group_name').value = n.group_name || '';
  renderTopo();
}
$('#topo-node-form').onsubmit = async e => {
  e.preventDefault();
  const n = topoNodeById(topoSel);
  if (!n) return;
  const f = new FormData(e.target);
  const body = { ...n, label: f.get('label'), ip: f.get('ip') || '', type: f.get('type') || '', group_name: f.get('group_name') || '' };
  try {
    await api(`/api/topologies/${topo.topology.id}/nodes/${topoSel}`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    openTopology(topo.topology.id);
  } catch (err) { alert(err.message); }
};
$('#topo-node-del').onclick = async () => {
  if (topoSel == null) return;
  if (!confirm('Delete this node and its links?')) return;
  await api(`/api/topologies/${topo.topology.id}/nodes/${topoSel}`, { method: 'DELETE' });
  topoSel = null;
  openTopology(topo.topology.id);
};

// ---------- settings: email alerts ----------
let emailHasPassword = false;
async function loadEmailSettings() {
  try {
    const s = await api('/api/settings/email');
    emailHasPassword = !!s.has_password;
    const f = $('#email-form');
    const el = n => f.elements.namedItem(n);
    el('enabled').checked = !!s.enabled;
    el('smtp_host').value = s.smtp_host || '';
    el('smtp_port').value = s.smtp_port || 587;
    el('tls_mode').value = s.tls_mode || 'starttls';
    el('username').value = s.username || '';
    el('password').value = '';
    el('password').placeholder = emailHasPassword ? 'A password is stored — leave blank to keep it' : 'Password';
    el('from').value = s.from || '';
    el('to').value = s.to || '';
    el('on_down').checked = !!s.on_down;
    el('on_up').checked = !!s.on_up;
  } catch (e) { $('#email-msg').textContent = 'Error: ' + e.message; }
}
$('#email-form').onsubmit = async e => {
  e.preventDefault();
  const f = new FormData(e.target);
  const typed = f.get('password');
  const body = {
    enabled: !!f.get('enabled'),
    smtp_host: f.get('smtp_host') || '',
    smtp_port: f.get('smtp_port') ? +f.get('smtp_port') : 587,
    tls_mode: f.get('tls_mode') || 'starttls',
    username: f.get('username') || '',
    password: typed ? typed : (emailHasPassword ? '********' : ''),
    from: f.get('from') || '',
    to: f.get('to') || '',
    on_down: !!f.get('on_down'),
    on_up: !!f.get('on_up'),
  };
  try {
    await api('/api/settings/email', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    $('#email-msg').textContent = 'Saved.';
    uiTick(true);
    loadEmailSettings();
  } catch (err) { uiTick(false); $('#email-msg').textContent = 'Error: ' + err.message; }
};
$('#email-test-btn').onclick = async () => {
  $('#email-msg').textContent = 'Sending test email…';
  try { await post('/api/settings/email/test', {}); uiTick(true); $('#email-msg').textContent = 'Test email sent — check the inbox.'; }
  catch (err) { uiTick(false); $('#email-msg').textContent = 'Error: ' + err.message; }
};

// ---------- traffic: NetFlow / sFlow (v2.1) ----------
let trafficRange = '1h';
function fmtBytes(b) {
  if (b == null) return '–';
  const u = ['B', 'KB', 'MB', 'GB', 'TB']; let i = 0;
  while (b >= 1000 && i < 4) { b /= 1000; i++; }
  return b.toFixed(i ? 2 : 0) + ' ' + u[i];
}
const setTrafficChart = () => makeChart($('#chart-traffic'), [{ label: 'Bytes', stroke: '#4fc3f7', fill: 'rgba(79,195,247,.15)', width: 2 }], fmtBytes);
async function loadTraffic() {
  try {
    const st = await api('/api/traffic/status');
    $('#traffic-status').textContent = st.listeners.length
      ? `Collectors: ${st.listeners.join(', ')} · ${st.packets} packets, ${st.records} flows decoded since start${st.bad_packets ? `, ${st.bad_packets} undecodable` : ''}${st.counter_samples ? `, ${st.counter_samples} sFlow counter samples` : ''}`
      : 'Collectors are disabled (ports set to 0).';
    const s = await api('/api/traffic/summary?range=' + trafficRange);
    $('#traffic-total').textContent = fmtBytes(s.total_bytes);
    $('#traffic-packets').textContent = (s.total_packets || 0).toLocaleString();
    const fill = (sel, rows, cols) => {
      $(sel + ' tbody').innerHTML = rows.map(cols).join('') || '<tr><td colspan="4" class="muted">No flow data yet in this range</td></tr>';
    };
    fill('#traffic-src', s.top_sources, t => `<tr><td>${esc(t.key)}</td><td>${fmtBytes(t.bytes)}</td><td>${t.packets.toLocaleString()}</td></tr>`);
    fill('#traffic-dst', s.top_destinations, t => `<tr><td>${esc(t.key)}</td><td>${fmtBytes(t.bytes)}</td><td>${t.packets.toLocaleString()}</td></tr>`);
    fill('#traffic-conv', s.top_conversations, t => `<tr><td>${esc(t.key)}</td><td>${esc(t.key2)}</td><td>${fmtBytes(t.bytes)}</td><td>${t.packets.toLocaleString()}</td></tr>`);
    fill('#traffic-port', s.top_ports, t => `<tr><td>${esc(t.key)}</td><td>${fmtBytes(t.bytes)}</td><td>${t.packets.toLocaleString()}</td></tr>`);
    fill('#traffic-proto', s.top_protocols, t => `<tr><td>${esc(t.key)}</td><td>${fmtBytes(t.bytes)}</td><td>${t.packets.toLocaleString()}</td></tr>`);
    fill('#traffic-exporter', s.top_exporters, t => `<tr><td>${esc(t.key)}</td><td>${fmtBytes(t.bytes)}</td><td>${t.packets.toLocaleString()}</td></tr>`);
    const ts = s.series.map(p => p.ts), vs = s.series.map(p => p.value);
    setTrafficChart()([ts, vs]);
    const recent = await api('/api/traffic/recent?limit=50');
    $('#traffic-recent tbody').innerHTML = recent.map(f =>
      `<tr><td>${fmtTime(Math.floor(new Date(f.ts).getTime() / 1000))}</td><td>${esc(f.exporter)}</td><td>${esc(f.src)}</td><td>${esc(f.dst)}</td><td>${f.dst_port}</td><td>${esc(f.proto_name || f.proto)}</td><td>${fmtBytes(f.bytes)}</td><td>${f.packets}</td></tr>`).join('')
      || '<tr><td colspan="8" class="muted">No flows received yet</td></tr>';
  } catch (e) { console.error(e); }
}
document.querySelectorAll('.trbtn').forEach(b => b.onclick = () => {
  trafficRange = b.dataset.trange;
  document.querySelectorAll('.trbtn').forEach(x => x.classList.toggle('active', x === b));
  loadTraffic();
});
$('#traffic-refresh').onclick = loadTraffic;

// ---------- logs: syslog (v2.1) ----------
async function loadLogs() {
  try {
    const f = new FormData($('#logs-form'));
    const range = f.get('range') || '24h';
    const counts = await api('/api/logs/counts?range=' + range);
    if (counts.port) $('#logs-port').textContent = counts.port;
    const order = ['emerg', 'alert', 'crit', 'err', 'warning', 'notice', 'info', 'debug'];
    $('#logs-cards').innerHTML = order.map(n =>
      `<div class="card"><h3>${n}</h3><div class="big">${counts.by_severity[n] || 0}</div></div>`).join('')
      + `<div class="card"><h3>Total (${range})</h3><div class="big">${counts.total}</div></div>`;
    const qs = new URLSearchParams();
    if (f.get('q')) qs.set('q', f.get('q'));
    if (f.get('source')) qs.set('source', f.get('source'));
    if (f.get('severity') !== '' && f.get('severity') != null) qs.set('severity', f.get('severity'));
    qs.set('range', range);
    const rows = await api('/api/logs?' + qs.toString());
    $('#logs-table tbody').innerHTML = rows.map(e =>
      `<tr><td>${fmtTime(e.ts)}</td><td>${esc(e.source_ip)}</td><td>${esc(e.hostname)}</td><td><span class="sev sev-${esc(e.severity_name)}">${esc(e.severity_name)}</span></td><td>${esc(e.app)}</td><td>${esc(e.message)}</td></tr>`).join('')
      || '<tr><td colspan="6" class="muted">No syslog messages match — point your devices at this computer\'s syslog port</td></tr>';
  } catch (e) { console.error(e); }
}
$('#logs-form').onsubmit = e => { e.preventDefault(); loadLogs(); };
setInterval(() => {
  if (!$('#view-logs').classList.contains('hidden') && $('#logs-auto').checked) loadLogs();
  if (!$('#view-traffic').classList.contains('hidden')) loadTraffic();
}, 15000);

// ---------- discovered links (v2.1) ----------
async function loadLinks() {
  try {
    const links = await api('/api/links');
    $('#links-table tbody').innerHTML = links.map(l =>
      `<tr><td><b>${esc(l.device_a_name)}</b></td><td>${esc(l.if_a)}</td><td><b>${esc(l.device_b_name)}</b></td><td>${esc(l.if_b)}</td><td>${esc(l.via)}</td><td>${esc(l.confidence)}</td><td>${fmtTime(l.last_seen)}</td></tr>`).join('')
      || '<tr><td colspan="7" class="muted">No links discovered yet — run link discovery</td></tr>';
    const st = await api('/api/links/status');
    if (st.running) $('#links-status').textContent = 'Discovery running…';
    else if (st.last_report) {
      const r = st.last_report;
      $('#links-status').textContent = `Last run: ${r.devices_ok}/${r.devices_polled} devices answered · LLDP ${r.lldp_neighbors}, CDP ${r.cdp_neighbors}, FDB ${r.fdb_entries}, ARP ${r.arp_entries} · ${r.links_total} links stored`;
    }
  } catch (e) { console.error(e); }
}
$('#links-run-btn').onclick = async () => {
  $('#links-status').textContent = 'Starting discovery…';
  try {
    await post('/api/links/discover', {});
    const poll = setInterval(async () => {
      const st = await api('/api/links/status');
      if (!st.running) { clearInterval(poll); loadLinks(); }
    }, 2000);
  } catch (e) { $('#links-status').textContent = 'Error: ' + e.message; }
};
$('#links-topo-btn').onclick = async () => {
  try {
    const r = await post('/api/links/topology', {});
    $('#links-status').textContent = `Topology created with ${r.nodes} nodes and ${r.links} links.`;
    uiTick(true);
    loadTopologies();
  } catch (e) { uiTick(false); $('#links-status').textContent = 'Error: ' + e.message; }
};

// ---------- boot ----------
applyCanvasTheme();
initAuth();
show('dashboard');
initAlarms();
updateLiveSummary();
checkServerVersion();
setInterval(updateLiveSummary, 15000);
setInterval(checkServerVersion, 15000);
setInterval(() => {
  if (!$('#view-dashboard').classList.contains('hidden')) loadDashboard();
  if (!$('#view-device').classList.contains('hidden')) {
    refreshDevice();
    loadTimeline().catch(() => {});
    loadDeviceEvents().catch(() => {});
    if (currentDevice) {
      api('/api/devices/' + currentDevice).then(d => {
        currentDeviceData = d;
        $('#dev-title').textContent = (d.status === 'up' ? '🟢 ' : d.status === 'down' ? '🔴 ' : '⚪ ') + d.name;
        renderDeviceHead(d).catch(() => {});
      }).catch(() => {});
    }
  }
}, 30000);

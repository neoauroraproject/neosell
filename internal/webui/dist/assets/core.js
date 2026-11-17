// Shared helpers: API, formatting, router, small UI components.

export const state = { boot: null, user: null, unread: 0 };

export const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
export const $ = (sel, el = document) => el.querySelector(sel);
export const $$ = (sel, el = document) => [...el.querySelectorAll(sel)];

// ---------- API ----------
export class ApiError extends Error {
  constructor(msg, status, code) { super(msg); this.status = status; this.code = code; }
}

export async function api(method, path, body, { form } = {}) {
  const opt = { method, headers: { 'X-Requested-With': 'neosell' }, credentials: 'same-origin' };
  if (form) opt.body = form;
  else if (body !== undefined) { opt.headers['Content-Type'] = 'application/json'; opt.body = JSON.stringify(body); }
  let res;
  try { res = await fetch(path, opt); } catch { throw new ApiError('اتصال به سرور برقرار نشد', 0); }
  let data = null;
  try { data = await res.json(); } catch { /* empty body */ }
  if (!res.ok) {
    if (res.status === 401 && !path.startsWith('/api/auth/')) { state.user = null; go('/login'); }
    throw new ApiError(data?.error || `خطای ${res.status}`, res.status, data?.code);
  }
  return data;
}
export const get = (p) => api('GET', p);
export const post = (p, b = {}) => api('POST', p, b);
export const put = (p, b = {}) => api('PUT', p, b);
export const del = (p) => api('DELETE', p);

// ---------- formatting ----------
const nf = new Intl.NumberFormat('fa-IR');
const nf2 = new Intl.NumberFormat('fa-IR', { maximumFractionDigits: 2 });
const dfDate = new Intl.DateTimeFormat('fa-IR-u-ca-persian', { year: 'numeric', month: 'long', day: 'numeric' });
const dfTime = new Intl.DateTimeFormat('fa-IR-u-ca-persian', { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' });
export const n = (x) => nf.format(x ?? 0);
export const n2 = (x) => nf2.format(x ?? 0);
const currencyFa = { toman: 'تومان', usd: 'دلار', usdt: 'تتر' };
export const money = (x) => `${n(x)} ${currencyFa[state.boot?.settings?.['site.currency']] || 'تومان'}`;
export function gbBase() { return Number(state.boot?.settings?.['usage.gb_base'] || 1073741824); }
export function bytes(b) {
  b = Number(b || 0);
  const g = gbBase();
  if (b >= g) return `${n2(b / g)} گیگ`;
  if (b >= g / 1024) return `${n2(b / (g / 1024))} مگ`;
  return `${n(Math.round(b / 1024))} کیلوبایت`;
}
export const date = (ts) => (ts ? dfDate.format(new Date(ts * 1000)) : '—');
export const datetime = (ts) => (ts ? dfTime.format(new Date(ts * 1000)) : '—');
export function daysLeft(ts) {
  if (!ts) return null;
  return Math.ceil((ts * 1000 - Date.now()) / 86400000);
}
export const statusFa = {
  provisioning: 'در حال ساخت', waiting_peer: 'منتظر اتصال سرور خارج', active: 'فعال', limited: 'حجم تمام شد',
  expired: 'منقضی', paused: 'متوقف', suspended: 'معلق', failed: 'ناموفق', deleted: 'حذف‌شده',
  pending: 'منتظر واریز', submitted: 'منتظر بررسی', approved: 'تأیید شد', rejected: 'رد شد',
  online: 'آنلاین', offline: 'آفلاین', error: 'خطا', unknown: 'نامشخص', open: 'باز', answered: 'پاسخ داده شد', closed: 'بسته',
  paid: 'پرداخت‌شده', refunded: 'برگشت وجه', blocked: 'مسدود', connected: 'وصل', waiting: 'منتظر',
};
export const status = (s) => `<span class="status ${esc(s)}">${esc(statusFa[s] || s)}</span>`;
export const toEn = (s) => String(s ?? '').replace(/[۰-۹]/g, (d) => '۰۱۲۳۴۵۶۷۸۹'.indexOf(d)).replace(/[٠-٩]/g, (d) => '٠١٢٣٤٥٦٧٨٩'.indexOf(d));
export const num = (s) => { const v = Number(toEn(s).replace(/[,٬\s]/g, '')); return Number.isFinite(v) ? v : 0; };

// ---------- components ----------
const ICONS = {
  home: 'M3 10.5 12 3l9 7.5V20a1 1 0 0 1-1 1h-5v-6H9v6H4a1 1 0 0 1-1-1z',
  cart: 'M3 4h2l2.4 11.2a1 1 0 0 0 1 .8h8.9a1 1 0 0 0 1-.8L20 8H6.2M9 20.5h.01M17 20.5h.01',
  wallet: 'M4 7h15a1 1 0 0 1 1 1v11a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V6a2 2 0 0 1 2-2h11M16 13.5h.01',
  chat: 'M4 5h16v11H8l-4 4z',
  user: 'M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8zm-7 9a7 7 0 0 1 14 0',
  bell: 'M6 16V11a6 6 0 1 1 12 0v5l1.5 2h-15zM10 20.5a2 2 0 0 0 4 0',
  search: 'M11 18a7 7 0 1 0 0-14 7 7 0 0 0 0 14zm5-2 4 4',
  arrow: 'M17 7 7 17M8 7h9v9',
  check: 'm5 12.5 4.2 4L19 7',
  chevL: 'm15 6-6 6 6 6', chevR: 'm9 6 6 6-6 6', chevD: 'm6 9 6 6 6-6',
  plus: 'M12 5v14M5 12h14', minus: 'M5 12h14', close: 'M6 6l12 12M18 6 6 18',
  card: 'M3 6.5A1.5 1.5 0 0 1 4.5 5h15A1.5 1.5 0 0 1 21 6.5v11a1.5 1.5 0 0 1-1.5 1.5h-15A1.5 1.5 0 0 1 3 17.5zM3 10h18M7 15h3',
  server: 'M4 4h16v6H4zM4 14h16v6H4zM8 7h.01M8 17h.01',
  globe: 'M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM3 12h18M12 3c2.5 2.6 3.8 5.6 3.8 9S14.5 18.4 12 21c-2.5-2.6-3.8-5.6-3.8-9S9.5 5.6 12 3z',
  bolt: 'M13 3 5 13.5h6L10 21l8-10.5h-6z',
  upload: 'M12 16V4m0 0-4.5 4.5M12 4l4.5 4.5M4 16v3a1 1 0 0 0 1 1h14a1 1 0 0 0 1-1v-3',
  copy: 'M9 9h10v11H9zM5 15V4h10',
  power: 'M12 3v8M6.3 6.6a8 8 0 1 0 11.4 0',
  menu: 'M4 7h16M4 12h16M4 17h16',
  logout: 'M15 4h3a1 1 0 0 1 1 1v14a1 1 0 0 1-1 1h-3M10 8l-4 4 4 4M6 12h10',
  receipt: 'M6 3h12v18l-3-2-3 2-3-2-3 2zM9 8h6M9 12h6',
  chart: 'M4 20V10M10 20V4M16 20v-7M22 20H2',
  grid: 'M4 4h7v7H4zM13 4h7v7h-7zM4 13h7v7H4zM13 13h7v7h-7z',
  port: 'M6 4h12v9a6 6 0 0 1-12 0zM9 4V1M15 4V1M12 19v4',
  sparkle: 'M12 3v4M12 17v4M3 12h4M17 12h4M6 6l2.5 2.5M15.5 15.5 18 18M6 18l2.5-2.5M15.5 8.5 18 6',
  ticket: 'M4 7a2 2 0 0 0 2-2h12a2 2 0 0 0 2 2v3a2 2 0 0 0 0 4v3a2 2 0 0 0-2 2H6a2 2 0 0 0-2-2v-3a2 2 0 0 0 0-4z',
  settings: 'M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM12 2v3M12 19v3M2 12h3M19 12h3M4.9 4.9 7 7M17 17l2.1 2.1M4.9 19.1 7 17M17 7l2.1-2.1',
};
export function icon(name, size = 18, sw = 1.8) {
  return `<svg width="${size}" height="${size}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="${sw}" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="${ICONS[name] || ''}"/></svg>`;
}

// Ring gauge (match-score style). pct 0..100.
export function ring(pct, { size = 112, stroke = 7, big = null, unit = '%', label = '', cls = '', ticks = true } = {}) {
  const r = 50 - stroke / 2 - 1;
  const c = 2 * Math.PI * r;
  const p = Math.max(0, Math.min(100, pct || 0));
  const fs = Math.round(size * 0.27);
  const base = ticks
    ? `<circle class="tk" cx="50" cy="50" r="${r}" fill="none" stroke-width="${stroke}"/>`
    : `<circle class="tr" cx="50" cy="50" r="${r}" fill="none" stroke-width="${stroke}"/>`;
  return `<div class="ring ${cls}" style="width:${size}px;height:${size}px" role="img" aria-label="${esc(label)} ${Math.round(p)}%">
    <svg viewBox="0 0 100 100">${base}<circle class="val" cx="50" cy="50" r="${r}" fill="none" stroke-width="${stroke}" stroke-dasharray="${(p / 100) * c} ${c}"/></svg>
    <div class="ring-in"><b style="font-size:${fs}px">${big ?? n(Math.round(p))}<span style="font-size:${Math.round(fs * 0.5)}px">${unit}</span></b>${label ? `<small>${esc(label)}</small>` : ''}</div></div>`;
}

// Thin usage bar for compact places (tables, admin).
export function meter(used, total, label = '') {
  if (!total) return `<div class="meter"><div class="meter-text"><span>${label}${bytes(used)} مصرف</span><span>نامحدود</span></div></div>`;
  const ratio = Math.min(used / total, 1);
  const cls = ratio >= 1 ? 'full' : ratio >= 0.8 ? 'warn' : '';
  return `<div class="meter ${cls}" role="meter" aria-valuemin="0" aria-valuemax="${total}" aria-valuenow="${used}">
    <div class="meter-bar"><i style="width:${(ratio * 100).toFixed(1)}%"></i></div>
    <div class="meter-text"><span>${label}${bytes(used)} از ${bytes(total)}</span><span>${n(Math.round(ratio * 100))}٪</span></div></div>`;
}

export function usagePct(used, total) { return total ? Math.min(100, (used / total) * 100) : 0; }

// Concentric hero arcs: items inner → outer, each {label}.
export function arcs(items, footStart = '', footEnd = '', caption = '') {
  const W = 440, H = 250, cx = 220, base = 222;
  const rs = items.map((_, i) => 70 + i * 70);
  const outer = rs[rs.length - 1];
  const path = (r) => `M${cx - r} ${base} A ${r} ${r} 0 0 1 ${cx + r} ${base} Z`;
  const pins = items.map((it, i) => {
    const r = rs[i];
    const ang = Math.PI * (0.62 - i * 0.08);
    const x = cx + r * Math.cos(ang), y = base - r * Math.sin(ang);
    return `<circle class="pin" cx="${x.toFixed(1)}" cy="${y.toFixed(1)}" r="3.5"/><text x="${x.toFixed(1)}" y="${(y - 12).toFixed(1)}" text-anchor="middle">${esc(it.label)}</text>`;
  }).join('');
  return `<svg class="arcs" viewBox="0 -14 ${W} ${H + 34}" role="img" aria-label="${esc(caption)}">
    <defs><linearGradient id="arcGrad" x1="1" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#ee4b2b"/><stop offset=".45" stop-color="#f6a38e"/><stop offset="1" stop-color="#fbe4dd" stop-opacity=".6"/></linearGradient>
    <pattern id="hatch" width="8" height="8" patternUnits="userSpaceOnUse" patternTransform="rotate(45)"><rect width="8" height="8" fill="rgba(28,33,48,.04)"/><line x1="0" y1="0" x2="0" y2="8" stroke="rgba(28,33,48,.13)" stroke-width="3"/></pattern></defs>
    ${rs[2] ? `<path class="a3" d="${path(rs[2])}"/>` : ''}${rs[1] ? `<path class="a2" d="${path(rs[1])}"/>` : ''}<path class="a1" d="${path(rs[0])}"/>
    <line class="base" x1="${cx - outer}" x2="${cx + outer}" y1="${base}" y2="${base}"/>${pins}
    <text class="lbl" x="${cx + outer}" y="${base + 24}" text-anchor="end">${esc(footStart)}</text>
    <text class="lbl" x="${cx}" y="${base + 24}" text-anchor="middle">${esc(caption)}</text>
    <text class="lbl" x="${cx - outer}" y="${base + 24}" text-anchor="start">${esc(footEnd)}</text></svg>`;
}

// Lollipop bars (income-tracker style). points: [{label, value, tip}], on = selected index.
export function lollipop(points, on = points.length - 1) {
  const max = Math.max(...points.map((p) => p.value), 1);
  return `<div class="lolli" role="group">${points.map((p, i) => `<button type="button" class="col ${i === on ? 'on' : ''}" data-li="${i}" aria-pressed="${i === on}" title="${esc(p.tip)}">
    ${i === on ? `<span class="tip">${esc(p.tip)}</span>` : ''}<span class="stick" style="height:${Math.max(6, (p.value / max) * 100)}%"></span><span class="day">${esc(p.label)}</span></button>`).join('')}</div>`;
}
export function bindLollipop(root, points) {
  root.addEventListener('click', (e) => {
    const b = e.target.closest('[data-li]');
    if (!b) return;
    b.closest('.lolli').outerHTML = lollipop(points, +b.dataset.li);
  });
}

// Smooth area chart (usage pattern). points [{at, up, down}].
export function areaChart(points, { daily = false } = {}) {
  if (!points.length) return '<p class="muted small">هنوز مصرفی ثبت نشده است.</p>';
  const W = 640, H = 170, pad = 18;
  const tot = points.map((p) => p.up + p.down);
  const max = Math.max(...tot, 1);
  const xs = points.map((_, i) => (points.length === 1 ? W / 2 : pad + (i * (W - 2 * pad)) / (points.length - 1)));
  const ys = tot.map((v) => H - pad - (v / max) * (H - 2 * pad - 14));
  let d = `M${xs[0]} ${ys[0]}`;
  for (let i = 1; i < xs.length; i++) {
    const mx = (xs[i - 1] + xs[i]) / 2;
    d += ` C${mx} ${ys[i - 1]} ${mx} ${ys[i]} ${xs[i]} ${ys[i]}`;
  }
  const area = `${d} L${xs[xs.length - 1]} ${H - pad} L${xs[0]} ${H - pad} Z`;
  const peak = tot.indexOf(max);
  const fmt = (ts) => new Intl.DateTimeFormat('fa-IR-u-ca-persian', daily ? { month: 'short', day: 'numeric' } : { hour: '2-digit', minute: '2-digit' }).format(new Date(ts * 1000));
  const step = Math.max(1, Math.ceil(points.length / 7));
  const tx = Math.min(W - 70, Math.max(70, xs[peak]));
  const ty = Math.max(16, ys[peak] - 22);
  return `<svg class="area" viewBox="0 0 ${W} ${H + 16}" preserveAspectRatio="none" role="img" aria-label="نمودار مصرف">
    <defs><linearGradient id="areaGrad" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#ee4b2b" stop-opacity=".22"/><stop offset="1" stop-color="#ee4b2b" stop-opacity="0"/></linearGradient></defs>
    ${[0.25, 0.5, 0.75].map((f) => `<line class="grid-l" x1="0" x2="${W}" y1="${pad + f * (H - 2 * pad)}" y2="${pad + f * (H - 2 * pad)}"/>`).join('')}
    <path class="fill" d="${area}"/><path class="line" d="${d}"/>
    <circle cx="${xs[peak]}" cy="${ys[peak]}" r="5" fill="#fff" stroke="#ee4b2b" stroke-width="2.5"/>
    <g transform="translate(${tx} ${ty})"><rect x="-58" y="-14" width="116" height="24" rx="8" fill="#fff" stroke="#f3b3a3"/><text x="0" y="3" text-anchor="middle" style="fill:#1c2130;font-size:11px">${esc(bytes(max))}</text></g>
    ${points.map((p, i) => (i % step === 0 || i === points.length - 1 ? `<text x="${xs[i]}" y="${H + 12}" text-anchor="middle">${esc(fmt(p.at))}</text>` : '')).join('')}</svg>`;
}

// Segmented progress (proposal-progress style). groups: [{label, value, of}].
export function segprog(groups) {
  return `<div class="segprog">${groups.map((g, i) => {
    const f = g.of ? Math.round((g.value / g.of) * 14) : Math.min(14, g.value);
    return `<div class="grp c${(i % 3) + 1}"><span>${esc(g.label)}</span><b>${n(g.value)}</b><div class="ticks">${Array.from({ length: 14 }, (_, k) => `<i class="${k < f ? 'f' : ''}"></i>`).join('')}</div></div>`;
  }).join('')}</div>`;
}

// Thermostat dial. pct 0..100 along a 270° arc.
export function dial({ pct = 0, big = '', small = '', calm = false }) {
  const r = 44, c = 2 * Math.PI * r, arc = c * 0.75;
  const p = Math.max(0, Math.min(100, pct));
  const ang = ((135 + 270 * (p / 100)) * Math.PI) / 180;
  const kx = 50 + r * Math.cos(ang), ky = 50 + r * Math.sin(ang);
  return `<div class="dial ${calm ? 'calm' : ''}" role="img" aria-label="${esc(small)}">
    <svg viewBox="0 0 100 100" style="rotate:0deg"><circle cx="50" cy="50" r="${r}" fill="none" stroke="var(--track)" stroke-width="3" stroke-dasharray="${arc} ${c}" stroke-linecap="round" transform="rotate(135 50 50)"/>
    <circle cx="50" cy="50" r="${r}" fill="none" stroke="${calm ? 'var(--navy)' : '#ee4b2b'}" stroke-width="3.4" stroke-dasharray="${(p / 100) * arc} ${c}" stroke-linecap="round" transform="rotate(135 50 50)"/></svg>
    <span class="knob" style="left:calc(${kx.toFixed(1)}% - 7px);top:calc(${ky.toFixed(1)}% - 7px)"></span>
    <div class="face"><div><b>${big}</b><br><small>${esc(small)}</small></div></div></div>`;
}

export function steps(labels, at) {
  return `<ol class="steps">${labels.map((l, i) => `<li class="${i < at ? 'done' : i === at ? 'on' : ''}" ${i === at ? 'aria-current="step"' : ''}>${esc(l)}</li>`).join('')}</ol>`;
}

export function counter(name, value, { min = 0, max = 0, step = 1, label = '' } = {}) {
  return `<div class="counter" data-min="${min}" data-max="${max}" data-step="${step}">
    <button type="button" data-d="-1" aria-label="کمتر">${icon('minus', 16, 2)}</button><input name="${name}" value="${value}" inputmode="numeric" aria-label="${esc(label || name)}">
    <button type="button" data-d="1" aria-label="بیشتر">${icon('plus', 16, 2)}</button></div>`;
}
document.addEventListener('click', (e) => {
  const b = e.target.closest('.counter [data-d]');
  if (!b) return;
  const box = b.closest('.counter');
  const inp = box.querySelector('input');
  const step = +box.dataset.step || 1, min = +box.dataset.min || 0, max = +box.dataset.max || 0;
  let v = num(inp.value) + step * +b.dataset.d;
  if (v < min) v = min;
  if (max && v > max) v = max;
  inp.value = v;
  inp.dispatchEvent(new Event('input', { bubbles: true }));
});

export function socket(p, svcStatus) {
  const lamp = svcStatus === 'active' ? 'on' : ['limited', 'expired', 'waiting_peer', 'provisioning'].includes(svcStatus) ? 'warn' : 'off';
  return `<div class="tile-svc" title="${esc(p.host)}:${p.port}"><small>${esc(p.host)}</small><b>${p.port}</b><small>${p.to_port && p.to_port !== p.port ? `→ ${p.to_port}` : 'same port'}</small><span class="lamp ${lamp}"></span></div>`;
}

export function chart(points, daily) { return areaChart(points, { daily }); }

export function copyField(value, label = 'کپی') {
  return `<div class="copy"><input readonly value="${esc(value)}" aria-label="${esc(label)}"><button type="button" class="btn small" data-copy="${esc(value)}">${icon('copy', 15)}${esc(label)}</button></div>`;
}

export function empty(text, action = '', ic = 'sparkle') { return `<div class="empty"><span class="icon-chip">${icon(ic, 20)}</span><p>${text}</p>${action}</div>`; }

let toastTimer;
export function toast(msg, bad = false) {
  let el = $('#toast');
  if (!el) { el = document.createElement('div'); el.id = 'toast'; el.setAttribute('role', 'status'); document.body.append(el); }
  el.className = 'toast' + (bad ? ' bad' : '');
  el.textContent = msg;
  el.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { el.hidden = true; }, bad ? 6000 : 3000);
}
export const fail = (e) => toast(e?.message || String(e), true);

export function modal(html, { wide } = {}) {
  const back = document.createElement('div');
  back.className = 'modal-back';
  back.innerHTML = `<div class="modal ${wide ? 'wide' : ''}" role="dialog" aria-modal="true">${html}</div>`;
  const close = () => { back.remove(); document.removeEventListener('keydown', onKey); };
  const onKey = (e) => { if (e.key === 'Escape') close(); };
  back.addEventListener('click', (e) => { if (e.target === back || e.target.closest('[data-close]')) close(); });
  document.addEventListener('keydown', onKey);
  document.body.append(back);
  back.querySelector('input,select,textarea,button')?.focus();
  return { el: back.firstElementChild, close };
}

export function confirmBox(text, okLabel = 'تأیید') {
  return new Promise((resolve) => {
    const m = modal(`<div class="form"><p>${text}</p><div class="row end"><button class="btn ghost" data-close>انصراف</button><button class="btn primary" id="ok">${esc(okLabel)}</button></div></div>`);
    m.el.querySelector('#ok').onclick = () => { m.close(); resolve(true); };
    m.el.parentElement.addEventListener('click', (e) => { if (e.target.closest('[data-close]') || e.target === m.el.parentElement) resolve(false); });
  });
}

// Disables a button while an async action runs and reports errors.
export async function busy(btn, fn) {
  if (btn) btn.disabled = true;
  try { return await fn(); } catch (e) { fail(e); } finally { if (btn) btn.disabled = false; }
}

// ---------- generic forms ----------
// field: {key, label, type: text|number|password|bool|select|textarea|code|list|nums|multi|json|money|bytesGB|date, options:[[v,l]], help, required}
export function fieldHTML(f, v) {
  const id = `f_${f.key.replace(/\W/g, '_')}`;
  const help = f.help ? `<small>${f.help}</small>` : '';
  const req = f.required ? 'required' : '';
  const val = v ?? f.default ?? '';
  switch (f.type) {
    case 'bool':
      return `<label class="check"><input type="checkbox" name="${f.key}" ${val ? 'checked' : ''}> ${esc(f.label)}</label>`;
    case 'select':
      return `<label class="field" for="${id}">${esc(f.label)}<select id="${id}" name="${f.key}" ${req}>${(f.options || []).map(([ov, ol]) => `<option value="${esc(ov)}" ${String(ov) === String(val) ? 'selected' : ''}>${esc(ol)}</option>`).join('')}</select>${help}</label>`;
    case 'multi': {
      const set = new Set((val || []).map(String));
      return `<fieldset class="field" style="border:0;padding:0;margin:0"><legend style="font-weight:600;font-size:.9rem;margin-bottom:4px">${esc(f.label)}</legend><div class="chips">${(f.options || []).map(([ov, ol]) => `<label class="chip"><input type="checkbox" name="${f.key}" value="${esc(ov)}" ${set.has(String(ov)) ? 'checked' : ''}>${esc(ol)}</label>`).join('') || '<span class="muted small">موردی تعریف نشده</span>'}</div>${help}</fieldset>`;
    }
    case 'textarea': case 'code': case 'json':
      return `<label class="field" for="${id}">${esc(f.label)}<textarea id="${id}" name="${f.key}" class="${f.type !== 'textarea' ? 'code' : ''}" ${req}>${esc(f.type === 'json' ? JSON.stringify(val || f.empty || {}, null, 2) : val)}</textarea>${help}</label>`;
    case 'list': case 'nums':
      return `<label class="field" for="${id}">${esc(f.label)}<input id="${id}" name="${f.key}" value="${esc((val || []).join(', '))}" ${req}>${help}</label>`;
    case 'date': {
      const d = val ? new Date(val * 1000) : null;
      const s = d ? new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16) : '';
      return `<label class="field" for="${id}">${esc(f.label)}<input id="${id}" type="datetime-local" name="${f.key}" value="${s}">${help}</label>`;
    }
    case 'bytesGB':
      return `<label class="field" for="${id}">${esc(f.label)}<input id="${id}" name="${f.key}" inputmode="decimal" value="${val ? +(val / gbBase()).toFixed(2) : 0}">${help}</label>`;
    default:
      return `<label class="field" for="${id}">${esc(f.label)}<input id="${id}" name="${f.key}" type="${f.type === 'password' ? 'password' : 'text'}" ${f.type === 'number' || f.type === 'money' ? 'inputmode="decimal"' : ''} value="${esc(val)}" ${req} ${f.ltr ? 'dir="ltr"' : ''} autocomplete="off">${help}</label>`;
  }
}

export function readFields(form, fields) {
  const out = {};
  for (const f of fields) {
    const els = form.querySelectorAll(`[name="${CSS.escape(f.key)}"]`);
    const el = els[0];
    if (!el) continue;
    let v;
    switch (f.type) {
      case 'bool': v = el.checked; break;
      case 'number': case 'money': v = el.value.trim() === '' ? 0 : num(el.value); break;
      case 'select': v = f.numeric ? num(el.value) : el.value; break;
      case 'multi': v = [...els].filter((x) => x.checked).map((x) => (f.numeric ? num(x.value) : x.value)); break;
      case 'list': v = el.value.split(/[,،\n]/).map((s) => s.trim()).filter(Boolean); break;
      case 'nums': v = el.value.split(/[,،\s]+/).map((s) => num(s)).filter((x) => x); break;
      case 'json':
        try { v = el.value.trim() ? JSON.parse(el.value) : (f.empty ?? {}); } catch { throw new Error(`${f.label}: JSON نامعتبر است`); }
        break;
      case 'date': v = el.value ? Math.floor(new Date(el.value).getTime() / 1000) : 0; break;
      case 'bytesGB': v = Math.round(num(el.value) * gbBase()); break;
      default: v = el.value.trim();
    }
    setPath(out, f.key, v);
  }
  return out;
}

export function getPath(o, key) { return key.split('.').reduce((a, k) => (a == null ? undefined : a[k]), o); }
export function setPath(o, key, v) {
  const ks = key.split('.');
  let cur = o;
  ks.slice(0, -1).forEach((k) => { cur[k] = cur[k] ?? {}; cur = cur[k]; });
  cur[ks.at(-1)] = v;
}

// ---------- router ----------
const routes = [];
export function route(pattern, view, opts = {}) { routes.push({ pattern, view, ...opts }); }
let renderHook = null;
export function onRender(fn) { renderHook = fn; }

export function go(path, replace = false) {
  if (location.pathname + location.search !== path) history[replace ? 'replaceState' : 'pushState'](null, '', path);
  renderHook?.();
}
window.addEventListener('popstate', () => renderHook?.());

export function match(path) {
  for (const r of routes) {
    const m = path.match(r.pattern);
    if (m) return { ...r, params: m.slice(1) };
  }
  return null;
}

// Internal link clicks and copy buttons (event delegation for the whole app).
document.addEventListener('click', (e) => {
  const cp = e.target.closest('[data-copy]');
  if (cp) {
    e.preventDefault();
    navigator.clipboard?.writeText(cp.dataset.copy).then(() => toast('کپی شد'), () => toast('کپی نشد؛ دستی انتخاب کنید', true));
    return;
  }
  const a = e.target.closest('a[href]');
  if (!a || a.target || a.hasAttribute('download') || e.ctrlKey || e.metaKey || e.shiftKey) return;
  const href = a.getAttribute('href');
  if (!href.startsWith('/') || href.startsWith('/api/') || href.startsWith('/sub/') || href.startsWith('/i/')) return;
  e.preventDefault();
  go(href);
});

// Keeps the top-bar balance chip in sync after payments.
export async function refreshBalance() {
  const chip = document.querySelector('.balance-chip');
  if (!chip) return;
  try { const w = await get('/api/wallet'); chip.innerHTML = `<small>موجودی</small>${money(w.balance)}`; } catch { /* ignore */ }
}

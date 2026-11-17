import {
  state, esc, $, $$, get, post, api, n, money, bytes, date, datetime, daysLeft, status, copyField, empty, toast, fail,
  modal, busy, num, go, route, gbBase, icon, ring, arcs, lollipop, bindLollipop, areaChart, segprog, dial, steps, counter,
  socket, usagePct, meter, refreshBalance,
} from './core.js';

const modelInfo = {
  package: ['بسته‌ی آماده', 'حجم، مدت و پورت مشخص با قیمت ثابت', 'grid'],
  builder: ['سفارشی', 'تعداد پورت، حجم و مدت را خودتان انتخاب کنید', 'settings'],
  per_port: ['به‌ازای پورت', 'هزینه بر اساس تعداد و نوع پورت', 'port'],
  per_gb: ['فقط حجم', 'هر چقدر حجم لازم دارید، بدون نگرانی از مدت', 'chart'],
  unlimited: ['حجم نامحدود', 'بدون سقف حجم، با سقف سرعت', 'bolt'],
  payg: ['پرداخت به‌اندازه‌ی مصرف', 'هر ساعت از کیف پول کم می‌شود', 'wallet'],
};
const coreFa = { neotunnel: 'NeoTunnel', kariz: 'Kariz', backpack: 'Backpack' };
const deliveryFa = { shared: ['روی سرور خارج ما', 'بدون نیاز به سرور خارج'], dedicated: ['به سرور خارج خودم', 'یک دستور روی سرور خودتان اجرا می‌کنید'] };
const protoFa = { tcp: 'TCP', udp: 'UDP', both: 'TCP و UDP' };

function svcTotals(s) {
  if (s.type === 'outbound') {
    const total = (s.locations || []).reduce((a, l) => a + l.quota_bytes, 0);
    const used = (s.locations || []).reduce((a, l) => a + l.used_up + l.used_down, 0);
    return { used, total };
  }
  return { used: s.used_up + s.used_down, total: s.quota_bytes };
}

function svcCard(s, lead) {
  const { used, total } = svcTotals(s);
  const left = daysLeft(s.expires_at);
  const pct = total ? usagePct(used, total) : 0;
  const sub = s.type === 'port' ? `${n((s.ports || []).length)} پورت` : `${n((s.locations || []).length)} لوکیشن`;
  return `<a class="svc-card ${lead ? 'lead' : ''}" href="/services/${s.id}">
    <div class="head"><div><h3>${esc(s.title)}</h3><span class="muted small">${sub} · ${status(s.status)}</span></div><span class="arrow">${icon('arrow', 16)}</span></div>
    <div class="body">${total ? ring(pct, { size: 108, label: 'مصرف', cls: lead ? '' : 'calm' }) : ring(100, { size: 108, big: '∞', unit: '', label: 'نامحدود', cls: 'calm' })}
      <ul class="checks"><li>${icon('check', 15, 2.2)}${left === null ? 'بدون انقضا' : left > 0 ? `${n(left)} روز مانده` : 'مدت تمام شد'}</li>
      <li>${icon('check', 15, 2.2)}${total ? `${bytes(Math.max(total - used, 0))} مانده` : `${bytes(used)} مصرف`}</li>
      <li>${icon('check', 15, 2.2)}${s.rate_mbit ? `${n(s.rate_mbit)} مگابیت` : 'سرعت آزاد'}</li></ul></div>
    ${s.type === 'port' ? `<div class="portline">${(s.ports || []).slice(0, 6).map((p) => `<span>${p.port}</span>`).join('')}</div>` : `<div class="chips">${(s.locations || []).map((l) => `<span class="chip soft">${esc(l.flag)} ${esc(l.name)}</span>`).join('')}</div>`}</a>`;
}

// Per-day totals (last 7 days) from several services' usage.
async function weekUsage(svcs) {
  const days = Array.from({ length: 7 }, (_, i) => { const d = new Date(); d.setHours(0, 0, 0, 0); d.setDate(d.getDate() - 6 + i); return d; });
  const sums = days.map(() => 0);
  await Promise.all(svcs.slice(0, 8).map(async (s) => {
    try {
      const r = await get(`/api/services/${s.id}/usage?range=30d`);
      for (const p of r.points) {
        const k = days.findIndex((d) => new Date(p.at * 1000).toDateString() === d.toDateString());
        if (k >= 0) sums[k] += p.up + p.down;
      }
    } catch { /* skip */ }
  }));
  const wd = new Intl.DateTimeFormat('fa-IR', { weekday: 'narrow' });
  return days.map((d, i) => ({ label: wd.format(d), value: sums[i], tip: bytes(sums[i]) }));
}

// ---------- dashboard ----------
route(/^\/$/, async (main) => {
  const [svcs, wallet, notes] = await Promise.all([get('/api/services'), get('/api/wallet'), get('/api/notifications')]);
  const live = svcs.items.filter((s) => !['deleted', 'failed'].includes(s.status));
  const active = live.filter((s) => s.status === 'active');
  const ports = live.reduce((a, s) => a + (s.ports || []).length, 0);
  const locs = new Set(live.flatMap((s) => (s.locations || []).map((l) => l.location_id))).size;
  const used = live.reduce((a, s) => a + svcTotals(s).used, 0);
  const soon = live.filter((s) => { const d = daysLeft(s.expires_at); return d !== null && d <= 5 && d > 0; });
  const name = state.user.name?.split(' ')[0] || '';
  const filters = [['', 'همه'], ['active', 'فعال'], ['limited', 'حجم تمام'], ['expired', 'منقضی']];
  let filter = '';

  main.innerHTML = `
    <section class="hero">
      <div><h1 class="display">${name ? `سلام<br>${esc(name)}` : 'سرویس‌های<br>شما'}</h1>
        <p style="margin-top:14px"><span class="tagpill">${n(active.length)} سرویس فعال</span></p>
        <p class="lede">مصرف، زمان باقی‌مانده و وضعیت هر پورت و لوکیشن را اینجا ببینید؛ تمدید و خرید تازه هم قدم‌به‌قدم است.</p>
        <div class="core-dots" aria-label="میان‌بُرها"><a href="/shop" title="خرید">${icon('cart', 18)}</a><a href="/wallet" title="کیف پول">${icon('wallet', 18)}</a><a href="/tickets" title="پشتیبانی">${icon('chat', 18)}</a></div></div>
      <div>${arcs([{ label: `${n(ports)} پورت` }, { label: `${n(locs)} لوکیشن` }, { label: bytes(used) }], '', '', 'مصرف این دوره')}</div>
      <div class="side-stack">
        <details class="acc" open><summary>کیف پول</summary><div class="acc-body"><div class="line"><b class="num" style="font-size:1.4rem;font-weight:400">${money(wallet.balance)}</b></div>
          <a class="btn white small" href="/wallet?topup=1">${icon('plus', 15, 2)}شارژ کیف پول</a></div></details>
        <details class="acc" ${soon.length ? 'open' : ''}><summary>نزدیک انقضا (${n(soon.length)})</summary><div class="acc-body">${soon.length ? soon.map((s) => `<a class="line" href="/services/${s.id}">${icon('bolt', 15)} ${esc(s.title)} <b>${n(daysLeft(s.expires_at))} روز</b></a>`).join('') : 'همه‌چیز رو به راه است.'}</div></details>
        <details class="acc"><summary>اعلان‌ها ${notes.unread ? `(${n(notes.unread)})` : ''}</summary><div class="acc-body">${notes.items.slice(0, 3).map((x) => `<div class="line">${icon('bell', 15)} ${esc(x.title)}</div>`).join('') || 'اعلانی نیست.'}<a class="small" href="/account#notes" style="color:var(--coral)">همه‌ی اعلان‌ها</a></div></details>
      </div>
    </section>
    <div class="filterbar"><div class="chips" role="group" aria-label="فیلتر">${filters.map(([k, l]) => `<button type="button" class="chip ${k === filter ? 'on' : ''}" data-f="${k}">${l}</button>`).join('')}</div>
      <div class="row"><button class="iconbtn" data-scroll="1" aria-label="قبلی">${icon('chevR', 18)}</button><span class="num ink2" id="cnt"></span><button class="iconbtn" data-scroll="-1" aria-label="بعدی">${icon('chevL', 18)}</button></div></div>
    <div id="rail"></div>
    <div class="split" style="margin-top:22px">
      <section class="card"><div class="card-head"><div class="row"><span class="icon-chip">${icon('chart', 20)}</span><div><h2>مصرف هفته</h2><span class="muted small">همه‌ی سرویس‌ها، روزانه</span></div></div></div><div id="week"><p class="muted small">در حال محاسبه…</p></div></section>
      <section class="card flat dotted" style="background-color:var(--glass);display:grid;gap:12px;align-content:space-between;min-height:260px">
        <div><h2 style="font-size:1.4rem;font-weight:400">سرویس تازه بخرید</h2><p class="ink2" style="margin-top:6px">پورت یا اوتباند، در چهار قدم ساده با قیمت دقیق پیش از پرداخت.</p></div>
        <a class="btn white block" href="/shop" style="justify-content:space-between">شروع خرید ${icon('chevL', 18)}</a></section></div>`;

  const drawRail = () => {
    const list = live.filter((s) => !filter || s.status === filter);
    $('#rail', main).innerHTML = list.length ? `<div class="rail" id="railbox">${list.map((s, i) => svcCard(s, i === 0)).join('')}</div>` : empty(live.length ? 'سرویسی با این وضعیت نیست.' : 'هنوز سرویسی ندارید.', '<a class="btn coral" href="/shop">اولین سرویس را بخرید</a>', 'cart');
    $('#cnt', main).textContent = `${n(list.length)} سرویس`;
    $$('[data-f]', main).forEach((b) => b.classList.toggle('on', b.dataset.f === filter));
  };
  drawRail();
  main.addEventListener('click', (e) => {
    const f = e.target.closest('[data-f]');
    if (f) { filter = f.dataset.f; drawRail(); }
    const sc = e.target.closest('[data-scroll]');
    if (sc) $('#railbox', main)?.scrollBy({ left: -320 * +sc.dataset.scroll, behavior: 'smooth' });
  });
  const pts = await weekUsage(live);
  const box = $('#week', main);
  if (box) { box.innerHTML = lollipop(pts); bindLollipop(box, pts); }
});

// ---------- purchase wizard ----------
const WIZ = ['محصول', 'روش خرید', 'تنظیمات', 'پرداخت'];

async function shopWizard(main, params) {
  const data = await get('/api/shop/products');
  if (!data.items.length) { main.innerHTML = empty('فعلاً محصولی برای فروش نیست.', '', 'cart'); return; }
  const w = { step: 0, product: null, sel: null, quote: null, balance: 0, err: '', idem: crypto.randomUUID() };
  const pid = +(params?.[0] || 0);
  if (pid) { w.product = data.items.find((p) => p.id === pid); if (w.product) startProduct(); }
  else if (data.items.length === 1) { w.product = data.items[0]; startProduct(); }

  function startProduct() {
    const o = w.product.options;
    w.sel = { model: o.packages?.length && o.models.includes('package') ? 'package' : o.models[0], package_id: o.packages?.[0]?.id,
      days: o.durations?.[0] || o.min_days || 30, gb: o.min_gb || 10, ports: [{ port: '', to_port: 0 }], locations: [],
      core: o.cores?.[0] || '', delivery: o.deliveries?.[0] || '', proto: o.protos?.[0] || '', coupon: '' };
    w.step = o.models.length > 1 || o.packages?.length > 1 ? 1 : 2;
  }

  function selection() {
    const s = w.sel, p = w.product;
    const out = { model: s.model, coupon: s.coupon };
    if (s.model === 'package') {
      out.package_id = s.package_id;
      if (p.type === 'port') { const pk = p.options.packages.find((x) => x.id === s.package_id); out.ports = Array.from({ length: pk?.ports || 1 }, () => ({})); Object.assign(out, { core: s.core, proto: s.proto, delivery: s.delivery, peer_address: s.peer_address }); }
      return out;
    }
    Object.assign(out, { days: +s.days, gb: +s.gb });
    if (s.rate_mbit) out.rate_mbit = +s.rate_mbit;
    if (s.max_ips) out.max_ips = +s.max_ips;
    if (p.type === 'port') {
      out.ports = s.ports.map((x) => ({ port: String(x.port || '').trim(), to_port: +x.to_port || 0 }));
      Object.assign(out, { core: s.core, proto: s.proto, delivery: s.delivery, peer_address: s.peer_address });
    } else out.locations = s.locations.map((l) => ({ location_id: l.location_id, gb: +l.gb || 0 }));
    return out;
  }

  let qt, qseq = 0;
  function requote() {
    clearTimeout(qt);
    qt = setTimeout(async () => {
      if (!w.product || w.step < 1) return;
      const my = ++qseq;
      try {
        const r = await post('/api/orders/quote', { kind: 'new', product_id: w.product.id, selection: selection() });
        if (my !== qseq) return;
        w.quote = r.quote; w.balance = r.balance; w.err = '';
      } catch (e) { if (my !== qseq) return; w.quote = null; w.err = e.message; }
      drawAside();
    }, 300);
  }

  function receiptHTML(full) {
    const q = w.quote;
    if (w.err) return `<p class="err">${esc(w.err)}</p>`;
    if (!q) return '<p class="muted small">در حال محاسبه…</p>';
    if (q.payg_per_gb || q.payg_per_day) return `<div class="receipt"><div class="quote-line"><span>هر گیگ مصرف</span><span>${money(q.payg_per_gb)}</span></div><div class="quote-line"><span>هر روز</span><span>${money(q.payg_per_day)}</span></div><p class="small muted">هزینه هر ساعت از کیف پول کم می‌شود.</p></div>`;
    return `<div class="receipt">${(full ? q.lines : q.lines.slice(0, 4)).map((l) => `<div class="quote-line"><span>${esc(l.label)}</span><span>${money(l.amount)}</span></div>`).join('')}
      ${q.adjustments.map((l) => `<div class="quote-line adj"><span>${esc(l.label)}</span><span>${money(l.amount)}</span></div>`).join('')}
      <div class="quote-total"><span class="ink2">مبلغ نهایی</span><b>${money(q.total)}</b></div></div>`;
  }

  function drawAside() {
    const a = $('#wiz-aside', main);
    if (!a) return;
    if (!w.product || w.step === 0) { a.innerHTML = `<div class="card flat"><h2>صورت‌حساب</h2><p class="muted small" style="margin-top:8px">بعد از انتخاب محصول، قیمت لحظه‌به‌لحظه اینجا محاسبه می‌شود.</p></div>`; return; }
    const short = w.quote && w.balance < w.quote.total;
    a.innerHTML = `<div class="card"><div class="card-head" style="margin-bottom:10px"><h2>صورت‌حساب</h2><span class="chip soft">${esc(w.product.title)}</span></div>${receiptHTML(false)}
      <div class="spread small" style="margin-top:12px"><span class="muted">موجودی کیف پول</span><span class="num" style="color:${short ? 'var(--fault)' : 'var(--ink)'}">${money(w.balance)}</span></div>
      ${short && w.step === 3 ? `<button class="btn coral block" style="margin-top:12px" id="aside-topup">${icon('plus', 15, 2)}شارژ ${money(w.quote.total - w.balance)}</button>` : ''}</div>`;
    $('#aside-topup', main)?.addEventListener('click', () => topupWizard({ amount: w.quote.total - w.balance, onDone: requote }));
    const pay = $('#pay', main);
    if (pay) pay.disabled = !w.quote || short;
    const tot = $('#wiz-total', main);
    if (tot) tot.textContent = w.quote ? money(w.quote.total) : '';
  }

  function stepHTML() {
    const p = w.product, s = w.sel;
    if (w.step === 0) {
      return `<h2 class="step-title">چه چیزی لازم دارید؟</h2><p class="step-sub">یک محصول انتخاب کنید؛ در قدم بعد روش خرید را می‌بینید.</p>
        <div class="picks">${data.items.map((x) => `<button type="button" class="pick ${p?.id === x.id ? 'on' : ''}" data-prod="${x.id}"><span class="tick">${icon('check', 13, 2.6)}</span>
          <span class="icon-chip ${x.type === 'port' ? 'coral' : 'blue'}">${icon(x.type === 'port' ? 'port' : 'globe', 20)}</span><b>${esc(x.title)}</b>
          <span class="small ink2">${esc(x.description || (x.type === 'port' ? 'پورت روی IP ایران با تانل' : 'اوتباند برای Xray'))}</span>
          ${x.type === 'outbound' && x.locations?.length ? `<span class="small muted">${x.locations.map((l) => esc(l.flag)).join(' ')}</span>` : ''}</button>`).join('')}</div>`;
    }
    if (w.step === 1) {
      const o = p.options;
      const pk = o.models.includes('package') ? (o.packages || []) : [];
      const others = o.models.filter((m) => m !== 'package');
      const maxGB = Math.max(...pk.map((k) => k.gb || 0), 1);
      return `<h2 class="step-title">روش خرید</h2><p class="step-sub">بسته‌ی آماده سریع‌ترین راه است؛ اگر چیز خاصی لازم دارید «سفارشی» را بزنید.</p>
        ${pk.length ? `<div class="picks">${pk.map((k) => `<button type="button" class="pick ${s.model === 'package' && s.package_id === k.id ? 'on' : ''}" data-pkg="${esc(k.id)}"><span class="tick">${icon('check', 13, 2.6)}</span>
          <div class="row" style="gap:14px">${ring(k.gb ? (k.gb / maxGB) * 100 : 100, { size: 76, stroke: 8, big: k.gb ? n(k.gb) : '∞', unit: k.gb ? 'GB' : '', ticks: false, cls: s.package_id === k.id ? '' : 'calm' })}
          <div><b>${esc(k.name)}</b><div class="price">${money(k.price)}</div></div></div>
          <span class="small ink2">${p.type === 'port' ? `${n(k.ports)} پورت · ` : ''}${n(k.days)} روز${k.rate_mbit ? ` · ${n(k.rate_mbit)} مگابیت` : ''}</span></button>`).join('')}</div>` : ''}
        ${others.length ? `<div class="picks" style="margin-top:12px">${others.map((m) => `<button type="button" class="pick ${s.model === m ? 'on' : ''}" data-model="${m}"><span class="tick">${icon('check', 13, 2.6)}</span>
          <span class="icon-chip">${icon(modelInfo[m][2], 20)}</span><b>${modelInfo[m][0]}</b><span class="small ink2">${modelInfo[m][1]}</span></button>`).join('')}</div>` : ''}`;
    }
    if (w.step === 2) return configHTML();
    // review
    const sel = selection();
    const lines = [];
    if (s.model === 'package') { const k = p.options.packages.find((x) => x.id === s.package_id); lines.push(['بسته', k?.name || '']); }
    else lines.push(['روش', modelInfo[s.model][0]]);
    if (p.type === 'port') {
      lines.push(['پورت‌ها', (sel.ports || []).map((x) => x.port || 'تصادفی').join('، ')]);
      if (s.delivery) lines.push(['مقصد', deliveryFa[s.delivery]?.[0] || s.delivery]);
      if (s.core) lines.push(['تانل', coreFa[s.core] || s.core]);
    } else lines.push(['لوکیشن‌ها', (p.locations || []).filter((l) => s.locations.some((x) => x.location_id === l.id)).map((l) => `${l.flag} ${l.name}`).join('، ')]);
    if (s.model !== 'package' && !['per_gb', 'payg'].includes(s.model)) lines.push(['مدت', `${n(s.days)} روز`]);
    return `<h2 class="step-title">بازبینی و پرداخت</h2><p class="step-sub">همه‌چیز درست است؟ مبلغ از کیف پول کم می‌شود و سرویس خودکار ساخته می‌شود.</p>
      <dl class="kv" style="margin-bottom:18px">${lines.map(([k, v]) => `<dt>${k}</dt><dd>${esc(v)}</dd>`).join('')}</dl>
      <div class="card flat" style="background:var(--sunk);box-shadow:none">${receiptHTML(true)}</div>
      <label class="field" style="margin-top:16px">کد تخفیف<div class="row"><input id="coupon" value="${esc(s.coupon)}" dir="ltr" style="flex:1" autocomplete="off"><button type="button" class="btn white" id="apply">اعمال</button></div></label>`;
  }

  function configHTML() {
    const p = w.product, o = p.options, s = w.sel;
    const rows = [];
    if (p.type === 'port') {
      if ((o.deliveries?.length || 0) > 1) rows.push(`<div class="optrow"><div class="t"><b>مقصد پورت‌ها</b><small>ترافیک به کجا برود؟</small></div><div class="picks" style="flex:1;min-width:260px;grid-template-columns:1fr 1fr">${o.deliveries.map((d) => `<button type="button" class="pick ${s.delivery === d ? 'on' : ''}" data-set="delivery" data-v="${d}"><span class="tick">${icon('check', 13, 2.6)}</span><b style="font-size:.95rem">${deliveryFa[d]?.[0] || d}</b><span class="small ink2">${deliveryFa[d]?.[1] || ''}</span></button>`).join('')}</div></div>`);
      if (s.delivery === 'dedicated') rows.push(`<div class="optrow"><div class="t"><b>IP سرور خارج شما</b><small>بعد از خرید یک دستور نصب برای همین سرور می‌گیرید.</small></div><input data-field="peer_address" value="${esc(s.peer_address || '')}" dir="ltr" placeholder="203.0.113.10" style="max-width:260px"></div>`);
      if (s.model !== 'package') {
        rows.push(`<div class="optrow"><div class="t"><b>پورت‌ها</b><small>خالی = پورت آزاد تصادفی. «مقصد» پورت روی سرور خارج است.</small></div>${counter('portcount', s.ports.length, { min: Math.max(1, o.min_ports || 1), max: o.max_ports || 0, label: 'تعداد پورت' })}</div>
          <div class="form" style="padding:4px 0 10px">${s.ports.map((x, i) => `<div class="port-row"><label class="field">پورت ${n(i + 1)} روی سرور ایران<input data-port="${i}" value="${esc(x.port)}" inputmode="numeric" dir="ltr" placeholder="تصادفی"></label>
            <label class="field">پورت مقصد<input data-to="${i}" value="${x.to_port || ''}" inputmode="numeric" dir="ltr" placeholder="همان پورت"></label><span></span></div>`).join('')}</div>`);
      }
      if ((o.cores?.length || 0) > 1) rows.push(`<div class="optrow"><div class="t"><b>نوع تانل</b></div><div class="seg">${o.cores.map((c) => `<button type="button" class="${s.core === c ? 'on' : ''}" data-set="core" data-v="${c}">${coreFa[c] || c}</button>`).join('')}</div></div>`);
      if ((o.protos?.length || 0) > 1) rows.push(`<div class="optrow"><div class="t"><b>پروتکل</b></div><div class="seg">${o.protos.map((c) => `<button type="button" class="${s.proto === c ? 'on' : ''}" data-set="proto" data-v="${c}">${protoFa[c] || c}</button>`).join('')}</div></div>`);
    }
    if (s.model !== 'package') {
      if (p.type === 'outbound') {
        rows.push(`<div class="optrow" style="display:block"><div class="t" style="margin-bottom:10px"><b>لوکیشن‌ها</b><small>هر لوکیشن ساب و حجم جدای خودش را دارد.</small></div><div class="tiles">${(p.locations || []).map((l) => {
          const cur = s.locations.find((x) => x.location_id === l.id);
          return `<div class="pick ${cur ? 'on' : ''}" style="cursor:default"><button type="button" class="tick" data-loc="${l.id}" aria-pressed="${!!cur}" aria-label="${esc(l.name)}" style="border:0;cursor:pointer">${icon('check', 13, 2.6)}</button>
            <span style="font-size:1.6rem">${esc(l.flag || '🌐')}</span><b style="font-size:.95rem">${esc(l.name)}</b>
            ${cur && ['builder', 'per_gb'].includes(s.model) ? `<div data-locgb="${l.id}">${counter('locgb' + l.id, cur.gb, { min: o.min_gb || 1, max: o.max_gb || 0, step: o.gb_step || 10, label: 'حجم' })}</div><small class="muted">گیگ</small>` : cur ? '' : `<button type="button" class="btn small white" data-loc="${l.id}">افزودن</button>`}</div>`;
        }).join('') || '<p class="muted">لوکیشنی تعریف نشده است.</p>'}</div></div>`);
      }
      if (!['per_gb', 'payg'].includes(s.model)) {
        rows.push(o.durations?.length
          ? `<div class="optrow"><div class="t"><b>مدت</b></div><div class="seg">${o.durations.map((d) => `<button type="button" class="${+s.days === d ? 'on' : ''}" data-set="days" data-v="${d}">${n(d)} روز</button>`).join('')}</div></div>`
          : `<div class="optrow"><div class="t"><b>مدت (روز)</b></div>${counter('days', s.days, { min: o.min_days || 1, max: o.max_days || 0, label: 'مدت' })}</div>`);
      }
      if (p.type === 'port' && ['builder', 'per_gb'].includes(s.model)) rows.push(`<div class="optrow"><div class="t"><b>حجم (گیگ)</b>${o.gb_step > 1 ? `<small>گام ${n(o.gb_step)} گیگ</small>` : ''}</div>${counter('gb', s.gb, { min: o.min_gb || 1, max: o.max_gb || 0, step: o.gb_step || 10, label: 'حجم' })}</div>`);
      if (s.model === 'unlimited' || o.max_rate) rows.push(`<div class="optrow"><div class="t"><b>سقف سرعت (مگابیت)</b></div>${counter('rate_mbit', s.rate_mbit || o.min_rate || 10, { min: o.min_rate || 1, max: o.max_rate || 0, step: 5, label: 'سرعت' })}</div>`);
      if (o.max_ips) rows.push(`<div class="optrow"><div class="t"><b>کاربر هم‌زمان (IP)</b><small>${n(o.included_ips || 0)} IP رایگان</small></div>${counter('max_ips', s.max_ips || o.included_ips || 1, { min: 1, max: o.max_ips, label: 'IP' })}</div>`);
    }
    return `<h2 class="step-title">تنظیمات</h2><p class="step-sub">${s.model === 'package' ? 'فقط چند انتخاب کوچک باقی مانده است.' : 'مقادیر را تنظیم کنید؛ قیمت کنار صفحه همان لحظه به‌روز می‌شود.'}</p>
      <div>${rows.join('') || '<p class="ink2">تنظیم دیگری لازم نیست؛ به قدم بعد بروید.</p>'}</div>`;
  }

  function canNext() {
    if (w.step === 0) return !!w.product;
    if (w.step === 2 && w.product.type === 'outbound' && w.sel.model !== 'package') return w.sel.locations.length > 0 && !w.err;
    if (w.step === 2 && w.sel.delivery === 'dedicated') return /^\d{1,3}(\.\d{1,3}){3}$|:/.test(w.sel.peer_address || '');
    if (w.step >= 1) return !w.err;
    return true;
  }

  function draw() {
    main.innerHTML = `<div class="page-head"><div><a class="back" href="/">${icon('chevR', 16)} نمای کلی</a><h1>خرید سرویس</h1></div></div>
      <div class="wizard"><section class="card">${steps(WIZ, w.step)}<div id="wiz-body">${stepHTML()}</div>
        <div class="wiz-nav">${w.step > 0 ? `<button type="button" class="btn ghost" id="back">${icon('chevR', 18)} قبلی</button>` : '<span></span>'}
        <span class="wiz-total" id="wiz-total"></span>${w.step < 3 ? `<button type="button" class="btn primary" id="next" ${canNext() ? '' : 'disabled'}>ادامه ${icon('chevL', 18)}</button>` : `<button type="button" class="btn coral" id="pay" disabled>پرداخت از کیف پول</button>`}</div></section>
      <aside id="wiz-aside"></aside></div>`;
    drawAside();
  }

  function refresh() { const b = $('#wiz-body', main); if (b) b.innerHTML = stepHTML(); const nx = $('#next', main); if (nx) nx.disabled = !canNext(); }

  main.addEventListener('click', async (e) => {
    const t = e.target.closest('button');
    if (!t) return;
    if (t.dataset.prod) { w.product = data.items.find((x) => x.id === +t.dataset.prod); startProduct(); w.step = 0; refresh(); requote(); return; }
    if (t.dataset.pkg) { w.sel.model = 'package'; w.sel.package_id = t.dataset.pkg; refresh(); requote(); return; }
    if (t.dataset.model) { w.sel.model = t.dataset.model; refresh(); requote(); return; }
    if (t.dataset.set) { w.sel[t.dataset.set] = t.dataset.set === 'days' ? +t.dataset.v : t.dataset.v; refresh(); requote(); return; }
    if (t.dataset.loc) {
      const id = +t.dataset.loc;
      const cur = w.sel.locations.find((x) => x.location_id === id);
      if (cur) w.sel.locations = w.sel.locations.filter((x) => x !== cur); else w.sel.locations.push({ location_id: id, gb: w.product.options.min_gb || 10 });
      refresh(); requote(); return;
    }
    if (t.id === 'next' && canNext()) { w.step++; draw(); requote(); return; }
    if (t.id === 'back') { w.step--; draw(); requote(); return; }
    if (t.id === 'apply') { w.sel.coupon = $('#coupon', main).value.trim(); requote(); setTimeout(refresh, 400); return; }
    if (t.id === 'pay') {
      busy(t, async () => {
        const o = await post('/api/orders', { kind: 'new', product_id: w.product.id, selection: selection(), idempotency_key: w.idem });
        refreshBalance();
        main.innerHTML = `<div class="card success view-enter"><span class="ok">${icon('check', 34, 2.4)}</span><h1>سرویس شما آماده شد</h1>
          <p class="ink2">مبلغ ${money(o.amount)} از کیف پول کم شد. جزئیات اتصال در صفحه‌ی سرویس است.</p>
          <div class="row" style="justify-content:center"><a class="btn coral" href="/services/${o.service_id}">مشاهده‌ی سرویس</a><a class="btn white" href="/">نمای کلی</a></div></div>`;
      });
    }
  });
  main.addEventListener('input', (e) => {
    const t = e.target, s = w.sel;
    if (t.dataset.port !== undefined) s.ports[+t.dataset.port].port = t.value;
    else if (t.dataset.to !== undefined) s.ports[+t.dataset.to].to_port = num(t.value);
    else if (t.dataset.field) s[t.dataset.field] = t.value.trim();
    else if (t.name === 'portcount') {
      const c = Math.max(1, num(t.value));
      while (s.ports.length < c) s.ports.push({ port: '', to_port: 0 });
      s.ports.length = c;
      refresh();
    } else if (t.name?.startsWith('locgb')) { const l = s.locations.find((x) => x.location_id === +t.name.slice(5)); if (l) l.gb = num(t.value); }
    else if (['gb', 'days', 'rate_mbit', 'max_ips'].includes(t.name)) s[t.name] = num(t.value);
    else return;
    const nx = $('#next', main); if (nx) nx.disabled = !canNext();
    requote();
  });
  draw();
  requote();
}
route(/^\/shop$/, (main) => shopWizard(main, []));
route(/^\/shop\/(\d+)$/, shopWizard);

// ---------- service detail (thermostat) ----------
route(/^\/services\/(\d+)$/, async (main, [id]) => {
  const [s, all] = await Promise.all([get(`/api/services/${id}`), get('/api/services')]);
  const live = all.items.filter((x) => !['deleted', 'failed'].includes(x.status));
  const idx = live.findIndex((x) => x.id === s.id);
  const prev = live[idx - 1], next = live[idx + 1];
  const { used, total } = svcTotals(s);
  const left = daysLeft(s.expires_at);
  const liveSvc = !['deleted', 'failed'].includes(s.status);
  let mode = total ? 'gb' : 'days';
  const totalDays = s.spec?.days || 30;

  const dialHTML = () => {
    if (mode === 'gb' && total) return dial({ pct: 100 - usagePct(used, total), big: n2(Math.max(total - used, 0)), small: `گیگ مانده از ${bytes(total)}`, calm: s.status !== 'active' });
    return dial({ pct: left === null ? 100 : Math.max(0, (left / totalDays) * 100), big: left === null ? '∞' : n(Math.max(left, 0)), small: left === null ? 'بدون انقضا' : `روز مانده · ${date(s.expires_at)}`, calm: s.status !== 'active' });
  };
  function n2(b) { return new Intl.NumberFormat('fa-IR', { maximumFractionDigits: 1 }).format(b / gbBase()); }

  const side = s.type === 'port' ? `
    <div class="summary-dark"><div><b style="font-size:1.1rem;font-weight:500">${n((s.ports || []).length)} پورت</b><div class="small" style="opacity:.75">${esc(s.node_name || '')} ${s.core ? `· ${coreFa[s.core] || s.core}` : ''}</div></div>${ring(usagePct(used, total), { size: 72, stroke: 9, ticks: false, label: '' })}</div>
    <div class="tiles" style="margin-top:12px">${(s.ports || []).map((p) => socket(p, s.status)).join('')}</div>
    ${s.tunnel ? `<div class="tile" style="margin-top:12px;display:grid;gap:8px"><div class="spread"><b>سرور خارج شما</b>${status(s.tunnel.peer_status || 'waiting')}</div>
      ${s.tunnel.install_cmd ? `<span class="small ink2">این دستور را با root روی سرور خارج اجرا کنید:</span>${copyField(s.tunnel.install_cmd)}` : ''}</div>` : ''}`
    : `<div class="summary-dark"><div><b style="font-size:1.1rem;font-weight:500">${n((s.locations || []).length)} لوکیشن</b><div class="small" style="opacity:.75">${bytes(used)} مصرف</div></div>${ring(usagePct(used, total), { size: 72, stroke: 9, ticks: false })}</div>
    <div class="stack" style="margin-top:12px">${(s.locations || []).map((l) => `<div class="tile" style="display:grid;gap:10px"><div class="spread"><b>${esc(l.flag)} ${esc(l.name)}</b>${status(l.status)}</div>
      <div class="row" style="gap:14px;flex-wrap:nowrap">${ring(usagePct(l.used_up + l.used_down, l.quota_bytes), { size: 64, stroke: 9, ticks: false, cls: l.status === 'active' ? '' : 'calm' })}<div class="small ink2">${bytes(l.used_up + l.used_down)} از ${l.quota_bytes ? bytes(l.quota_bytes) : 'نامحدود'}</div></div>
      <div class="row"><button class="btn small white" data-copy="${esc(l.sub_url)}">${icon('copy', 14)}ساب</button><button class="btn small white" data-copy="${esc(l.sub_url)}?format=json">${icon('copy', 14)}JSON اوتباند</button></div></div>`).join('')}
      <div class="tile" style="display:grid;gap:8px"><b>ساب همه‌ی لوکیشن‌ها</b>${copyField(s.sub_url)}<button class="btn small ghost" id="rotate" style="justify-self:start">ساخت لینک تازه</button></div></div>`;

  main.innerHTML = `
    <div class="page-head"><div><a class="back" href="/">${icon('chevR', 16)} سرویس‌های من</a><h1>${esc(s.title)} <span class="muted">#${n(s.id)}</span></h1>
      <div class="row" style="margin-top:6px">${status(s.status)}${s.status_reason ? `<span class="muted small">${esc(s.status_reason)}</span>` : ''}</div></div>
      ${liveSvc && !['waiting_peer', 'provisioning'].includes(s.status) && s.model !== 'payg' ? `<div class="row"><button class="btn white" id="addon">${icon('plus', 16, 2)}افزودنی</button><button class="btn primary" id="renew">تمدید</button></div>` : ''}</div>
    <div class="split">
      <section class="card"><div class="spread"><div class="row"><span class="icon-chip">${icon(s.type === 'port' ? 'port' : 'globe', 20)}</span><div><b>${s.type === 'port' ? 'پورت تانل' : 'اوتباند'}</b><div class="small muted">${status(s.status)}</div></div></div>
        ${total ? `<div class="seg" role="tablist"><button type="button" data-mode="gb" class="${mode === 'gb' ? 'on' : ''}">حجم</button><button type="button" data-mode="days" class="${mode === 'days' ? 'on' : ''}">زمان</button></div>` : ''}</div>
        <div class="dial-wrap" style="margin-top:12px">${next ? `<a class="dial-btn" href="/services/${next.id}" aria-label="سرویس بعدی">${icon('chevR', 20)}</a>` : '<span></span>'}<div id="dial">${dialHTML()}</div>${prev ? `<a class="dial-btn" href="/services/${prev.id}" aria-label="سرویس قبلی">${icon('chevL', 20)}</a>` : '<span></span>'}</div>
        <div class="dial-meta"><span>${icon('bolt', 15)} ${s.rate_mbit ? `${n(s.rate_mbit)} مگابیت` : 'سرعت آزاد'}</span><span>${icon('user', 15)} ${s.max_ips ? `${n(s.max_ips)} IP هم‌زمان` : 'بدون محدودیت IP'}</span><span>${icon('chart', 15)} ${bytes(used)} مصرف</span></div>
        <div style="margin-top:24px"><div class="spread"><h2>الگوی مصرف</h2><div class="seg"><button type="button" class="on" data-range="24h">روزانه</button><button type="button" data-range="30d">ماهانه</button></div></div><div id="chart" style="margin-top:8px"></div></div></section>
      <aside>${side}</aside></div>`;

  async function loadChart(range) {
    const r = await get(`/api/services/${id}/usage?range=${range}`);
    $('#chart', main).innerHTML = areaChart(r.points, { daily: range === '30d' });
    $$('[data-range]', main).forEach((b) => b.classList.toggle('on', b.dataset.range === range));
  }
  loadChart('24h').catch(fail);
  main.addEventListener('click', async (e) => {
    const r = e.target.closest('[data-range]');
    if (r) loadChart(r.dataset.range).catch(fail);
    const m = e.target.closest('[data-mode]');
    if (m) { mode = m.dataset.mode; $('#dial', main).innerHTML = dialHTML(); $$('[data-mode]', main).forEach((b) => b.classList.toggle('on', b === m)); }
    if (e.target.closest('#rotate')) {
      if (!confirm('لینک‌های قبلی ساب از کار می‌افتند. ادامه می‌دهید؟')) return;
      await busy(e.target, async () => { await post(`/api/services/${id}/rotate-sub`); toast('لینک تازه ساخته شد'); go(location.pathname, true); });
    }
    if (e.target.closest('#renew')) orderSheet({ kind: 'renew', service_id: s.id }, 'تمدید سرویس', `${n(s.spec?.days || 30)} روز دیگر با همان مشخصات.`);
    if (e.target.closest('#addon')) addonSheet(s);
  });
});

// Confirm-and-pay sheet for renewals and add-ons (with inline top-up).
async function orderSheet(req, title, sub = '') {
  const m = modal(`<h2>${esc(title)}</h2>${sub ? `<p class="ink2" style="margin-top:4px">${esc(sub)}</p>` : ''}<div id="mq" style="margin:18px 0"><p class="muted">در حال محاسبه…</p></div>
    <div class="row end" id="mact"><button class="btn ghost" data-close>انصراف</button></div>`);
  const idem = crypto.randomUUID();
  const load = async () => {
    try {
      const r = await post('/api/orders/quote', req);
      const q = r.quote, short = r.balance < q.total;
      $('#mq', m.el).innerHTML = `<div class="receipt">${q.lines.map((l) => `<div class="quote-line"><span>${esc(l.label)}</span><span>${money(l.amount)}</span></div>`).join('')}
        ${q.adjustments.map((l) => `<div class="quote-line adj"><span>${esc(l.label)}</span><span>${money(l.amount)}</span></div>`).join('')}
        <div class="quote-total"><span class="ink2">مبلغ نهایی</span><b>${money(q.total)}</b></div>
        <div class="spread small"><span class="muted">موجودی</span><span style="color:${short ? 'var(--fault)' : 'inherit'}">${money(r.balance)}</span></div></div>`;
      $('#mact', m.el).innerHTML = `<button class="btn ghost" data-close>انصراف</button>${short
        ? `<button class="btn coral" id="mtop">${icon('plus', 15, 2)}شارژ ${money(q.total - r.balance)}</button>`
        : '<button class="btn coral" id="mok">پرداخت از کیف پول</button>'}`;
      $('#mtop', m.el)?.addEventListener('click', () => topupWizard({ amount: q.total - r.balance, onDone: load }));
      $('#mok', m.el)?.addEventListener('click', (e) => busy(e.target, async () => {
        await post('/api/orders', { ...req, idempotency_key: idem });
        refreshBalance();
        $('#mq', m.el).innerHTML = `<div class="success"><span class="ok">${icon('check', 30, 2.4)}</span><h2>انجام شد</h2></div>`;
        $('#mact', m.el).innerHTML = '';
        setTimeout(() => { m.close(); go(location.pathname, true); }, 900);
      }));
    } catch (e) { $('#mq', m.el).innerHTML = `<p class="err">${esc(e.message)}</p>`; }
  };
  load();
}

function addonSheet(s) {
  const kinds = s.type === 'port'
    ? [['gb', 'حجم اضافه', 'گیگ', 'chart'], ['days', 'روز اضافه', 'روز', 'bolt'], ['port', 'پورت اضافه', 'پورت', 'port'], ['ip', 'IP هم‌زمان', 'IP', 'user'], ['rate', 'سرعت اضافه', 'مگابیت', 'bolt']]
    : [['gb', 'حجم برای یک لوکیشن', 'گیگ', 'chart'], ['days', 'روز اضافه', 'روز', 'bolt'], ['ip', 'IP هم‌زمان', 'IP', 'user']];
  let kind = kinds[0][0];
  const m = modal(`<h2>افزودنی</h2><p class="ink2" style="margin-top:4px">به سرویس فعلی اضافه می‌شود؛ مدت و مشخصات دیگر تغییر نمی‌کند.</p>
    <div class="picks" style="margin:16px 0;grid-template-columns:repeat(auto-fill,minmax(150px,1fr))">${kinds.map(([k, l, , ic], i) => `<button type="button" class="pick ${i === 0 ? 'on' : ''}" data-k="${k}"><span class="tick">${icon('check', 13, 2.6)}</span><span class="icon-chip">${icon(ic, 18)}</span><b style="font-size:.95rem">${l}</b></button>`).join('')}</div>
    <div class="optrow"><div class="t"><b>مقدار</b><small id="unit">${kinds[0][2]}</small></div>${counter('qty', 10, { min: 1, label: 'مقدار' })}</div>
    ${s.type === 'outbound' ? `<div class="optrow"><div class="t"><b>لوکیشن</b></div><select id="loc" style="max-width:220px">${s.locations.map((l) => `<option value="${l.location_id}">${esc(l.flag)} ${esc(l.name)}</option>`).join('')}</select></div>` : ''}
    <div class="row end" style="margin-top:16px"><button class="btn ghost" data-close>انصراف</button><button class="btn primary" id="go">محاسبه‌ی قیمت ${icon('chevL', 16)}</button></div>`);
  m.el.addEventListener('click', (e) => {
    const p = e.target.closest('[data-k]');
    if (p) { kind = p.dataset.k; $$('[data-k]', m.el).forEach((x) => x.classList.toggle('on', x === p)); $('#unit', m.el).textContent = kinds.find((k) => k[0] === kind)[2]; }
  });
  $('#go', m.el).onclick = () => {
    const sel = { addon_kind: kind, addon_qty: num(m.el.querySelector('[name=qty]').value) };
    const loc = $('#loc', m.el);
    if (loc) sel.locations = [{ location_id: +loc.value }];
    m.close();
    orderSheet({ kind: 'addon', service_id: s.id, selection: sel }, kinds.find((k) => k[0] === kind)[1], `${n(sel.addon_qty)} ${kinds.find((k) => k[0] === kind)[2]}`);
  };
}

// ---------- top-up wizard ----------
export async function topupWizard({ amount = 0, onDone } = {}) {
  const st = state.boot.settings;
  if (!st['payments.card.enabled']) { toast('شارژ کارت‌به‌کارت فعال نیست؛ با پشتیبانی تماس بگیرید', true); return; }
  const min = st['payments.min_topup'] || 0;
  const pays = await get('/api/wallet/payments');
  let pay = pays.items.find((p) => p.status === 'pending') || null;
  let step = pay ? 1 : 0, file = null;
  const need = amount > 0 ? Math.max(Math.ceil(amount / 1000) * 1000, min) : 0;
  const presets = [...new Set([need, 100000, 200000, 500000, 1000000].filter((x) => x && x >= min))].slice(0, 5);
  const pre = need || (presets.includes(200000) ? 200000 : presets[0]);
  const m = modal('<div id="tw"></div>');
  const draw = () => {
    const body = $('#tw', m.el);
    const head = `<h2>شارژ کیف پول</h2>${steps(['مبلغ', 'واریز', 'رسید'], step)}`;
    if (step === 0) {
      body.innerHTML = `${head}<p class="ink2" style="margin-bottom:12px">چه مبلغی شارژ می‌کنید؟</p>
        <div class="amounts">${presets.map((p) => `<button type="button" class="pick ${p === pre ? 'on' : ''}" data-amt="${p}"><span class="tick">${icon('check', 13, 2.6)}</span><b class="num">${n(p)}</b><span class="small muted">تومان</span></button>`).join('')}</div>
        <label class="field" style="margin-top:14px">مبلغ دلخواه<input id="amt" inputmode="numeric" value="${pre || ''}" placeholder="حداقل ${n(min)}"></label>
        <p class="err" id="te"></p><div class="row end" style="margin-top:12px"><button class="btn ghost" data-close>انصراف</button><button class="btn primary" id="t-next">دریافت شماره کارت ${icon('chevL', 16)}</button></div>`;
      body.addEventListener('click', (e) => { const b = e.target.closest('[data-amt]'); if (b) { $$('[data-amt]', body).forEach((x) => x.classList.toggle('on', x === b)); $('#amt', body).value = b.dataset.amt; } });
      $('#t-next', body).onclick = (e) => busy(e.target, async () => {
        try { pay = await post('/api/wallet/topup', { amount: num($('#amt', body).value) }); step = 1; draw(); }
        catch (err) { $('#te', body).textContent = err.message; }
      });
    } else if (step === 1) {
      body.innerHTML = `${head}<p class="ink2">دقیقاً این مبلغ را به کارت زیر واریز کنید. رقم‌های آخر مبلغ، واریز شما را شناسایی می‌کند.</p>
        <div class="figure" style="margin:16px 0;text-align:center"><b style="font-size:2.2rem">${money(pay.pay_amount)}</b><button class="btn small ghost" data-copy="${pay.pay_amount}" style="justify-self:center">${icon('copy', 14)}کپی مبلغ</button></div>
        <div class="cardnum">${esc((pay.card?.number || '').replace(/(\d{4})(?=\d)/g, '$1 '))}</div>
        <div class="spread small" style="margin-top:8px"><span class="ink2">${esc(pay.card?.holder || '')} · ${esc(pay.card?.bank || '')}</span><button class="btn small white" data-copy="${esc(pay.card?.number || '')}">${icon('copy', 14)}کپی شماره کارت</button></div>
        <p class="small muted" style="margin-top:10px">مهلت واریز تا ${datetime(pay.expires_at)}</p>
        <div class="row end" style="margin-top:14px"><button class="btn ghost" data-close>بعداً</button><button class="btn primary" id="t-next">واریز کردم ${icon('chevL', 16)}</button></div>`;
      $('#t-next', body).onclick = () => { step = 2; draw(); };
    } else if (step === 2) {
      body.innerHTML = `${head}<p class="ink2" style="margin-bottom:12px">عکس رسید یا اسکرین‌شات پیامک بانک را بفرستید.</p>
        <label class="dropzone" id="dz">${file ? `<img src="${URL.createObjectURL(file)}" alt="پیش‌نمایش رسید">` : `${icon('upload', 28)}<b>انتخاب یا رها کردن فایل</b><span class="small muted">عکس یا PDF تا ۵ مگابایت</span>`}
          <input type="file" accept="image/*,application/pdf" class="sr" id="rf"></label>
        <label class="field" style="margin-top:12px">توضیح (اختیاری)<input id="rnote"></label><p class="err" id="te"></p>
        <div class="row end" style="margin-top:12px"><button class="btn ghost" id="t-back">${icon('chevR', 16)} قبلی</button><button class="btn coral" id="t-send" ${file ? '' : 'disabled'}>ارسال رسید</button></div>`;
      const dz = $('#dz', body);
      const take = (f) => { if (f) { file = f; draw(); } };
      $('#rf', body).onchange = (e) => take(e.target.files[0]);
      dz.addEventListener('dragover', (e) => { e.preventDefault(); dz.classList.add('drag'); });
      dz.addEventListener('dragleave', () => dz.classList.remove('drag'));
      dz.addEventListener('drop', (e) => { e.preventDefault(); take(e.dataTransfer.files[0]); });
      $('#t-back', body).onclick = () => { step = 1; draw(); };
      $('#t-send', body).onclick = (e) => busy(e.target, async () => {
        const fd = new FormData();
        fd.append('file', file);
        fd.append('note', $('#rnote', body).value);
        try {
          await api('POST', `/api/wallet/payments/${pay.id}/receipt`, undefined, { form: fd });
          step = 3; draw();
        } catch (err) { $('#te', body).textContent = err.message; }
      });
    } else {
      body.innerHTML = `<div class="success"><span class="ok">${icon('check', 30, 2.4)}</span><h2>رسید ثبت شد</h2>
        <p class="ink2">پس از بررسی، ${money(pay.pay_amount)} به کیف پول اضافه می‌شود و اعلان می‌گیرید.</p><button class="btn primary" data-close>باشه</button></div>`;
      onDone?.();
    }
  };
  draw();
}

// ---------- wallet (income-tracker layout) ----------
route(/^\/wallet$/, async (main) => {
  const [w, pays] = await Promise.all([get('/api/wallet'), get('/api/wallet/payments')]);
  const days = Array.from({ length: 7 }, (_, i) => { const d = new Date(); d.setHours(0, 0, 0, 0); d.setDate(d.getDate() - 6 + i); return d; });
  const wd = new Intl.DateTimeFormat('fa-IR', { weekday: 'narrow' });
  const spent = days.map((d) => w.transactions.filter((t) => t.amount < 0 && new Date(t.created_at * 1000).toDateString() === d.toDateString()).reduce((a, t) => a - t.amount, 0));
  const pts = days.map((d, i) => ({ label: wd.format(d), value: spent[i], tip: money(spent[i]) }));
  const weekSpent = spent.reduce((a, b) => a + b, 0);
  const cnt = (s) => pays.items.filter((p) => p.status === s).length;
  const txIcon = (t) => (t.amount > 0 ? ['plus', 'blue'] : t.type === 'purchase' ? ['cart', 'coral'] : ['receipt', '']);

  main.innerHTML = `<div class="split">
    <section class="card"><div class="card-head"><div class="row"><span class="icon-chip">${icon('wallet', 20)}</span><h1>کیف پول</h1></div><button class="btn white" id="topup">${icon('plus', 16, 2)}شارژ</button></div>
      <p class="ink2">موجودی، خرج‌های هفته و وضعیت شارژها.</p>
      <div style="display:grid;grid-template-columns:minmax(150px,auto) 1fr;gap:18px;align-items:end;margin-top:12px" class="wallet-grid">
        <div><div class="big-delta num" style="white-space:nowrap">${n(w.balance)}</div><p class="small ink2" style="margin-top:8px">تومان موجودی<br>${n(Math.round(weekSpent))} تومان خرج در ۷ روز اخیر</p></div>
        <div id="spend">${lollipop(pts)}</div></div></section>
    <section><div class="spread" style="margin-bottom:8px"><h2 style="font-size:1.3rem;font-weight:400">تراکنش‌های اخیر</h2></div>
      <div class="list">${w.transactions.slice(0, 7).map((t) => { const [ic, c] = txIcon(t); return `<div class="list-item"><span class="icon-chip ${c}">${icon(ic, 18)}</span><div class="grow"><b>${esc(t.note || t.type)}</b><div class="small muted">${datetime(t.created_at)}</div></div><span class="num" style="color:${t.amount < 0 ? 'var(--ink)' : 'var(--green)'}">${t.amount > 0 ? '+' : ''}${money(t.amount)}</span></div>`; }).join('') || '<p class="muted">تراکنشی ثبت نشده است.</p>'}</div></section></div>
    <div class="split" style="margin-top:16px">
      <section class="card"><div class="card-head"><h2>درخواست‌های شارژ</h2></div>${segprog([{ label: 'منتظر واریز', value: cnt('pending'), of: Math.max(pays.items.length, 1) }, { label: 'منتظر بررسی', value: cnt('submitted'), of: Math.max(pays.items.length, 1) }, { label: 'تأییدشده', value: cnt('approved'), of: Math.max(pays.items.length, 1) }])}
        <div class="list" style="margin-top:16px">${pays.items.slice(0, 6).map((p) => `<div class="list-item"><span class="icon-chip">${icon('card', 18)}</span><div class="grow"><b class="num">${money(p.pay_amount)}</b><div class="small muted">${datetime(p.created_at)}${p.admin_note ? ` · ${esc(p.admin_note)}` : ''}</div></div>${status(p.status)}
          ${p.status === 'pending' || p.status === 'submitted' ? `<button class="btn small white" data-resume="${p.id}">${p.status === 'pending' ? 'ادامه' : 'رسید دیگر'}</button>` : ''}</div>`).join('') || '<p class="muted">هنوز درخواستی ندارید.</p>'}</div></section>
      <section class="card flat dotted" style="background-color:var(--glass);display:grid;gap:12px;align-content:space-between;min-height:220px">
        <div><h2 style="font-size:1.4rem;font-weight:400">شارژ سریع</h2><p class="ink2" style="margin-top:6px">مبلغ را انتخاب کنید، کارت‌به‌کارت واریز کنید و عکس رسید را بفرستید. همین.</p></div>
        <button class="btn white block" id="topup2" style="justify-content:space-between">شروع شارژ ${icon('chevL', 18)}</button></section></div>`;
  bindLollipop($('#spend', main), pts);
  const reload = () => go('/wallet', true);
  main.addEventListener('click', (e) => {
    if (e.target.closest('#topup, #topup2, [data-resume]')) topupWizard({ onDone: reload });
  });
  if (new URLSearchParams(location.search).get('topup')) topupWizard({ onDone: reload });
});

// ---------- tickets ----------
route(/^\/tickets$/, async (main) => {
  const t = await get('/api/tickets');
  main.innerHTML = `<div class="page-head"><div><h1>پشتیبانی</h1>${state.boot.settings['site.support'] ? `<p>راه تماس مستقیم: ${esc(state.boot.settings['site.support'])}</p>` : '<p>سؤال یا مشکلی دارید؟ تیکت بفرستید؛ پاسخ را اینجا و در تلگرام می‌گیرید.</p>'}</div></div>
    <div class="split"><section class="card"><div class="card-head"><h2>تیکت‌های من</h2></div>${t.items.length ? `<div class="list">${t.items.map((x) => `<a class="list-item" href="/tickets/${x.id}"><span class="icon-chip">${icon('ticket', 18)}</span><div class="grow"><b>${esc(x.subject)}</b><div class="small muted">${datetime(x.updated_at)}</div></div>${status(x.status)}</a>`).join('')}</div>` : empty('تیکتی ندارید.', '', 'chat')}</section>
    <section class="card"><h2 style="margin-bottom:14px">تیکت تازه</h2><form class="form" id="nt"><label class="field">موضوع<input name="subject" required></label><label class="field">پیام<textarea name="body" required></textarea></label><button class="btn primary">ارسال تیکت</button></form></section></div>`;
  $('#nt', main).onsubmit = (e) => { e.preventDefault(); busy(e.submitter, async () => { const r = await post('/api/tickets', { subject: e.target.subject.value, body: e.target.body.value }); go(`/tickets/${r.id}`); }); };
});

export function ticketView(base) {
  return async (main, [id]) => {
    const r = await get(`${base}/${id}`);
    const staff = base.includes('admin');
    main.innerHTML = `<div class="page-head"><div><a class="back" href="${staff ? '/admin/tickets' : '/tickets'}">${icon('chevR', 16)} همه‌ی تیکت‌ها</a><h1>${esc(r.ticket.subject)}</h1><div class="row" style="margin-top:6px">${status(r.ticket.status)}${staff ? `<span class="muted ltr">${esc(r.ticket.user_email)}</span>` : ''}</div></div>
      ${staff && r.ticket.status !== 'closed' ? '<button class="btn white" id="close">بستن تیکت</button>' : ''}</div>
      <section class="card"><div class="msgs">${r.messages.map((m) => `<div class="msg ${m.is_staff ? 'staff' : ''}">${esc(m.body)}<div class="small muted" style="margin-top:4px">${m.is_staff ? 'پشتیبانی · ' : ''}${datetime(m.created_at)}</div></div>`).join('')}</div>
      <form class="form" id="rep" style="margin-top:18px"><textarea name="body" required placeholder="پاسخ خود را بنویسید"></textarea><div class="row end"><button class="btn primary">ارسال پاسخ</button></div></form></section>`;
    $('#rep', main).onsubmit = (e) => { e.preventDefault(); busy(e.submitter, async () => { await post(`${base}/${id}/reply`, { body: e.target.body.value }); go(location.pathname, true); }); };
    $('#close', main)?.addEventListener('click', async () => { await post(`${base}/${id}/close`); go(location.pathname, true); });
  };
}
route(/^\/tickets\/(\d+)$/, ticketView('/api/tickets'));

// ---------- account ----------
route(/^\/account$/, async (main) => {
  const notes = await get('/api/notifications');
  const u = state.user;
  main.innerHTML = `<div class="page-head"><div><h1>حساب کاربری</h1></div></div>
    <div class="grid three">
      <section class="card"><div class="row" style="margin-bottom:14px"><span class="avatar">${esc((u.name || u.email || '?').slice(0, 1))}</span><div><b>${esc(u.name || '—')}</b><div class="small muted ltr">${esc(u.email || '')}</div></div></div><dl class="kv"><dt>عضویت</dt><dd>${date(u.created_at)}</dd></dl></section>
      <section class="card"><div class="row" style="margin-bottom:10px"><span class="icon-chip blue">${icon('chat', 20)}</span><h2>تلگرام</h2></div>${u.telegram_chat_id
        ? `<p class="ink2">وصل است ${u.telegram_username ? `<span class="ltr">@${esc(u.telegram_username)}</span>` : ''}؛ اعلان‌ها آنجا هم می‌آیند.</p><button class="btn danger small" id="tgoff" style="margin-top:12px">قطع اتصال</button>`
        : '<p class="ink2">مصرف و اعلان‌ها را در ربات ببینید و از همان‌جا خرید و شارژ کنید.</p><button class="btn primary" id="tglink" style="margin-top:12px">اتصال به تلگرام</button>'}</section>
      ${u.email ? `<section class="card"><h2 style="margin-bottom:12px">تغییر رمز</h2><form class="form" id="pw"><input type="password" name="current" required autocomplete="current-password" placeholder="رمز فعلی" aria-label="رمز فعلی">
        <input type="password" name="new" required minlength="8" autocomplete="new-password" placeholder="رمز تازه (حداقل ۸ نویسه)" aria-label="رمز تازه"><button class="btn white">ذخیره‌ی رمز</button></form></section>` : ''}
    </div>
    <section class="card" id="notes" style="margin-top:16px"><div class="card-head"><h2>اعلان‌ها</h2>${notes.unread ? '<button class="btn small white" id="readall">همه خوانده شد</button>' : ''}</div>
      ${notes.items.length ? `<div class="list">${notes.items.map((x) => `<div class="list-item"><span class="icon-chip ${x.read ? '' : 'coral'}">${icon('bell', 18)}</span><div class="grow"><b>${esc(x.title)}</b><p class="small ink2">${esc(x.body)}</p></div><span class="small muted">${datetime(x.created_at)}</span></div>`).join('')}</div>` : empty('اعلانی ندارید.', '', 'bell')}</section>`;
  $('#tglink', main)?.addEventListener('click', (e) => busy(e.target, async () => { const r = await post('/api/me/telegram-link'); window.open(r.url, '_blank', 'noopener'); }));
  $('#tgoff', main)?.addEventListener('click', (e) => busy(e.target, async () => { await api('DELETE', '/api/me/telegram'); state.user.telegram_chat_id = 0; go('/account', true); }));
  $('#readall', main)?.addEventListener('click', async () => { await post('/api/notifications/read'); state.unread = 0; go('/account', true); });
  $('#pw', main)?.addEventListener('submit', (e) => { e.preventDefault(); busy(e.submitter, async () => { await post('/api/me/password', { current: e.target.current.value, new: e.target.new.value }); e.target.reset(); toast('رمز تغییر کرد'); }); });
});

export { meter };

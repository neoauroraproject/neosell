import {
  state, esc, $, $$, get, post, put, del, api, n, money, bytes, date, datetime, status, meter, copyField, empty, toast, fail,
  modal, busy, num, go, route, fieldHTML, readFields, getPath, confirmBox, gbBase, statusFa, icon, lollipop, bindLollipop, segprog,
} from './core.js';
import { ticketView } from './customer.js';

const can = (p) => state.user?.is_owner || state.user?.permissions?.includes(p);

// ---------- generic resource screen ----------
// cfg: {path, title, intro, cols: [[label, item => html]], fields: (meta) => [...], meta: async () => ({}),
//       defaults, newLabel, rowActions: (item) => html, onRowAction(name, item, reload), beforeSave(values, item)}
function resource(cfg) {
  return async (main) => {
    const meta = cfg.meta ? await cfg.meta() : {};
    const { items } = await get(cfg.path);
    const fields = cfg.fields(meta);
    main.innerHTML = `<div class="page-head"><div><h1>${esc(cfg.title)}</h1>${cfg.intro ? `<p>${cfg.intro}</p>` : ''}</div>
      ${cfg.noCreate ? '' : `<button class="btn primary" id="new">${icon('plus', 16, 2)}${esc(cfg.newLabel || 'افزودن')}</button>`}</div>
      ${cfg.top ? cfg.top(meta, items) : ''}
      <section class="panel">${items.length ? `<div class="table-wrap"><table><thead><tr>${cfg.cols.map(([l]) => `<th>${esc(l)}</th>`).join('')}<th></th></tr></thead><tbody>
      ${items.map((it, i) => `<tr class="click" data-i="${i}">${cfg.cols.map(([, f]) => `<td>${f(it, meta)}</td>`).join('')}<td><div class="row end">${cfg.rowActions ? cfg.rowActions(it, meta) : ''}</div></td></tr>`).join('')}
      </tbody></table></div>` : empty(cfg.empty || 'هنوز چیزی تعریف نشده است.')}</section>`;
    const open = (item) => {
      const isNew = !item;
      const vals = item || structuredClone(cfg.defaults || {});
      const m = modal(`<h2>${isNew ? esc(cfg.newLabel || 'افزودن') : 'ویرایش'}</h2>
        <form class="form" style="margin-top:12px" novalidate>${fields.map((f) => (f.section ? `<h3 style="margin-top:6px">${esc(f.section)}</h3>${f.help ? `<p class="small muted">${f.help}</p>` : ''}` : fieldHTML(f, getPath(vals, f.key)))).join('')}
        <p class="err" id="ferr"></p>
        <div class="spread"><div>${!isNew && !cfg.noDelete ? '<button type="button" class="btn danger" id="del">حذف</button>' : ''}</div>
        <div class="row"><button type="button" class="btn ghost" data-close>انصراف</button><button class="btn primary">ذخیره</button></div></div></form>`, { wide: cfg.wide });
      const form = m.el.querySelector('form');
      form.onsubmit = (e) => {
        e.preventDefault();
        busy(e.submitter, async () => {
          try {
            let body = readFields(form, fields.filter((f) => !f.section));
            if (cfg.beforeSave) body = cfg.beforeSave(body, item, meta);
            const base = cfg.base || cfg.path;
            if (isNew) await post(base, body); else await put(`${base}/${item.id}`, body);
            m.close();
            toast('ذخیره شد');
            go(location.pathname, true);
          } catch (err) { $('#ferr', m.el).textContent = err.message; }
        });
      };
      $('#del', m.el)?.addEventListener('click', async (e) => {
        if (!(await confirmBox('حذف شود؟ این کار برگشت ندارد.', 'حذف'))) return;
        busy(e.target, async () => { await del(`${cfg.base || cfg.path}/${item.id}`); m.close(); toast('حذف شد'); go(location.pathname, true); });
      });
    };
    $('#new', main)?.addEventListener('click', () => open(null));
    main.querySelector('tbody')?.addEventListener('click', (e) => {
      const act = e.target.closest('[data-act]');
      const tr = e.target.closest('tr[data-i]');
      if (!tr) return;
      const item = items[+tr.dataset.i];
      if (act) { e.stopPropagation(); cfg.onRowAction?.(act.dataset.act, item, act); return; }
      open(item);
    });
  };
}

const yes = (b) => (b ? 'بله' : 'خیر');
const opt = (list, key = 'id', label = 'name') => list.map((x) => [x[key], x[label]]);
async function lists(...names) {
  const urls = { locations: '/api/admin/locations', groups: '/api/admin/groups', products: '/api/admin/products', cores: '/api/admin/cores', classes: '/api/admin/port-classes' };
  const out = {};
  await Promise.all(names.map(async (k) => {
    if (k === 'tunnelNodes' || k === 'xuiNodes') out[k] = (await get(`/api/admin/nodes?kind=${k === 'tunnelNodes' ? 'tunnel' : 'xui'}`)).items;
    else out[k] = (await get(urls[k])).items;
  }));
  return out;
}

// ---------- dashboard (income-tracker layout) ----------
route(/^\/admin$/, async (main) => {
  const [s, orders, nodes] = await Promise.all([
    get('/api/admin/stats'),
    can('orders.view') ? get('/api/admin/orders') : Promise.resolve({ items: [] }),
    can('nodes.manage') ? get('/api/admin/nodes') : Promise.resolve({ items: [] }),
  ]);
  const byDay = new Map((s.sales_by_day || []).map((d) => [new Date(d.day * 1000).toDateString(), d.amount]));
  const day = (off) => { const d = new Date(); d.setHours(0, 0, 0, 0); d.setDate(d.getDate() - off); return d; };
  const week = Array.from({ length: 7 }, (_, i) => day(6 - i));
  const prev = Array.from({ length: 7 }, (_, i) => day(13 - i));
  const sum = (ds) => ds.reduce((a, d) => a + (byDay.get(d.toDateString()) || 0), 0);
  const thisWeek = sum(week), lastWeek = sum(prev);
  const delta = lastWeek ? Math.round(((thisWeek - lastWeek) / lastWeek) * 100) : (thisWeek ? 100 : 0);
  const wd = new Intl.DateTimeFormat('fa-IR', { weekday: 'narrow' });
  const pts = week.map((d) => ({ label: wd.format(d), value: byDay.get(d.toDateString()) || 0, tip: money(byDay.get(d.toDateString()) || 0) }));
  const svc = s.services || {};
  const liveTotal = Object.entries(svc).filter(([k]) => !['failed'].includes(k)).reduce((a, [, v]) => a + v, 0) || 1;
  const kindFa = { new: 'خرید', renew: 'تمدید', addon: 'افزودنی' };

  main.innerHTML = `<div class="split">
    <section class="card"><div class="card-head"><div class="row"><span class="icon-chip">${icon('chart', 20)}</span><h1>درآمد</h1></div><span class="chip soft">۷ روز اخیر</span></div>
      <p class="ink2">تغییر فروش نسبت به هفته‌ی قبل و فروش هر روز.</p>
      <div class="wallet-grid" style="display:grid;grid-template-columns:minmax(150px,auto) 1fr;gap:18px;align-items:end;margin-top:12px">
        <div><div class="big-delta num" dir="ltr" style="text-align:right">${delta >= 0 ? '+' : ''}${n(delta)}٪</div><p class="small ink2" style="margin-top:8px">فروش این هفته ${money(thisWeek)}<br>امروز ${money(s.sales_today)} · این ماه ${money(s.sales_month)}</p></div>
        <div id="sales">${lollipop(pts)}</div></div></section>
    <section><div class="spread" style="margin-bottom:8px"><h2 style="font-size:1.3rem;font-weight:400">سفارش‌های اخیر</h2><a class="small ink2" href="/admin/orders">همه‌ی سفارش‌ها</a></div>
      <div class="list">${orders.items.slice(0, 6).map((o) => `<a class="list-item" href="/admin/services/${o.service_id}"><span class="icon-chip ${o.kind === 'new' ? 'coral' : o.kind === 'renew' ? 'navy' : 'blue'}">${icon(o.kind === 'new' ? 'cart' : o.kind === 'renew' ? 'bolt' : 'plus', 18)}</span>
        <div class="grow"><b>${kindFa[o.kind]} · <span class="num">${money(o.amount)}</span></b><div class="small muted ltr">${esc(o.user_email)}</div></div>${status(o.status)}</a>`).join('') || '<p class="muted">هنوز سفارشی ثبت نشده است.</p>'}</div></section></div>
  <div class="grid three" style="margin-top:16px;grid-template-columns:repeat(auto-fit,minmax(260px,1fr))">
    <section class="card flat dotted" style="background-color:var(--glass);display:grid;gap:12px;align-content:space-between">
      <div><h2 style="font-size:1.4rem;font-weight:400">${n(s.pending_receipts)} رسید منتظر</h2><p class="ink2" style="margin-top:6px">رسیدهای کارت‌به‌کارت را تطبیق دهید و کیف پول‌ها را شارژ کنید.</p></div>
      <a class="btn white block" href="/admin/payments" style="justify-content:space-between">بررسی رسیدها ${icon('chevL', 18)}</a></section>
    <section class="card"><div class="card-head"><h2>سرویس‌ها</h2><span class="muted small">${n(s.users)} کاربر · ${n(s.users_today)} امروز</span></div>
      ${segprog([{ label: 'فعال', value: svc.active || 0, of: liveTotal }, { label: 'نیاز به تمدید', value: (svc.limited || 0) + (svc.expired || 0), of: liveTotal }, { label: 'منتظر اتصال', value: (svc.waiting_peer || 0) + (svc.provisioning || 0), of: liveTotal }])}</section>
    <section class="card"><div class="card-head"><h2>سرورها</h2><span class="chip soft">${n(s.nodes_online)} از ${n(s.nodes_total)} آنلاین</span></div>
      <div class="list">${nodes.items.slice(0, 4).map((x) => `<a class="list-item" href="${x.kind === 'xui' ? '/admin/xui' : '/admin/nodes'}"><span class="icon-chip">${icon(x.kind === 'xui' ? 'globe' : 'server', 18)}</span><div class="grow"><b>${esc(x.name)}</b><div class="small muted">${x.kind === 'xui' ? 'پنل ثنایی' : `${n(x.live_services)} سرویس`}</div></div>${status(x.status)}</a>`).join('') || '<p class="muted small">سروری اضافه نشده است.</p>'}</div>
      <p class="small muted" style="margin-top:8px">ترافیک امروز: ${bytes(s.traffic_today)} · ${n(s.open_tickets)} تیکت باز</p></section></div>`;
  bindLollipop($('#sales', main), pts);
});

// ---------- license ----------
route(/^\/admin\/license$/, async (main) => {
  const l = await get('/api/admin/license');
  const owner = state.user.is_owner;
  const modFa = { ports: 'فروش پورت', outbound: 'فروش اوتباند', bot: 'ربات تلگرام', gateways: 'درگاه آنلاین', payg: 'پرداخت به‌اندازه‌ی مصرف', reseller: 'نمایندگی', multi_admin: 'چند ادمین', white_label: 'حذف برند' };
  const limFa = { max_nodes: 'سرور تانل', max_xui_panels: 'پنل ثنایی', max_active_services: 'سرویس فعال', max_admins: 'ادمین' };
  main.innerHTML = `<div class="page-head"><div><h1>لایسنس</h1><p>لایسنس با سرور لایسنس بررسی می‌شود؛ اگر نامعتبر شود فروش متوقف می‌شود ولی سرویس‌های فعلی مشتری‌ها قطع نمی‌شوند.</p></div></div>
    <div class="grid two"><section class="panel stack"><div class="spread"><h2>وضعیت</h2>${status(l.state === 'active' ? 'active' : l.state === 'locked' ? 'suspended' : 'pending')}</div>
      ${l.warning ? `<div class="banner warn">${esc(l.warning)}</div>` : ''}${l.reason && l.state !== 'active' ? `<div class="banner bad">${esc(l.reason)}</div>` : ''}
      <dl class="kv"><dt>کلید</dt><dd class="ltr">${esc(l.key_hint || '—')}</dd><dt>سطح</dt><dd>${esc(l.tier || '—')}</dd>
      <dt>انقضای لایسنس</dt><dd>${l.license_expires_at ? date(l.license_expires_at) : 'بدون انقضا'}</dd><dt>آخرین بررسی</dt><dd>${datetime(l.last_check)}</dd>
      <dt>شناسه‌ی دستگاه</dt><dd><code>${esc(l.fingerprint.slice(0, 16))}…</code></dd></dl>
      ${owner ? `<form class="row" id="act"><input name="key" placeholder="HM-XXXX-XXXX-XXXX" dir="ltr" style="flex:1" required><button class="btn primary">فعال‌سازی</button></form>
      <div class="row"><button class="btn" id="recheck">بررسی دوباره</button>${l.state !== 'unactivated' ? '<button class="btn danger" id="deact">حذف لایسنس از این سرور</button>' : ''}</div>` : ''}</section>
    <section class="panel stack"><h2>امکانات لایسنس</h2><div class="chips">${Object.entries(modFa).map(([k, v]) => `<span class="chip" style="${l.modules.includes(k) ? '' : 'opacity:.45;text-decoration:line-through'}">${v}</span>`).join('')}</div>
      <dl class="kv">${Object.entries(limFa).map(([k, v]) => `<dt>${v}</dt><dd>${l.limits[k] === -1 ? 'نامحدود' : n(l.limits[k] || 0)}</dd>`).join('')}</dl></section></div>`;
  const after = (r) => { toast(r?.state === 'active' ? 'لایسنس فعال است' : 'انجام شد'); location.reload(); };
  $('#act', main)?.addEventListener('submit', (e) => { e.preventDefault(); busy(e.submitter, async () => after(await post('/api/admin/license/activate', { key: e.target.key.value }))); });
  $('#recheck', main)?.addEventListener('click', (e) => busy(e.target, async () => after(await post('/api/admin/license/recheck'))));
  $('#deact', main)?.addEventListener('click', async (e) => {
    if (!(await confirmBox('لایسنس از این سرور برداشته شود؟ فروش تا فعال‌سازی دوباره متوقف می‌شود.', 'حذف لایسنس'))) return;
    busy(e.target, async () => after(await post('/api/admin/license/deactivate')));
  });
});

// ---------- servers ----------
const nodeStatus = (x) => `${status(x.status)}${x.status_detail ? `<div class="small muted" style="max-width:340px">${esc(x.status_detail)}</div>` : ''}`;

route(/^\/admin\/nodes$/, resource({
  path: '/api/admin/nodes?kind=tunnel', base: '/api/admin/nodes', title: 'سرورهای تانل (ایران)', newLabel: 'افزودن سرور',
  intro: 'روی هر سرور ایران Neo Agent نصب می‌شود؛ ساخت پورت، شمارش مصرف و اعمال حجم، زمان، سرعت و تعداد IP همه توسط Neo Sell انجام می‌شود.',
  meta: () => lists('locations', 'cores'),
  defaults: { kind: 'tunnel', enabled: true, sale_enabled: true, options: { port_ranges: '20000-60000', bridge_range: '40000-45000', reserved_ports: '22,80,443', weight: 1 } },
  cols: [['نام', (x) => `<b>${esc(x.name)}</b><div class="small muted ltr">${esc(x.public_host)}</div>`], ['وضعیت', nodeStatus],
    ['سرویس', (x) => n(x.live_services)], ['فروش', (x) => yes(x.sale_enabled)], ['آخرین ارتباط', (x) => datetime(x.last_seen_at)]],
  rowActions: () => '<button class="btn small" data-act="agent">دستور نصب Agent</button>',
  async onRowAction(act, x, btn) {
    if (!(await confirmBox('توکن تازه ساخته می‌شود و Agent قبلی (اگر نصب است) دیگر وصل نمی‌شود تا با دستور تازه نصب شود. ادامه؟', 'ساخت دستور'))) return;
    busy(btn, async () => {
      const r = await post(`/api/admin/nodes/${x.id}/agent-token`);
      modal(`<h2>نصب Neo Agent روی ${esc(x.name)}</h2><p style="margin:8px 0">این دستور را با root روی سرور ایران اجرا کنید:</p>${copyField(r.command)}
        <p class="small muted" style="margin-top:8px">Agent خودش به پنل وصل می‌شود؛ روی سرور ایران پورت ورودی لازم نیست.</p><div class="row end" style="margin-top:12px"><button class="btn" data-close>بستن</button></div>`);
    });
  },
  beforeSave: (b) => ({ ...b, kind: 'tunnel' }),
  fields: (m) => [
    { key: 'name', label: 'نام', required: true }, { key: 'public_host', label: 'IP یا دامنه‌ی عمومی (برای مشتری‌ها)', ltr: true },
    { key: 'location_id', label: 'لوکیشن', type: 'select', numeric: true, options: [[0, '—'], ...opt(m.locations)] },
    { key: 'enabled', label: 'فعال', type: 'bool' }, { key: 'sale_enabled', label: 'فروش تازه روی این سرور', type: 'bool' },
    { section: 'پورت‌ها' },
    { key: 'options.port_ranges', label: 'بازه‌ی پورت‌های تصادفی', ltr: true, help: 'مثلاً 20000-60000' },
    { key: 'options.reserved_ports', label: 'پورت‌های رزرو (فروخته نمی‌شوند)', ltr: true, help: 'مثلاً 22,80,443,2087' },
    { key: 'options.bridge_range', label: 'بازه‌ی پورت اتصال تانل‌های اختصاصی', ltr: true },
    { key: 'options.capacity', label: 'حداکثر سرویس (۰ = نامحدود)', type: 'number' }, { key: 'options.weight', label: 'اولویت برای سرویس تازه', type: 'number' },
    { key: 'options.cores', label: 'هسته‌های نصب‌شده', type: 'multi', options: opt(m.cores, 'core', 'title'), help: 'هیچ‌کدام = همه' },
    { section: 'NeoTunnel روی این سرور', help: 'Agent از API محلی NeoTunnel فقط برای ساخت تانل و فوروارد استفاده می‌کند.' },
    { key: 'secrets.nt_base_url', label: 'آدرس پنل NeoTunnel (محلی)', ltr: true, help: 'مثلاً https://127.0.0.1:2096/مسیر-مخفی' },
    { key: 'secrets.nt_token', label: 'توکن API (دسترسی full)', type: 'password', help: 'خالی = بدون تغییر' },
    { key: 'options.nt_mode', label: 'نوع تانل برای مشتری‌های اختصاصی', ltr: true, help: 'مثلاً tcp' },
  ],
}));

route(/^\/admin\/xui$/, resource({
  path: '/api/admin/nodes?kind=xui', base: '/api/admin/nodes', title: 'پنل‌های ثنایی (3x-ui)', newLabel: 'افزودن پنل',
  intro: 'برای هر سرور خارج یک پنل ثنایی اضافه کنید، بعد اینباندهایش را به‌عنوان اوتباند قابل فروش ثبت کنید.',
  meta: () => lists('locations'), defaults: { kind: 'xui', enabled: true, sale_enabled: true },
  cols: [['نام', (x) => `<b>${esc(x.name)}</b><div class="small muted ltr">${esc(x.xui_base_url || '')}</div>`], ['وضعیت', nodeStatus], ['آخرین بررسی', (x) => datetime(x.last_seen_at)]],
  rowActions: () => '<button class="btn small" data-act="inb">اینباندها</button>',
  onRowAction: (act, x, btn) => busy(btn, async () => {
    const r = await get(`/api/admin/nodes/${x.id}/inbounds`);
    const m = modal(`<h2>اینباندهای ${esc(x.name)}</h2><div class="table-wrap" style="margin-top:10px"><table><thead><tr><th>شناسه</th><th>نام</th><th>پروتکل</th><th>پورت</th><th>کلاینت</th></tr></thead>
      <tbody>${r.items.map((i) => `<tr><td>${i.id}</td><td>${esc(i.remark)}</td><td class="ltr">${esc(i.protocol)}</td><td>${i.port}</td><td>${n(i.clients)}</td></tr>`).join('')}</tbody></table></div>
      <p class="small muted" style="margin-top:8px">شناسه‌ی اینباند را در «اوتباندهای قابل فروش» وارد کنید.</p><div class="row end"><a class="btn primary" href="/admin/outbounds" data-close>رفتن به اوتباندها</a></div>`, { wide: true });
    m.el.querySelector('a').addEventListener('click', () => m.close());
  }),
  beforeSave: (b) => ({ ...b, kind: 'xui' }),
  fields: (m) => [
    { key: 'name', label: 'نام', required: true }, { key: 'location_id', label: 'لوکیشن', type: 'select', numeric: true, options: [[0, '—'], ...opt(m.locations)] },
    { key: 'public_host', label: 'آدرس عمومی پیش‌فرض اوتباندها', ltr: true },
    { key: 'secrets.xui_base_url', label: 'آدرس پنل با مسیر', ltr: true, help: 'مثلاً https://de.example.com:2053/panel-path' },
    { key: 'secrets.xui_username', label: 'نام کاربری', ltr: true }, { key: 'secrets.xui_password', label: 'رمز', type: 'password', help: 'خالی = بدون تغییر' },
    { key: 'options.insecure', label: 'گواهی پنل خودامضاست', type: 'bool' }, { key: 'enabled', label: 'فعال', type: 'bool' },
  ],
}));

route(/^\/admin\/tunnels$/, resource({
  path: '/api/admin/tunnels', title: 'تانل‌های اشتراکی', newLabel: 'افزودن تانل',
  intro: 'تانل بین سرور ایران و سرور خارجِ خودتان. پورت‌هایی که «روی سرور خارج ما» فروخته می‌شوند از این تانل عبور می‌کنند.',
  meta: () => lists('tunnelNodes', 'cores'),
  cols: [['نام', (x) => `<b class="ltr">${esc(x.name)}</b>`], ['سرور', (x, m) => esc(m.tunnelNodes.find((y) => y.id === x.node_id)?.name || x.node_id)], ['هسته', (x) => esc(x.core)],
    ['سمت خارج', (x) => (x.peer_token && !x.nt_tunnel ? `<button class="btn small" data-copy="${esc(x.peer_token)}">کپی دستور نصب</button>` : `<span class="small muted ltr">${esc(x.nt_tunnel)}</span>`)]],
  fields: (m) => [
    { key: 'node_id', label: 'سرور ایران', type: 'select', numeric: true, options: opt(m.tunnelNodes) },
    { key: 'core', label: 'هسته', type: 'select', options: opt(m.cores, 'core', 'title') },
    { key: 'name', label: 'نام (لاتین)', ltr: true, required: true },
    { key: 'nt_tunnel', label: 'NeoTunnel: نام تانل موجود', ltr: true, help: 'مثلاً t1 (فقط برای NeoTunnel)' },
    { key: 'bridge_port', label: 'Kariz/Backpack: پورت اتصال تانل', type: 'number' },
  ],
}));

route(/^\/admin\/outbounds$/, resource({
  path: '/api/admin/outbounds', title: 'اوتباندهای قابل فروش', newLabel: 'ثبت اینباند',
  intro: 'هر ردیف یک اینباند پنل ثنایی است. اینباندهای یک لوکیشن زیر یک ساب و یک حجم به مشتری داده می‌شوند.',
  meta: () => lists('xuiNodes', 'locations'), defaults: { enabled: true },
  cols: [['لوکیشن', (x, m) => { const l = m.locations.find((y) => y.id === x.location_id); return l ? `${esc(l.flag)} ${esc(l.name)}` : '—'; }],
    ['اینباند', (x) => `<span class="ltr">${esc(x.protocol)} ${esc(x.network)} ${esc(x.security)}</span><div class="small muted">${esc(x.label)}</div>`],
    ['آدرس', (x) => `<span class="ltr small">${esc(x.public_host)}:${x.public_port}</span>`], ['فعال', (x) => yes(x.enabled)]],
  fields: (m) => [
    { key: 'node_id', label: 'پنل ثنایی', type: 'select', numeric: true, options: opt(m.xuiNodes) },
    { key: 'inbound_id', label: 'شناسه‌ی اینباند', type: 'number', help: 'از دکمه‌ی «اینباندها» در صفحه‌ی پنل‌ها' },
    { key: 'location_id', label: 'لوکیشن', type: 'select', numeric: true, options: opt(m.locations) },
    { key: 'label', label: 'نام نمایشی' }, { key: 'public_host', label: 'آدرس عمومی (خالی = آدرس پنل)', ltr: true },
    { key: 'public_port', label: 'پورت عمومی (۰ = پورت اینباند)', type: 'number' }, { key: 'tag', label: 'تگ اوتباند', ltr: true },
    { key: 'flow', label: 'Flow (VLESS)', type: 'select', options: [['', 'هیچ'], ['xtls-rprx-vision', 'xtls-rprx-vision']] },
    { key: 'enabled', label: 'فعال', type: 'bool' },
  ],
}));

route(/^\/admin\/cores$/, resource({
  path: '/api/admin/cores', title: 'هسته‌های تانل', newLabel: 'افزودن هسته', noDelete: true, wide: true,
  intro: 'NeoTunnel از API خودش کنترل می‌شود. برای Kariz، Backpack یا هر هسته‌ی دیگر، قالب کانفیگ سمت ایران و خارج را اینجا بنویسید.',
  defaults: { driver: 'template', enabled: true, config_ext: 'toml' },
  cols: [['هسته', (x) => `<b>${esc(x.title)}</b> <span class="small muted ltr">${esc(x.core)}</span>`], ['روش', (x) => (x.driver === 'neotunnel' ? 'API نئوتانل' : 'قالب کانفیگ')], ['فعال', (x) => yes(x.enabled)]],
  fields: () => [
    { key: 'core', label: 'شناسه (لاتین)', ltr: true, required: true }, { key: 'title', label: 'نام' },
    { key: 'driver', label: 'روش کنترل', type: 'select', options: [['template', 'قالب کانفیگ'], ['neotunnel', 'API نئوتانل']] },
    { key: 'binary_url', label: 'آدرس دانلود باینری (https، {arch} = amd64/arm64)', ltr: true }, { key: 'binary_sha256', label: 'SHA256 باینری (اختیاری)', ltr: true },
    { key: 'config_ext', label: 'پسوند فایل کانفیگ', ltr: true },
    { section: 'متغیرهای قالب', help: '{{.Tunnel.Name}} {{.Tunnel.BridgePort}} {{.Tunnel.Secret}} {{.Node.PublicHost}} {{.PortList}} و {{range .Ports}}{{.Port}} {{.ToPort}} {{.Proto}}{{end}}. در دستور اجرا {bin} و {config} جایگزین می‌شوند.' },
    { key: 'server_config', label: 'کانفیگ سمت ایران', type: 'code' }, { key: 'server_exec', label: 'دستور اجرای سمت ایران', ltr: true, help: 'مثلاً {bin} -c {config}' },
    { key: 'client_config', label: 'کانفیگ سمت خارج', type: 'code' }, { key: 'client_exec', label: 'دستور اجرای سمت خارج', ltr: true },
    { key: 'enabled', label: 'فعال', type: 'bool' },
  ],
}));

// ---------- catalog ----------
route(/^\/admin\/locations$/, resource({
  path: '/api/admin/locations', title: 'لوکیشن‌ها', newLabel: 'افزودن لوکیشن',
  cols: [['لوکیشن', (x) => `${esc(x.flag)} ${esc(x.name)}`], ['ترتیب', (x) => n(x.sort)]],
  fields: () => [{ key: 'name', label: 'نام', required: true }, { key: 'flag', label: 'پرچم (ایموجی)' }, { key: 'sort', label: 'ترتیب', type: 'number' }],
}));

const modelOpts = [['package', 'بسته‌ی آماده'], ['builder', 'انتخاب دلخواه'], ['per_port', 'به‌ازای پورت'], ['per_gb', 'فقط حجم'], ['unlimited', 'نامحدود با سقف سرعت'], ['payg', 'پرداخت به‌اندازه‌ی مصرف']];

route(/^\/admin\/products$/, resource({
  path: '/api/admin/products', title: 'محصولات', newLabel: 'محصول تازه', wide: true,
  intro: 'هر محصول روش‌های فروش و حدود انتخاب مشتری را مشخص می‌کند؛ قیمت‌ها در «قوانین قیمت» تعیین می‌شوند.',
  meta: () => lists('groups', 'tunnelNodes', 'locations', 'cores'),
  defaults: { type: 'port', enabled: true, stock: -1, options: { models: ['builder'], durations: [30, 90], protos: ['tcp'], deliveries: ['shared'], packages: [] } },
  cols: [['محصول', (x) => `<b>${esc(x.title)}</b>`], ['نوع', (x) => (x.type === 'port' ? 'پورت' : 'اوتباند')], ['روش فروش', (x) => x.options.models.map((m) => modelOpts.find((o) => o[0] === m)?.[1]).join('، ')],
    ['فروخته', (x) => `${n(x.sold)}${x.stock >= 0 ? ` از ${n(x.stock)}` : ''}`], ['فعال', (x) => yes(x.enabled)]],
  fields: (m) => [
    { key: 'type', label: 'نوع', type: 'select', options: [['port', 'پورت تانل'], ['outbound', 'اوتباند (ثنایی)']] },
    { key: 'title', label: 'عنوان', required: true }, { key: 'description', label: 'توضیح برای مشتری', type: 'textarea' },
    { key: 'enabled', label: 'فعال', type: 'bool' }, { key: 'stock', label: 'موجودی (-۱ = نامحدود)', type: 'number' }, { key: 'sort', label: 'ترتیب', type: 'number' },
    { key: 'visible_groups', label: 'فقط برای این گروه‌ها', type: 'multi', numeric: true, options: opt(m.groups), help: 'هیچ‌کدام = همه' },
    { section: 'روش فروش و حدود' },
    { key: 'options.models', label: 'روش‌های فروش', type: 'multi', options: modelOpts },
    { key: 'options.durations', label: 'مدت‌های قابل انتخاب (روز)', type: 'nums', help: 'مثلاً 30, 90, 180 (خالی = بازه‌ی حداقل/حداکثر)' },
    { key: 'options.min_days', label: 'حداقل روز', type: 'number' }, { key: 'options.max_days', label: 'حداکثر روز', type: 'number' },
    { key: 'options.min_ports', label: 'حداقل پورت', type: 'number' }, { key: 'options.max_ports', label: 'حداکثر پورت', type: 'number' },
    { key: 'options.min_gb', label: 'حداقل گیگ', type: 'number' }, { key: 'options.max_gb', label: 'حداکثر گیگ', type: 'number' }, { key: 'options.gb_step', label: 'گام حجم (گیگ)', type: 'number' },
    { key: 'options.per_port_gb', label: 'حجم هر پورت در روش «به‌ازای پورت» (۰ = نامحدود)', type: 'number' },
    { key: 'options.per_gb_days', label: 'اعتبار روش «فقط حجم» (روز)', type: 'number' },
    { key: 'options.min_rate', label: 'حداقل سرعت (مگابیت)', type: 'number' }, { key: 'options.max_rate', label: 'حداکثر سرعت', type: 'number' },
    { key: 'options.included_ips', label: 'IP رایگان', type: 'number' }, { key: 'options.max_ips', label: 'حداکثر IP', type: 'number' },
    { key: 'options.payg_min_balance', label: 'حداقل موجودی برای پس‌پرداخت', type: 'number' },
    { section: 'پورت تانل' },
    { key: 'options.cores', label: 'هسته‌ها', type: 'multi', options: opt(m.cores, 'core', 'title') },
    { key: 'options.protos', label: 'پروتکل', type: 'multi', options: [['tcp', 'TCP'], ['udp', 'UDP'], ['both', 'هر دو']] },
    { key: 'options.deliveries', label: 'مقصد', type: 'multi', options: [['shared', 'سرور خارج ما'], ['dedicated', 'سرور خارج مشتری']] },
    { key: 'options.nodes', label: 'سرورهای ایران', type: 'multi', numeric: true, options: opt(m.tunnelNodes), help: 'هیچ‌کدام = همه' },
    { section: 'اوتباند' },
    { key: 'options.locations', label: 'لوکیشن‌ها', type: 'multi', numeric: true, options: m.locations.map((l) => [l.id, `${l.flag} ${l.name}`]) },
    { key: 'options.min_locations', label: 'حداقل لوکیشن', type: 'number' },
    { section: 'بسته‌ها', help: 'برای روش «بسته‌ی آماده». هر بسته: id، name، ports، gb (۰ = نامحدود)، days، rate_mbit، max_ips، price و برای اوتباند locations: [{"location_id":1,"gb":50}]' },
    { key: 'options.packages', label: 'بسته‌ها (JSON)', type: 'json', empty: [] },
  ],
}));

route(/^\/admin\/port-classes$/, resource({
  path: '/api/admin/port-classes', title: 'کلاس‌های پورت', newLabel: 'کلاس تازه',
  intro: 'هر پورت درخواستی در یک کلاس قرار می‌گیرد و قیمت هر کلاس جداست. پورتی که در هیچ کلاسی نباشد فروخته نمی‌شود. «تصادفی» یعنی وقتی مشتری پورت را خالی می‌گذارد.',
  cols: [['نام', (x) => esc(x.name)], ['نوع', (x) => ({ specific: 'پورت‌های مشخص', range: 'بازه', random: 'تصادفی' }[x.kind])], ['پورت‌ها', (x) => `<span class="ltr">${esc(x.expr)}</span>`]],
  fields: () => [{ key: 'name', label: 'نام', required: true },
    { key: 'kind', label: 'نوع', type: 'select', options: [['specific', 'پورت‌های مشخص'], ['range', 'بازه'], ['random', 'تصادفی']] },
    { key: 'expr', label: 'پورت‌ها', ltr: true, help: 'مشخص: 443,8443 — بازه: 20000-30000' }, { key: 'sort', label: 'ترتیب', type: 'number' }],
}));

route(/^\/admin\/rules$/, async (main) => {
  const [meta, l] = await Promise.all([get('/api/admin/pricing/meta'), lists('products', 'groups')]);
  const compFa = Object.fromEntries(meta.components.map((c) => [c.id, c.label]));
  return resource({
    path: '/api/admin/price-rules', title: 'قوانین قیمت', newLabel: 'قانون تازه', wide: true,
    intro: 'برای هر جزء قیمت، دقیق‌ترین قانون اعمال می‌شود: کاربر، بعد گروه، بعد محصول، بعد شرط‌های بیشتر، بعد اولویت. نتیجه را در «ماشین‌حساب قیمت» امتحان کنید.',
    defaults: { component: 'port', enabled: true, scope: {}, tiers: [] },
    cols: [['جزء', (x) => `<b>${esc(compFa[x.component] || x.component)}</b>${x.note ? `<div class="small muted">${esc(x.note)}</div>` : ''}`],
      ['محصول', (x) => esc(l.products.find((p) => p.id === x.product_id)?.title || 'همه')],
      ['شرط', (x) => `<span class="ltr small">${esc(Object.entries(x.scope || {}).map(([k, v]) => `${k}=${v}`).join(' '))}</span>${x.group_id ? `<div class="small">گروه: ${esc(l.groups.find((g) => g.id === x.group_id)?.name || x.group_id)}</div>` : ''}${x.user_id ? `<div class="small">کاربر #${n(x.user_id)}</div>` : ''}`],
      ['مقدار', (x) => (x.tiers?.length ? `<span class="small">${x.tiers.length} پله</span>` : n(x.value))], ['فعال', (x) => yes(x.enabled)]],
    fields: () => [
      { key: 'component', label: 'جزء قیمت', type: 'select', options: meta.components.map((c) => [c.id, c.label]) },
      { key: 'product_id', label: 'محصول', type: 'select', numeric: true, options: [[0, 'همه‌ی محصولات'], ...opt(l.products, 'id', 'title')] },
      { key: 'value', label: 'مقدار', type: 'number', help: 'قیمت هر واحد، یا درصد برای فروش ویژه، یا ضریب برای تمدید' },
      { key: 'tiers', label: 'پله‌ها (JSON)', type: 'json', empty: [], help: 'حجم پلکانی: [{"upto":100,"price":2000},{"price":1500}] — ضریب مدت: [{"days":90,"mult":2.7}]' },
      { key: 'scope', label: 'شرط (JSON)', type: 'json', empty: {}, help: `کلیدها: ${meta.scope_keys.join('، ')} — مثلاً {"class_id":"2"} یا {"location_id":"1","core":"kariz"}` },
      { key: 'group_id', label: 'فقط گروه', type: 'select', numeric: true, options: [[0, 'همه'], ...opt(l.groups)] },
      { key: 'user_id', label: 'فقط کاربر (شناسه)', type: 'number' }, { key: 'priority', label: 'اولویت', type: 'number' },
      { key: 'valid_from', label: 'از تاریخ', type: 'date' }, { key: 'valid_to', label: 'تا تاریخ', type: 'date' },
      { key: 'note', label: 'یادداشت (برای فروش ویژه نام کمپین)' }, { key: 'enabled', label: 'فعال', type: 'bool' },
    ],
    beforeSave: (b) => { b.scope = Object.fromEntries(Object.entries(b.scope || {}).map(([k, v]) => [k, String(v)])); return b; },
  })(main);
});

route(/^\/admin\/simulate$/, async (main) => {
  const l = await lists('products', 'groups');
  main.innerHTML = `<div class="page-head"><div><h1>ماشین‌حساب قیمت</h1><p>قیمتی که اینجا می‌بینید دقیقاً همان است که مشتری می‌پردازد (با گروه، قیمت اختصاصی، فروش ویژه و کد تخفیف).</p></div></div>
    <div class="buy-layout"><form class="panel form" id="sim">
      <label class="field">محصول<select name="product_id">${l.products.map((p) => `<option value="${p.id}">${esc(p.title)}</option>`).join('')}</select></label>
      <div class="form-grid"><label class="field">کاربر (شناسه، اختیاری)<input name="user_id" inputmode="numeric"></label>
      <label class="field">گروه<select name="group_id"><option value="0">—</option>${l.groups.map((g) => `<option value="${g.id}">${esc(g.name)}</option>`).join('')}</select></label></div>
      <label class="field">انتخاب مشتری (JSON)<textarea name="selection" class="code" rows="10">${esc(JSON.stringify({ model: 'builder', days: 30, gb: 50, ports: [{ port: '443' }, { port: '' }] }, null, 2))}</textarea>
      <small>کلیدها: model, package_id, days, gb, ports[{port,to_port}], locations[{location_id,gb}], rate_mbit, max_ips, core, proto, delivery, coupon، و برای افزودنی kind:"addon", addon_kind, addon_qty</small></label>
      <div><button class="btn primary">محاسبه</button></div></form><aside class="panel"><div id="out" class="quote"><p class="muted">نتیجه اینجا نمایش داده می‌شود.</p></div></aside></div>`;
  $('#sim', main).onsubmit = (e) => {
    e.preventDefault();
    const f = e.target;
    busy(e.submitter, async () => {
      let sel;
      try { sel = JSON.parse(f.selection.value); } catch { throw new Error('JSON انتخاب نامعتبر است'); }
      try {
        const r = await post('/api/admin/pricing/simulate', { product_id: +f.product_id.value, selection: sel, user_id: num(f.user_id.value), group_id: +f.group_id.value });
        const q = r.quote;
        $('#out', main).innerHTML = q.lines.map((x) => `<div class="quote-line"><span>${esc(x.label)}${x.rule_id ? ` <span class="small muted">(قانون ${x.rule_id})</span>` : ''}</span><span>${money(x.amount)}</span></div>`).join('')
          + q.adjustments.map((x) => `<div class="quote-line adj"><span>${esc(x.label)}</span><span>${money(x.amount)}</span></div>`).join('')
          + `<div class="quote-total"><span>مبلغ نهایی</span><span>${money(q.total)}</span></div><pre class="small code" style="white-space:pre-wrap">${esc(JSON.stringify(q.spec, null, 1))}</pre>`;
      } catch (err) { $('#out', main).innerHTML = `<p class="err">${esc(err.message)}</p>`; }
    });
  };
});

route(/^\/admin\/groups$/, resource({
  path: '/api/admin/groups', title: 'گروه‌های کاربری', newLabel: 'گروه تازه',
  intro: 'برای نماینده‌ها یا مشتری‌های ویژه: تخفیف درصدی روی همه‌چیز، یا قیمت جدا با «قوانین قیمت» مخصوص گروه.',
  cols: [['نام', (x) => esc(x.name)], ['تخفیف', (x) => `${n(x.discount_pct)}٪`], ['اعضا', (x) => n(x.members)]],
  fields: () => [{ key: 'name', label: 'نام', required: true }, { key: 'discount_pct', label: 'درصد تخفیف', type: 'number' }],
}));

route(/^\/admin\/discounts$/, async (main) => {
  const l = await lists('products');
  return resource({
    path: '/api/admin/discounts', title: 'کدهای تخفیف', newLabel: 'کد تازه', defaults: { kind: 'percent', enabled: true, per_user: 1 },
    cols: [['کد', (x) => `<b class="ltr">${esc(x.code)}</b>`], ['مقدار', (x) => (x.kind === 'percent' ? `${n(x.value)}٪` : money(x.value))], ['استفاده', (x) => `${n(x.used)}${x.max_uses ? ` از ${n(x.max_uses)}` : ''}`],
      ['اعتبار', (x) => (x.valid_to ? `تا ${date(x.valid_to)}` : 'بدون انقضا')], ['فعال', (x) => yes(x.enabled)]],
    fields: () => [{ key: 'code', label: 'کد', ltr: true, required: true }, { key: 'kind', label: 'نوع', type: 'select', options: [['percent', 'درصدی'], ['amount', 'مبلغ ثابت']] },
      { key: 'value', label: 'مقدار', type: 'number' }, { key: 'max_uses', label: 'سقف کل استفاده (۰ = نامحدود)', type: 'number' },
      { key: 'per_user', label: 'سقف استفاده‌ی هر کاربر (۰ = نامحدود)', type: 'number' }, { key: 'min_amount', label: 'حداقل مبلغ سفارش', type: 'number' },
      { key: 'product_ids', label: 'فقط این محصولات', type: 'multi', numeric: true, options: opt(l.products, 'id', 'title') },
      { key: 'first_order_only', label: 'فقط اولین خرید', type: 'bool' }, { key: 'valid_from', label: 'از', type: 'date' }, { key: 'valid_to', label: 'تا', type: 'date' },
      { key: 'enabled', label: 'فعال', type: 'bool' }],
  })(main);
});

route(/^\/admin\/cards$/, resource({
  path: '/api/admin/cards', title: 'کارت‌های واریز', newLabel: 'کارت تازه', defaults: { enabled: true },
  intro: 'برای شارژ کارت‌به‌کارت، کارت‌ها به نوبت به مشتری‌ها نشان داده می‌شوند.',
  cols: [['شماره', (x) => `<span class="ltr">${esc(x.number)}</span>`], ['صاحب حساب', (x) => esc(x.holder)], ['بانک', (x) => esc(x.bank)], ['فعال', (x) => yes(x.enabled)]],
  fields: () => [{ key: 'number', label: 'شماره کارت', ltr: true, required: true }, { key: 'holder', label: 'نام صاحب حساب' }, { key: 'bank', label: 'بانک' },
    { key: 'sort', label: 'ترتیب', type: 'number' }, { key: 'enabled', label: 'فعال', type: 'bool' }],
}));

// ---------- payments ----------
route(/^\/admin\/payments$/, async (main) => {
  const st = new URLSearchParams(location.search).get('status') ?? 'submitted';
  const { items } = await get(`/api/admin/payments?status=${st}`);
  const tabs = [['submitted', 'منتظر بررسی'], ['pending', 'منتظر واریز'], ['approved', 'تأییدشده'], ['rejected', 'ردشده'], ['', 'همه']];
  main.innerHTML = `<div class="page-head"><div><h1>رسیدها و شارژها</h1><p>مبلغ واریزی هر درخواست یکتاست؛ آن را با صورت‌حساب بانک تطبیق دهید.</p></div></div>
    <div class="chips" style="margin-bottom:12px">${tabs.map(([k, l]) => `<a class="chip" href="/admin/payments?status=${k}" style="${k === st ? 'border-color:var(--ink);font-weight:700' : ''}">${l}</a>`).join('')}</div>
    <section class="panel">${items.length ? `<div class="table-wrap"><table><thead><tr><th>#</th><th>کاربر</th><th>مبلغ واریزی</th><th>کارت</th><th>وضعیت</th><th>زمان</th></tr></thead><tbody>
    ${items.map((p, i) => `<tr class="click" data-i="${i}"><td>${n(p.id)}</td><td class="ltr small">${esc(p.user_email)}</td><td class="num"><b>${money(p.pay_amount)}</b></td><td class="ltr small">${esc(p.card?.number?.slice(-4) || '')}</td><td>${status(p.status)}</td><td class="small">${datetime(p.created_at)}</td></tr>`).join('')}
    </tbody></table></div>` : empty('موردی نیست.')}</section>`;
  main.querySelector('tbody')?.addEventListener('click', async (e) => {
    const tr = e.target.closest('tr[data-i]');
    if (!tr) return;
    const p = items[+tr.dataset.i];
    let preview = '<p class="muted">رسیدی ارسال نشده است.</p>';
    if (p.has_receipt) {
      const res = await fetch(`/api/admin/payments/${p.id}/receipt`, { credentials: 'same-origin' });
      const blob = await res.blob();
      const url = URL.createObjectURL(blob);
      preview = blob.type.startsWith('image/') ? `<img class="receipt-img" src="${url}" alt="رسید">` : `<a class="btn" href="${url}" target="_blank" rel="noopener">باز کردن فایل رسید</a>`;
    }
    const m = modal(`<h2>رسید #${n(p.id)}</h2><dl class="kv" style="margin:10px 0"><dt>کاربر</dt><dd class="ltr">${esc(p.user_email)}</dd><dt>مبلغ درخواستی</dt><dd>${money(p.amount)}</dd>
      <dt>مبلغ واریزی</dt><dd><b>${money(p.pay_amount)}</b></dd><dt>کارت</dt><dd class="ltr">${esc(p.card?.number || '')}</dd>${p.customer_note ? `<dt>یادداشت</dt><dd>${esc(p.customer_note)}</dd>` : ''}</dl>
      ${preview}
      ${['pending', 'submitted'].includes(p.status) && can('payments.approve') ? `<form class="form" style="margin-top:12px"><div class="form-grid"><label class="field">مبلغ شارژ (خالی = ${n(p.pay_amount)})<input name="amount" inputmode="numeric"></label>
      <label class="field">یادداشت<input name="note"></label></div><div class="row end"><button type="button" class="btn danger" id="rej">رد رسید</button><button class="btn go">تأیید و شارژ کیف پول</button></div></form>` : `<div class="row end" style="margin-top:12px">${status(p.status)}<button class="btn" data-close>بستن</button></div>`}`, { wide: true });
    const f = m.el.querySelector('form');
    if (!f) return;
    f.onsubmit = (ev) => { ev.preventDefault(); busy(ev.submitter, async () => { await post(`/api/admin/payments/${p.id}/approve`, { amount: num(f.amount.value), note: f.note.value }); m.close(); toast('تأیید شد'); go(location.pathname + location.search, true); }); };
    $('#rej', m.el).onclick = (ev) => busy(ev.target, async () => { await post(`/api/admin/payments/${p.id}/reject`, { note: f.note.value }); m.close(); toast('رد شد'); go(location.pathname + location.search, true); });
  });
});

// ---------- users ----------
route(/^\/admin\/users$/, async (main) => {
  const q = new URLSearchParams(location.search).get('q') || '';
  const { items } = await get(`/api/admin/users?q=${encodeURIComponent(q)}`);
  main.innerHTML = `<div class="page-head"><div><h1>کاربران</h1></div><form class="row" id="s"><input name="q" value="${esc(q)}" placeholder="ایمیل، نام، تلفن، تلگرام یا شناسه"><button class="btn">جست‌وجو</button></form></div>
    <section class="panel">${items.length ? `<div class="table-wrap"><table><thead><tr><th>#</th><th>کاربر</th><th>موجودی</th><th>وضعیت</th><th>عضویت</th></tr></thead><tbody>
    ${items.map((u) => `<tr class="click" data-href="/admin/users/${u.id}"><td>${n(u.id)}</td><td><span class="ltr">${esc(u.email || (u.telegram_username ? '@' + u.telegram_username : '—'))}</span><div class="small muted">${esc(u.name)}${u.is_admin ? ' (ادمین)' : ''}</div></td>
      <td class="num">${money(u.balance)}</td><td>${status(u.status)}</td><td class="small">${date(u.created_at)}</td></tr>`).join('')}</tbody></table></div>` : empty('کاربری پیدا نشد.')}</section>`;
  $('#s', main).onsubmit = (e) => { e.preventDefault(); go(`/admin/users?q=${encodeURIComponent(e.target.q.value)}`); };
});

route(/^\/admin\/users\/(\d+)$/, async (main, [id]) => {
  const [r, groups, svcs] = await Promise.all([get(`/api/admin/users/${id}`), get('/api/admin/groups'), get(`/api/admin/services?user_id=${id}`)]);
  const u = r.user;
  main.innerHTML = `<div class="page-head"><div><a class="small" href="/admin/users">کاربران</a><h1 class="ltr">${esc(u.email || u.telegram_username || '#' + u.id)}</h1><div class="row">${status(u.status)}<span class="muted">${esc(u.name)}</span></div></div></div>
    <div class="grid two">
      <section class="panel"><h2 style="margin-bottom:10px">مشخصات</h2><form class="form" id="uf">
        ${fieldHTML({ key: 'name', label: 'نام' }, u.name)}${fieldHTML({ key: 'phone', label: 'تلفن', ltr: true }, u.phone)}
        ${fieldHTML({ key: 'group_id', label: 'گروه', type: 'select', options: [[0, '—'], ...groups.items.map((g) => [g.id, g.name])] }, u.group_id)}
        ${fieldHTML({ key: 'credit_limit', label: 'سقف بدهی مجاز', type: 'number' }, u.credit_limit)}
        ${fieldHTML({ key: 'status', label: 'وضعیت', type: 'select', options: [['active', 'فعال'], ['blocked', 'مسدود']] }, u.status)}
        <div><button class="btn primary">ذخیره</button></div></form></section>
      <section class="panel"><div class="figure" style="margin-bottom:12px"><span>موجودی</span><b>${money(u.balance)}</b></div>
        ${can('users.wallet') ? `<form class="form" id="wf"><div class="form-grid"><label class="field">مبلغ (منفی = کسر)<input name="amount" inputmode="numeric" required></label><label class="field">دلیل<input name="note" required></label></div>
        <label class="check"><input type="checkbox" name="neg"> اجازه‌ی منفی شدن موجودی</label><div><button class="btn primary">ثبت تغییر موجودی</button></div></form>` : ''}</section>
    </div>
    <section class="panel" style="margin-top:16px"><h2 style="margin-bottom:10px">سرویس‌ها</h2>${svcs.items.length ? `<table><tbody>${svcs.items.map((s) => `<tr class="click" data-href="/admin/services/${s.id}"><td>#${n(s.id)} ${esc(s.title)}</td><td>${status(s.status)}</td><td class="small">${date(s.expires_at)}</td></tr>`).join('')}</tbody></table>` : '<p class="muted">سرویسی ندارد.</p>'}</section>
    <section class="panel" style="margin-top:16px"><h2 style="margin-bottom:10px">تراکنش‌ها</h2><div class="table-wrap"><table><tbody>${r.transactions.map((t) => `<tr><td class="small">${datetime(t.created_at)}</td><td>${esc(t.note || t.type)}</td><td class="num">${money(t.amount)}</td><td class="num muted">${money(t.balance_after)}</td></tr>`).join('') || '<tr><td class="muted">تراکنشی نیست.</td></tr>'}</tbody></table></div></section>`;
  $('#uf', main).onsubmit = (e) => { e.preventDefault(); busy(e.submitter, async () => {
    const f = e.target;
    await put(`/api/admin/users/${id}`, { name: f.name.value, phone: f.phone.value, group_id: +f.group_id.value, credit_limit: num(f.credit_limit.value), status: f.status.value });
    toast('ذخیره شد'); go(location.pathname, true);
  }); };
  $('#wf', main)?.addEventListener('submit', (e) => { e.preventDefault(); busy(e.submitter, async () => {
    const f = e.target;
    await post(`/api/admin/users/${id}/wallet`, { amount: num(f.amount.value), note: f.note.value, allow_negative: f.neg.checked });
    toast('موجودی تغییر کرد'); go(location.pathname, true);
  }); });
});

// ---------- services & orders ----------
route(/^\/admin\/services$/, async (main) => {
  const p = new URLSearchParams(location.search);
  const { items } = await get(`/api/admin/services?${p}`);
  const sts = ['', 'active', 'waiting_peer', 'provisioning', 'limited', 'expired', 'paused', 'suspended', 'failed', 'deleted'];
  main.innerHTML = `<div class="page-head"><div><h1>سرویس‌ها</h1></div><form class="row" id="f">
    <select name="status">${sts.map((s) => `<option value="${s}" ${p.get('status') === s ? 'selected' : ''}>${s ? statusFa[s] : 'همه‌ی وضعیت‌ها'}</option>`).join('')}</select>
    <select name="type"><option value="">همه</option><option value="port" ${p.get('type') === 'port' ? 'selected' : ''}>پورت</option><option value="outbound" ${p.get('type') === 'outbound' ? 'selected' : ''}>اوتباند</option></select>
    <input name="q" value="${esc(p.get('q') || '')}" placeholder="شناسه، ایمیل یا پورت"><button class="btn">فیلتر</button></form></div>
    <section class="panel">${items.length ? `<div class="table-wrap"><table><thead><tr><th>#</th><th>سرویس</th><th>کاربر</th><th>مصرف</th><th>وضعیت</th><th>انقضا</th></tr></thead><tbody>
    ${items.map((s) => `<tr class="click" data-href="/admin/services/${s.id}"><td>${n(s.id)}</td><td>${esc(s.title)}<div class="small muted ltr">${(s.ports || []).map((x) => x.port).join(' ')}</div></td><td class="small ltr">${esc(s.user_email)}</td>
      <td class="small">${s.type === 'port' ? `${bytes(s.used_up + s.used_down)}${s.quota_bytes ? ` / ${bytes(s.quota_bytes)}` : ''}` : bytes(s.used_up + s.used_down)}</td><td>${status(s.status)}</td><td class="small">${date(s.expires_at)}</td></tr>`).join('')}
    </tbody></table></div>` : empty('سرویسی پیدا نشد.')}</section>`;
  $('#f', main).onsubmit = (e) => { e.preventDefault(); const f = e.target; go(`/admin/services?status=${f.status.value}&type=${f.type.value}&q=${encodeURIComponent(f.q.value)}`); };
});

route(/^\/admin\/services\/(\d+)$/, async (main, [id]) => {
  const { service: s, orders } = await get(`/api/admin/services/${id}`);
  main.innerHTML = `<div class="page-head"><div><a class="small" href="/admin/services">سرویس‌ها</a><h1>${esc(s.title)} <span class="muted">#${n(s.id)}</span></h1>
      <div class="row">${status(s.status)}<a class="small ltr" href="/admin/users/${s.user_id}">${esc(s.user_email)}</a>${s.status_reason ? `<span class="small muted">${esc(s.status_reason)}</span>` : ''}</div></div>
      <div class="row">${s.status === 'paused' ? '<button class="btn" data-st="active">ادامه</button>' : s.status !== 'deleted' ? '<button class="btn" data-st="paused">توقف</button>' : ''}${s.status !== 'deleted' ? '<button class="btn danger" id="del">حذف سرویس</button>' : ''}</div></div>
    <div class="grid two">
      <section class="panel stack">${s.type === 'port' ? meter(s.used_up + s.used_down, s.quota_bytes) : s.locations.map((l) => `<div><b>${esc(l.flag)} ${esc(l.name)}</b> ${status(l.status)}${meter(l.used_up + l.used_down, l.quota_bytes)}</div>`).join('')}
        <dl class="kv"><dt>سرور</dt><dd>${esc(s.node_name || '—')}</dd><dt>هسته</dt><dd>${esc(s.core || '—')} ${s.delivery === 'dedicated' ? '(اختصاصی)' : ''}</dd><dt>پورت‌ها</dt><dd class="ltr">${(s.ports || []).map((p) => `${p.port}→${p.to_port}`).join(', ') || '—'}</dd>
        <dt>روش فروش</dt><dd>${esc(s.model)}</dd><dt>ساخته‌شده</dt><dd>${datetime(s.created_at)}</dd></dl>
        ${s.tunnel?.install_cmd ? copyField(s.tunnel.install_cmd) : ''}${s.sub_url ? copyField(s.sub_url) : ''}</section>
      <section class="panel"><h2 style="margin-bottom:10px">تغییر محدودیت‌ها</h2><form class="form" id="lim">
        ${fieldHTML({ key: 'quota_bytes', label: 'حجم (گیگ، ۰ = نامحدود)', type: 'bytesGB' }, s.quota_bytes)}
        ${fieldHTML({ key: 'expires_at', label: 'انقضا', type: 'date' }, s.expires_at)}
        <div class="form-grid">${fieldHTML({ key: 'rate_mbit', label: 'سرعت (مگابیت)', type: 'number' }, s.rate_mbit)}${fieldHTML({ key: 'max_ips', label: 'IP هم‌زمان', type: 'number' }, s.max_ips)}</div>
        <label class="check"><input type="checkbox" name="reset"> صفر کردن مصرف دوره</label><div><button class="btn primary">اعمال</button></div></form></section></div>
    <section class="panel" style="margin-top:16px"><h2 style="margin-bottom:10px">سفارش‌ها</h2><table><tbody>${orders.map((o) => `<tr><td>#${n(o.id)}</td><td>${{ new: 'خرید', renew: 'تمدید', addon: 'افزودنی' }[o.kind]}</td><td class="num">${money(o.amount)}</td><td>${status(o.status)}</td><td class="small">${datetime(o.created_at)}</td>
      <td>${o.status === 'paid' && o.amount > 0 && can('users.wallet') ? `<button class="btn small" data-refund="${o.id}">برگشت وجه</button>` : ''}</td></tr>`).join('')}</tbody></table></section>`;
  $('#lim', main).onsubmit = (e) => { e.preventDefault(); busy(e.submitter, async () => {
    const f = e.target;
    const v = readFields(f, [{ key: 'quota_bytes', type: 'bytesGB' }, { key: 'expires_at', type: 'date' }, { key: 'rate_mbit', type: 'number' }, { key: 'max_ips', type: 'number' }]);
    await put(`/api/admin/services/${id}`, { ...v, reset_usage: f.reset.checked });
    toast('اعمال شد'); go(location.pathname, true);
  }); };
  $$('[data-st]', main).forEach((b) => (b.onclick = () => busy(b, async () => { await put(`/api/admin/services/${id}`, { status: b.dataset.st }); go(location.pathname, true); })));
  $('#del', main)?.addEventListener('click', async (e) => {
    if (!(await confirmBox('سرویس حذف و پورت‌ها آزاد می‌شوند. ادامه؟', 'حذف سرویس'))) return;
    busy(e.target, async () => { await del(`/api/admin/services/${id}`); toast('حذف شد'); go(location.pathname, true); });
  });
  main.addEventListener('click', async (e) => {
    const b = e.target.closest('[data-refund]');
    if (!b || !(await confirmBox('مبلغ این سفارش به کیف پول کاربر برگردد؟', 'برگشت وجه'))) return;
    busy(b, async () => { await post(`/api/admin/orders/${b.dataset.refund}/refund`, { reason: 'برگشت وجه توسط ادمین' }); go(location.pathname, true); });
  });
});

route(/^\/admin\/orders$/, async (main) => {
  const { items } = await get('/api/admin/orders');
  main.innerHTML = `<div class="page-head"><div><h1>سفارش‌ها</h1></div></div><section class="panel">${items.length ? `<div class="table-wrap"><table><thead><tr><th>#</th><th>کاربر</th><th>نوع</th><th>مبلغ</th><th>وضعیت</th><th>زمان</th></tr></thead><tbody>
    ${items.map((o) => `<tr class="click" data-href="/admin/services/${o.service_id}"><td>${n(o.id)}</td><td class="ltr small">${esc(o.user_email)}</td><td>${{ new: 'خرید', renew: 'تمدید', addon: 'افزودنی' }[o.kind]}</td><td class="num">${money(o.amount)}</td><td>${status(o.status)}</td><td class="small">${datetime(o.created_at)}</td></tr>`).join('')}
    </tbody></table></div>` : empty('سفارشی ثبت نشده است.')}</section>`;
});

// ---------- tickets, broadcast ----------
route(/^\/admin\/tickets$/, async (main) => {
  const { items } = await get('/api/admin/tickets');
  main.innerHTML = `<div class="page-head"><div><h1>تیکت‌ها</h1></div></div><section class="panel">${items.length ? `<table><tbody>${items.map((t) => `<tr class="click" data-href="/admin/tickets/${t.id}"><td>${esc(t.subject)}</td><td class="ltr small">${esc(t.user_email)}</td><td>${status(t.status)}</td><td class="small">${datetime(t.updated_at)}</td></tr>`).join('')}</tbody></table>` : empty('تیکتی نیست.')}</section>`;
});
route(/^\/admin\/tickets\/(\d+)$/, ticketView('/api/admin/tickets'));

route(/^\/admin\/broadcast$/, async (main) => {
  main.innerHTML = `<div class="page-head"><div><h1>پیام همگانی</h1><p>به همه‌ی کاربران فعال در پنل و (اگر وصل کرده باشند) در تلگرام فرستاده می‌شود.</p></div></div>
    <section class="panel"><form class="form" id="b"><label class="field">عنوان<input name="title" required></label><label class="field">متن<textarea name="body"></textarea></label><div><button class="btn primary">ارسال</button></div></form></section>`;
  $('#b', main).onsubmit = async (e) => {
    e.preventDefault();
    if (!(await confirmBox('پیام برای همه ارسال شود؟', 'ارسال'))) return;
    busy(e.submitter, async () => { const r = await post('/api/admin/broadcast', { title: e.target.title.value, body: e.target.body.value }); toast(`برای ${n(r.recipients)} نفر در صف ارسال است`); e.target.reset(); });
  };
});

// ---------- settings ----------
route(/^\/admin\/settings$/, async (main) => {
  const { defs, values } = await get('/api/admin/settings');
  const groupFa = { general: 'عمومی', users: 'کاربران', usage: 'مصرف', lifecycle: 'چرخه‌ی سرویس', notify: 'اعلان‌ها', payments: 'پرداخت', pricing: 'قیمت', bot: 'ربات تلگرام' };
  const optFa = { open: 'باز', closed: 'بسته', invite: 'فقط با دعوت', toman: 'تومان', usd: 'دلار', usdt: 'تتر', 1073741824: '۲ به توان ۳۰ (GiB)', 1000000000: '۱۰ به توان ۹ (GB)' };
  const groups = [...new Set(defs.map((d) => d.group))];
  const fields = defs.map((d) => ({ key: d.key, label: d.label, help: d.kind === 'secret' ? 'برای تغییر مقدار تازه بنویسید' : d.help,
    type: { bool: 'bool', int: 'number', enum: 'select', text: 'textarea', secret: 'password' }[d.kind] || 'text', options: d.options?.map((o) => [o, optFa[o] || o]) }));
  main.innerHTML = `<div class="page-head"><div><h1>تنظیمات</h1></div></div><form id="sf" class="stack">
    ${groups.map((g) => `<section class="panel form"><h2>${groupFa[g] || g}</h2>${fields.filter((f, i) => defs[i].group === g).map((f) => fieldHTML(f, values[f.key])).join('')}</section>`).join('')}
    <div class="row end"><button class="btn primary">ذخیره‌ی تنظیمات</button></div></form>`;
  $('#sf', main).onsubmit = (e) => { e.preventDefault(); busy(e.submitter, async () => { await put('/api/admin/settings', readFields(e.target, fields)); toast('ذخیره شد'); location.reload(); }); };
});

// ---------- admins, roles, audit ----------
route(/^\/admin\/roles$/, async (main) => {
  const meta = await get('/api/admin/roles');
  return resource({
    path: '/api/admin/roles', title: 'نقش‌ها', newLabel: 'نقش تازه', intro: 'هر نقش مجموعه‌ای از دسترسی‌هاست. مالک پنل همیشه دسترسی کامل دارد.',
    cols: [['نقش', (x) => esc(x.name)], ['دسترسی‌ها', (x) => n(x.permissions.length)], ['اعضا', (x) => n(x.members)]],
    fields: () => [{ key: 'name', label: 'نام', required: true }, { key: 'permissions', label: 'دسترسی‌ها', type: 'multi', options: meta.permissions.filter((p) => p.id !== 'admins.manage').map((p) => [p.id, p.label]) }],
  })(main);
});

route(/^\/admin\/admins$/, async (main) => {
  const [a, roles] = await Promise.all([get('/api/admin/admins'), get('/api/admin/roles')]);
  main.innerHTML = `<div class="page-head"><div><h1>ادمین‌ها</h1><p>سقف لایسنس: ${a.limit === -1 ? 'نامحدود' : n(a.limit)} ادمین علاوه بر مالک.</p></div><button class="btn primary" id="new">ادمین تازه</button></div>
    <section class="panel"><table><tbody>${a.items.map((x) => `<tr><td class="ltr">${esc(x.email)}</td><td>${esc(x.name)}</td><td>${x.is_owner ? 'مالک' : x.role_ids.map((r) => esc(roles.items.find((y) => y.id === r)?.name || r)).join('، ')}</td><td>${status(x.status)}</td>
      <td><div class="row end">${x.is_owner ? '' : `<button class="btn small" data-edit="${x.id}">نقش‌ها</button><button class="btn small" data-tog="${x.id}" data-s="${x.status === 'active' ? 'blocked' : 'active'}">${x.status === 'active' ? 'مسدود' : 'فعال'}</button><button class="btn small danger" data-rm="${x.id}">حذف دسترسی</button>`}</div></td></tr>`).join('')}</tbody></table></section>`;
  const roleField = { key: 'role_ids', label: 'نقش‌ها', type: 'multi', numeric: true, options: roles.items.map((r) => [r.id, r.name]) };
  $('#new', main).onclick = () => {
    const fs = [{ key: 'email', label: 'ایمیل', ltr: true }, { key: 'name', label: 'نام' }, { key: 'password', label: 'رمز اولیه', type: 'password' }, roleField];
    const m = modal(`<h2>ادمین تازه</h2><form class="form" style="margin-top:10px">${fs.map((f) => fieldHTML(f)).join('')}<div class="row end"><button class="btn primary">ساخت</button></div></form>`);
    m.el.querySelector('form').onsubmit = (e) => { e.preventDefault(); busy(e.submitter, async () => { await post('/api/admin/admins', readFields(e.target, fs)); m.close(); go(location.pathname, true); }); };
  };
  main.addEventListener('click', (e) => {
    const b = e.target.closest('button');
    if (!b) return;
    if (b.dataset.edit) {
      const x = a.items.find((y) => y.id === +b.dataset.edit);
      const m = modal(`<h2>نقش‌های ${esc(x.email)}</h2><form class="form" style="margin-top:10px">${fieldHTML(roleField, x.role_ids)}<div class="row end"><button class="btn primary">ذخیره</button></div></form>`);
      m.el.querySelector('form').onsubmit = (ev) => { ev.preventDefault(); busy(ev.submitter, async () => { await put(`/api/admin/admins/${x.id}`, readFields(ev.target, [roleField])); m.close(); go(location.pathname, true); }); };
    }
    if (b.dataset.tog) busy(b, async () => { await put(`/api/admin/admins/${b.dataset.tog}`, { status: b.dataset.s }); go(location.pathname, true); });
    if (b.dataset.rm) busy(b, async () => { if (await confirmBox('دسترسی ادمین برداشته شود؟')) { await put(`/api/admin/admins/${b.dataset.rm}`, { remove: true }); go(location.pathname, true); } });
  });
});

route(/^\/admin\/audit$/, async (main) => {
  const { items } = await get('/api/admin/audit?limit=200');
  main.innerHTML = `<div class="page-head"><div><h1>لاگ ممیزی</h1><p>هر تغییری که ادمین‌ها انجام داده‌اند، با مقدار قبل و بعد.</p></div></div>
    <section class="panel"><div class="table-wrap"><table><thead><tr><th>زمان</th><th>ادمین</th><th>کار</th><th>هدف</th><th>IP</th></tr></thead><tbody>
    ${items.map((x, i) => `<tr class="click" data-i="${i}"><td class="small">${datetime(x.created_at)}</td><td class="ltr small">${esc(x.actor_email || '—')}</td><td class="ltr">${esc(x.action)}</td><td class="ltr small">${esc(x.target_type)} ${esc(x.target_id)}</td><td class="ltr small">${esc(x.ip)}</td></tr>`).join('')}
    </tbody></table></div></section>`;
  main.querySelector('tbody')?.addEventListener('click', (e) => {
    const tr = e.target.closest('tr[data-i]');
    if (!tr) return;
    const x = items[+tr.dataset.i];
    modal(`<h2 class="ltr">${esc(x.action)}</h2><div class="grid two" style="margin-top:10px"><div><h3>قبل</h3><pre class="code small pre">${esc(JSON.stringify(x.before ?? null, null, 2))}</pre></div>
      <div><h3>بعد</h3><pre class="code small pre">${esc(JSON.stringify(x.after ?? null, null, 2))}</pre></div></div>`, { wide: true });
  });
});

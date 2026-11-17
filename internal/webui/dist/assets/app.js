// Entry: bootstrap, landing, auth screens, shell and navigation.
import { state, esc, $, get, post, match, onRender, go, busy, money, n, icon, ring, arcs } from './core.js';
import './customer.js';
import './admin.js';

const app = document.getElementById('app');
const site = () => state.boot.settings['site.name'] || 'Neo Sell';

async function boot() {
  state.boot = await get('/api/bootstrap');
  state.user = state.boot.user || null;
  document.title = site();
}

const brand = (href = '/') => `<a class="brandpill" href="${href}"><span>${esc(site())}</span><span class="mark" aria-hidden="true"></span></a>`;

// ---------- landing (visitors) ----------
async function landingView() {
  let cat = { products: [], locations: [] };
  try { cat = await get('/api/public/catalog'); } catch { /* panel still works without it */ }
  const open = state.boot.license.state === 'active';
  const reg = open && state.boot.settings['registration.mode'] === 'open';
  const pkgs = cat.products.flatMap((p) => p.packages.map((k) => ({ ...k, product: p })));
  const maxGB = Math.max(...pkgs.map((k) => k.gb || 0), 1);
  const cores = [...new Set(cat.products.flatMap((p) => p.cores || []))];
  const coreInitial = { neotunnel: 'NT', kariz: 'KZ', backpack: 'BP' };
  app.innerHTML = `
    <header class="topbar">${brand()}<nav class="pillnav" aria-label="بخش‌ها"><a class="on" href="#top">معرفی</a><a href="#plans">بسته‌ها</a><a href="#how">روند خرید</a>${cat.locations.length ? '<a href="#locations">لوکیشن‌ها</a>' : ''}</nav>
      <div class="actions"><a class="btn ghost" href="/login">ورود</a>${reg ? '<a class="btn white" href="/register">شروع خرید</a>' : ''}</div></header>
    <main class="page landing view-enter" id="top">
      <section class="hero">
        <div><h1 class="display">پورت تانل<br>بی‌دردسر</h1>
          <p style="margin-top:14px"><span class="tagpill">تحویل خودکار</span></p>
          <p class="lede">پورت روی IP ایران بخرید، به سرور خارج خودتان یا ما وصل کنید و مصرف هر گیگ را لحظه‌ای ببینید. اوتباند چند لوکیشنی هم با یک ساب.</p>
          ${cores.length ? `<div class="core-dots" aria-label="هسته‌های پشتیبانی‌شده">${cores.map((c) => `<span title="${esc(c)}">${coreInitial[c] || esc(c.slice(0, 2).toUpperCase())}</span>`).join('')}<span title="بیشتر">${icon('plus', 16, 2)}</span></div>` : ''}
          <div class="row" style="margin-top:26px">${reg ? '<a class="btn coral" href="/register">ساخت حساب و خرید</a>' : ''}<a class="btn white" href="/login">ورود به پنل</a></div></div>
        <div>${arcs([{ label: `${n(cores.length || 1)} هسته` }, { label: `${n(cat.locations.length || 1)} لوکیشن` }, { label: 'تحویل در چند ثانیه' }], 'امروز', 'همیشه', 'از سفارش تا اتصال')}</div>
        <div class="side-stack">
          <details class="acc" open><summary>چه چیزی می‌خرید</summary><div class="acc-body">
            <div class="line">${icon('port', 16)} پورت روی IP ایران با حجم و مدت دلخواه</div>
            <div class="line">${icon('globe', 16)} اوتباند چند لوکیشنی، هر لوکیشن ساب جدا</div>
            <div class="line">${icon('chart', 16)} مصرف دقیق، شمرده‌شده روی خود سرور</div></div></details>
          <details class="acc"><summary>پرداخت</summary><div class="acc-body"><div class="line">${icon('wallet', 16)} کیف پول و کارت‌به‌کارت با تأیید سریع</div></div></details>
          <details class="acc"><summary>پشتیبانی</summary><div class="acc-body"><div class="line">${icon('chat', 16)} ${esc(state.boot.settings['site.support'] || 'تیکت از داخل پنل')}</div></div></details>
        </div>
      </section>

      <section class="lp-section" id="plans"><div class="spread"><h2>بسته‌های آماده</h2>${pkgs.length ? `<span class="muted small">${n(pkgs.length)} بسته</span>` : ''}</div>
        ${pkgs.length ? `<div class="rail">${pkgs.map((k, i) => `
          <a class="svc-card ${i === 0 ? 'lead' : ''}" href="${reg ? '/register' : '/login'}">
            <div class="head"><div><h3>${esc(k.name)}</h3><span class="muted small">${esc(k.product.title)}</span></div><span class="arrow">${icon('arrow', 16)}</span></div>
            <div class="body">${ring(k.gb ? (k.gb / maxGB) * 100 : 100, { size: 110, big: k.gb ? n(k.gb) : '∞', unit: k.gb ? 'GB' : '', label: `${n(k.days)} روز` , cls: i === 0 ? '' : 'calm' })}
              <ul class="checks">${k.ports ? `<li>${icon('check', 15, 2.2)}${n(k.ports)} پورت</li>` : ''}<li>${icon('check', 15, 2.2)}${n(k.days)} روز اعتبار</li>
              ${k.rate_mbit ? `<li>${icon('check', 15, 2.2)}${n(k.rate_mbit)} مگابیت</li>` : `<li>${icon('check', 15, 2.2)}سرعت آزاد</li>`}</ul></div>
            <div class="spread"><b class="num" style="font-size:1.25rem;font-weight:500">${money(k.price)}</b></div></a>`).join('')}</div>`
          : `<div class="card flat empty"><p>بسته‌ها به‌زودی اینجا نمایش داده می‌شوند. ${open ? 'برای خرید سفارشی وارد شوید.' : ''}</p></div>`}</section>

      <section class="lp-section" id="how"><h2>از سفارش تا اتصال</h2><div class="howto">
        <div class="tile"><b>حساب بسازید</b><span class="muted small">با ایمیل یا از داخل ربات تلگرام.</span></div>
        <div class="tile"><b>کیف پول را شارژ کنید</b><span class="muted small">کارت‌به‌کارت؛ رسید را می‌فرستید و تأیید می‌شود.</span></div>
        <div class="tile"><b>پورت یا اوتباند انتخاب کنید</b><span class="muted small">قدم‌به‌قدم، با قیمت دقیق پیش از پرداخت.</span></div>
        <div class="tile"><b>وصل شوید</b><span class="muted small">سرویس خودکار ساخته می‌شود؛ مصرف را زنده ببینید.</span></div></div></section>

      ${cat.locations.length ? `<section class="lp-section" id="locations"><h2>لوکیشن‌ها</h2><div class="chips">${cat.locations.map((l) => `<span class="chip">${esc(l.flag)} ${esc(l.name)}</span>`).join('')}</div></section>` : ''}
      <footer class="footer"><span>${esc(site())}</span><span>${esc(state.boot.settings['site.support'] || '')}</span></footer>
    </main>`;
}

// ---------- auth ----------
function authPage(title, sub, inner) {
  app.innerHTML = `<main class="auth">
    <section class="auth-art"><div style="position:absolute;inset:auto -10% -6% -10%">${arcs([{ label: 'پورت' }, { label: 'اوتباند' }, { label: 'مصرف زنده' }], '', '', '')}</div>
      <p class="display" style="position:relative;margin-bottom:auto;margin-top:0">${esc(site())}</p></section>
    <section class="auth-form"><div class="card view-enter"><div class="spread" style="margin-bottom:22px">${brand('/')}<a class="small muted" href="/">صفحه‌ی اصلی</a></div>
      <h1>${title}</h1><p class="ink2" style="margin:6px 0 22px">${sub}</p>${inner}</div></section></main>`;
}

function setupView() {
  authPage('راه‌اندازی پنل', 'حساب مالک پنل را بسازید. بعد از آن کلید لایسنس را وارد می‌کنید.', `
    <form class="form" id="f"><label class="field">نام<input name="name" autocomplete="name"></label>
    <label class="field">ایمیل<input name="email" type="email" dir="ltr" required autocomplete="email"></label>
    <label class="field">رمز عبور<input name="password" type="password" required minlength="8" autocomplete="new-password"><small>حداقل ۸ نویسه</small></label>
    <p class="err" id="e" role="alert"></p><button class="btn primary block">ساخت حساب مالک</button></form>`);
  $('#f').onsubmit = (e) => {
    e.preventDefault();
    const f = e.target;
    busy(e.submitter, async () => {
      try { await post('/api/setup', { name: f.name.value, email: f.email.value, password: f.password.value }); await boot(); go('/admin/license', true); }
      catch (err) { $('#e').textContent = err.message; }
    });
  };
}

function loginView() {
  const reg = state.boot.settings['registration.mode'] === 'open' && state.boot.license.state === 'active';
  const linkErr = new URLSearchParams(location.search).get('error') === 'link';
  authPage('خوش برگشتید', linkErr ? 'لینک ورود منقضی شده است؛ دوباره از ربات بگیرید یا با رمز وارد شوید.' : 'برای دیدن سرویس‌ها و خرید وارد شوید.', `
    <form class="form" id="f"><label class="field">ایمیل<input name="email" type="email" dir="ltr" required autocomplete="username"></label>
    <label class="field">رمز عبور<input name="password" type="password" required autocomplete="current-password"></label>
    <p class="err" id="e" role="alert"></p><button class="btn primary block">ورود</button>
    ${reg ? '<p class="small ink2" style="text-align:center">حساب ندارید؟ <a href="/register" style="color:var(--coral)">ثبت‌نام</a></p>' : ''}</form>`);
  $('#f').onsubmit = (e) => {
    e.preventDefault();
    const f = e.target;
    busy(e.submitter, async () => {
      try { await post('/api/auth/login', { email: f.email.value, password: f.password.value }); await boot(); go(state.user.is_admin ? '/admin' : '/', true); }
      catch (err) { $('#e').textContent = err.message; }
    });
  };
}

function registerView() {
  const rules = state.boot.settings['site.rules'];
  authPage('ساخت حساب', 'یک دقیقه طول می‌کشد. بعد کیف پول را شارژ می‌کنید و قدم‌به‌قدم می‌خرید.', `
    <form class="form" id="f"><label class="field">نام<input name="name" autocomplete="name"></label>
    <label class="field">ایمیل<input name="email" type="email" dir="ltr" required autocomplete="email"></label>
    <label class="field">رمز عبور<input name="password" type="password" required minlength="8" autocomplete="new-password"><small>حداقل ۸ نویسه</small></label>
    ${rules ? `<details class="box"><summary>قوانین</summary><p class="pre small">${esc(rules)}</p></details><label class="check"><input type="checkbox" required> قوانین را می‌پذیرم</label>` : ''}
    <p class="err" id="e" role="alert"></p><button class="btn coral block">ساخت حساب</button><p class="small ink2" style="text-align:center">حساب دارید؟ <a href="/login" style="color:var(--coral)">ورود</a></p></form>`);
  $('#f').onsubmit = (e) => {
    e.preventDefault();
    const f = e.target;
    busy(e.submitter, async () => {
      try { await post('/api/auth/register', { name: f.name.value, email: f.email.value, password: f.password.value }); await boot(); go('/', true); }
      catch (err) { $('#e').textContent = err.message; }
    });
  };
}

// ---------- shell ----------
const customerNav = [['/', 'نمای کلی', 'home'], ['/shop', 'خرید', 'cart'], ['/wallet', 'کیف پول', 'wallet'], ['/tickets', 'پشتیبانی', 'chat']];
const adminGroups = [
  ['/admin', 'داشبورد', [['/admin', 'نمای کلی', ''], ['/admin/payments', 'رسیدها', 'payments.approve'], ['/admin/orders', 'سفارش‌ها', 'orders.view']]],
  ['/admin/services', 'مشتریان', [['/admin/services', 'سرویس‌ها', 'services.manage'], ['/admin/users', 'کاربران', 'users.view'], ['/admin/tickets', 'تیکت‌ها', 'tickets.manage'], ['/admin/broadcast', 'پیام همگانی', 'broadcast.send']]],
  ['/admin/products', 'فروش', [['/admin/products', 'محصولات', 'products.manage'], ['/admin/rules', 'قوانین قیمت', 'pricing.manage'], ['/admin/simulate', 'ماشین‌حساب', 'pricing.manage'], ['/admin/port-classes', 'کلاس پورت', 'pricing.manage'], ['/admin/groups', 'گروه‌ها', 'pricing.manage'], ['/admin/discounts', 'تخفیف', 'pricing.manage'], ['/admin/cards', 'کارت‌ها', 'settings.manage']]],
  ['/admin/nodes', 'سرورها', [['/admin/nodes', 'سرور تانل', 'nodes.manage'], ['/admin/tunnels', 'تانل اشتراکی', 'nodes.manage'], ['/admin/xui', 'پنل ثنایی', 'nodes.manage'], ['/admin/outbounds', 'اوتباندها', 'nodes.manage'], ['/admin/locations', 'لوکیشن‌ها', 'products.manage'], ['/admin/cores', 'هسته‌ها', 'nodes.manage']]],
  ['/admin/settings', 'سیستم', [['/admin/settings', 'تنظیمات', 'settings.manage'], ['/admin/system', 'به‌روزرسانی', 'settings.manage'], ['/admin/admins', 'ادمین‌ها', 'admins.manage'], ['/admin/roles', 'نقش‌ها', 'admins.manage'], ['/admin/audit', 'لاگ ممیزی', 'audit.view'], ['/admin/license', 'لایسنس', '']]],
];

function isOn(href) {
  const p = location.pathname;
  return href === p || (href !== '/' && href !== '/admin' && p.startsWith(href + '/'));
}

async function shell() {
  const u = state.user;
  const inAdmin = location.pathname.startsWith('/admin');
  const can = (p) => !p || u.is_owner || u.permissions.includes(p);
  let top = '', sub = '';
  if (inAdmin) {
    const groups = adminGroups.map(([h, l, items]) => [h, l, items.filter((i) => can(i[2]))]).filter((g) => g[2].length);
    const cur = groups.find((g) => g[2].some((i) => isOn(i[0]))) || groups[0];
    top = groups.map(([, l, items]) => `<a href="${items[0][0]}" class="${cur && cur[1] === l ? 'on' : ''}">${l}</a>`).join('');
    sub = cur ? `<nav class="subnav" aria-label="${esc(cur[1])}">${cur[2].map(([h, l]) => `<a href="${h}" class="${isOn(h) ? 'on' : ''}" ${isOn(h) ? 'aria-current="page"' : ''}>${l}</a>`).join('')}</nav>` : '';
  } else {
    top = customerNav.map(([h, l]) => `<a href="${h}" class="${isOn(h) ? 'on' : ''}" ${isOn(h) ? 'aria-current="page"' : ''}>${l}</a>`).join('');
  }
  let bal = '';
  if (!inAdmin) { try { bal = money((await get('/api/wallet')).balance); } catch { /* ignore */ } }
  const lic = state.boot.license;
  const banner = lic.state !== 'active'
    ? `<div class="banner bad">${u.is_admin ? `${esc(lic.reason || 'لایسنس فعال نیست')} — <a href="/admin/license">لایسنس</a>` : 'خرید و شارژ موقتاً متوقف است؛ سرویس‌های فعلی شما کار می‌کنند.'}</div>`
    : lic.warning && u.is_admin ? `<div class="banner warn">${esc(lic.warning)}</div>` : '';
  const who = esc(u.email || (u.telegram_username ? '@' + u.telegram_username : u.name || ''));
  app.innerHTML = `
    <header class="topbar">${brand(inAdmin ? '/admin' : '/')}<nav class="pillnav" aria-label="منو">${top}</nav>
      <div class="actions">${bal ? `<a class="balance-chip" href="/wallet" title="موجودی کیف پول"><small>موجودی</small>${bal}</a>` : ''}
        ${inAdmin ? '' : `<a class="iconbtn" href="/account#notes" aria-label="اعلان‌ها">${icon('bell', 19)}${state.unread ? '<span class="dot"></span>' : ''}</a>`}
        <div style="position:relative"><button class="iconbtn" id="me" aria-haspopup="true" aria-expanded="false" aria-label="حساب">${icon('user', 19)}</button>
          <div class="menu-pop hide" id="mepop" role="menu"><div class="who ltr">${who}</div>
            ${u.is_admin ? `<a href="${inAdmin ? '/' : '/admin'}" role="menuitem">${inAdmin ? 'نمای مشتری' : 'پنل مدیریت'}</a>` : ''}
            ${inAdmin ? '' : '<a href="/account" role="menuitem">حساب کاربری</a>'}
            <button id="logout" role="menuitem">خروج</button></div></div></div></header>
    ${sub}
    <main class="page" id="main" tabindex="-1">${banner}<div id="view"></div></main>
    <nav class="tabbar" aria-label="منوی پایین">${inAdmin
      ? `${[['/admin', 'خانه', 'home'], ['/admin/payments', 'رسیدها', 'receipt'], ['/admin/services', 'سرویس‌ها', 'grid'], ['/admin/nodes', 'سرورها', 'server']].map(([h, l, i]) => `<a href="${h}" class="${isOn(h) ? 'on' : ''}">${icon(i, 20)}<span>${l}</span></a>`).join('')}`
      : `${customerNav.map(([h, l, i]) => `<a href="${h}" class="${isOn(h) ? 'on' : ''}">${icon(i, 20)}<span>${l}</span></a>`).join('')}`}</nav>`;
  const pop = $('#mepop'), me = $('#me');
  me.onclick = (e) => { e.stopPropagation(); pop.classList.toggle('hide'); me.setAttribute('aria-expanded', String(!pop.classList.contains('hide'))); };
  document.addEventListener('click', () => pop.classList.add('hide'), { once: true });
  $('#logout').onclick = async () => { await post('/api/auth/logout'); state.user = null; go('/'); };
  return $('#view');
}

// ---------- render ----------
let renderSeq = 0;
async function render() {
  const my = ++renderSeq;
  const path = location.pathname;
  if (state.boot.needs_setup) return path === '/setup' ? setupView() : go('/setup', true);
  if (!state.user) {
    if (path === '/register') return registerView();
    if (path === '/login') return loginView();
    if (path === '/') return landingView();
    return go('/login', true);
  }
  if (['/login', '/register', '/setup'].includes(path)) return go(state.user.is_admin ? '/admin' : '/', true);
  if (path.startsWith('/admin') && !state.user.is_admin) return go('/', true);
  const r = match(path);
  try { const nt = await get('/api/notifications'); state.unread = nt.unread; } catch { /* ignore */ }
  if (my !== renderSeq) return;
  const view = await shell();
  if (!r) { view.innerHTML = `<div class="empty"><h1>این صفحه وجود ندارد</h1><a class="btn white" href="/">بازگشت</a></div>`; return; }
  view.innerHTML = '<p class="muted" style="padding:40px 0">در حال بارگذاری…</p>';
  try {
    await r.view(view, r.params);
    view.classList.add('view-enter');
    setTimeout(() => view.classList.remove('view-enter'), 700); // only the first paint animates
  } catch (e) {
    if (my !== renderSeq) return;
    view.innerHTML = `<div class="banner bad">${esc(e.message)}</div>`;
  }
  $('#main')?.focus({ preventScroll: true });
  if (location.hash) document.getElementById(location.hash.slice(1))?.scrollIntoView();
  else window.scrollTo(0, 0);
}
onRender(render);

document.addEventListener('click', (e) => {
  const tr = e.target.closest('tr[data-href]');
  if (tr && !e.target.closest('a,button,input')) go(tr.dataset.href);
  const a = e.target.closest('a[href^="#"]');
  if (a) { e.preventDefault(); document.getElementById(a.getAttribute('href').slice(1))?.scrollIntoView({ behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' }); }
});

boot().then(render).catch((e) => { app.innerHTML = `<main class="auth-form" style="min-height:100vh"><div class="card"><h1>پنل در دسترس نیست</h1><p class="err">${esc(e.message)}</p></div></main>`; });

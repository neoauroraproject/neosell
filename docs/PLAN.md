<div dir="rtl">

# Neo Sell — برنامه‌ی جامع (نسخه‌ی ۳)

> سیستم فروش **پورت تانل** (NeoTunnel، Kariz، Backpack) و **اوتباند** (از پنل‌های ثنایی / 3x-ui) با پنل کاربری، کیف پول، پرداخت، ربات تلگرام، مصرف دقیق و **کنترل کامل ادمین روی همه‌چیز**.

**تغییرات نسخه‌ی ۲:**
1. اوتباند: **هر لوکیشن یک ساب جدا با حجم جدا** (ساب نسخه‌ی جدید ثنایی مصرف همه‌ی کانفیگ‌های آن ساب را یکجا حساب می‌کند؛ پس مرز «حجم مشترک» = یک لوکیشن).
2. تانل‌ها: **همه‌ی منطق فروش (ساخت، حجم، زمان، سرعت، IP، شمارش مصرف، قطع/وصل) فقط توسط Neo Sell** و Agent خودش. از قابلیت فروش داخلی هسته‌های تانل (مثل `plan` در NeoTunnel) استفاده نمی‌شود؛ هسته‌ها فقط «حمل ترافیک» هستند.
3. ادمین: **موتور قیمت‌گذاری و مدل فروش کاملاً قابل تنظیم** + تنظیم همه‌ی رفتارهای سیستم از پنل (بخش ۵).

**تغییرات نسخه‌ی ۳:**
4. **لایسنس از روز اول:** همه‌ی لایسنس‌دهی فقط با سرور لایسنس فعلی روی Cloudflare (`hm-license-manager`)، با همان الگوی امضای Ed25519 که NeoSpoof و 3xFactor استفاده می‌کنند (بخش ۲.۳).
5. **باینری تک‌فایلی بدون وابستگی:** کل پروژه یک باینری Go است؛ بدون PostgreSQL، Redis، Node یا Docker روی سرور. دیتابیس SQLite داخلی (pure Go)، رابط وب، ربات، صف کارها و TLS همه داخل همان باینری (بخش ۱۱).

**وضعیت پیاده‌سازی (۱۴۰۵/۰۸/۲۶):** همه‌ی فازهای این برنامه در کد پیاده شده و تست دارد: لایسنس، کاتالوگ و موتور قیمت، کیف پول و کارت‌به‌کارت، سفارش/تمدید/افزودنی، Neo Agent (nftables + درایور NeoTunnel و قالب Kariz/Backpack)، ثنایی و ساب هر لوکیشن، مصرف و چرخه‌ی عمر، پس‌پرداخت، ربات تلگرام، تیکت، رابط وب مشتری و ادمین. باقی‌مانده: تست Agent روی سرور لینوکس واقعی با هر سه هسته، قالب کانفیگ Kariz/Backpack، درگاه‌های پرداخت آنلاین و نمایندگی (فقط ماژول لایسنس آن‌ها آماده است). راهنمای نصب: [README](../README.md).

---

## ۰. خلاصه در یک نگاه

| بخش | کار |
|---|---|
| **پنل مشتری (وب)** | ثبت‌نام، کیف پول، خرید پورت/اوتباند، مصرف دقیق و لیمیت هر سرویس و هر لوکیشن، تمدید، ساب، اتصال تلگرام |
| **پنل ادمین (وب)** | کنترل کامل: نودها، محصولات، **مدل فروش و قیمت هر چیز**، کاربران، گروه‌ها، مالی، پیام‌ها، تنظیمات |
| **ربات تلگرام** | کارهای اصلی مشتری + اعلان‌ها + تأیید رسید و مدیریت سریع برای ادمین |
| **Neo Agent (Go)** | روی هر سرور تانل: ساخت تانل/پورت روی هسته، شمارش دقیق، اعمال لیمیت در کرنل |
| **Provisioner / Reconciler** | ساخت و هم‌گام نگه‌داشتن خودکار سرویس‌ها روی Agentها و پنل‌های ثنایی |
| **Usage Collector** | جمع‌کردن مصرف، سری زمانی، هشدار |
| **Pricing Engine** | محاسبه‌ی قیمت هر سفارش طبق قوانینی که ادمین تعریف کرده |
| **License** | فعال‌سازی و بررسی دوره‌ای با سرور لایسنس Cloudflare؛ ماژول‌ها و سقف‌ها از توکن امضاشده |

---

## ۱. محصولات

### ۱.۱ پورت تانل
مشتری روی **IP ایرانِ فروشنده** یک یا چند پورت می‌خرد که از راه تانل به **سرور خارج خودش** (یا مقصدی که ادمین تعیین کرده) می‌رود.

**دو حالت تحویل** (ادمین برای هر محصول مجاز/غیرمجاز می‌کند):
1. **Dedicated — مقصد = سرور خارج مشتری:** Neo Sell یک دستور یک‌خطی برای سرور خارج مشتری می‌سازد (`bash <(curl -s https://panel/i/<token>)`) که سمت خارجِ همان هسته‌ی تانل (NeoTunnel/Kariz/Backpack) را با کانفیگ مخصوص این سرویس نصب می‌کند. وضعیت «در انتظار اتصال سرور خارج» در پنل و ربات دیده می‌شود.
2. **Shared — مقصد = سرور خارج فروشنده:** تانل از قبل هست؛ فقط پورت روی آن باز می‌شود.

**تعریف پورت توسط مشتری:** مشتری پورت‌ها را می‌نویسد (`443`، `8080,8443`، `2053-2056`) + پورت مقصد. Neo Sell با قوانین ادمین (بازه‌های مجاز، پورت‌های ممنوع/رزرو، کلاس قیمتی پورت) و آزاد بودن واقعی روی سرور (Agent با `ss` و جدول داخلی) چک می‌کند؛ اگر آزاد نبود جایگزین پیشنهاد می‌دهد. یا «پورت تصادفی».

### ۱.۲ اوتباند (ثنایی / 3x-ui)
ادمین روی پنل‌های ثنایی اینباند می‌سازد (مثلاً `vless tcp 2083`) و در Neo Sell به‌عنوان **قالب اوتباند** ثبت می‌کند. با خرید، کلاینت ساخته می‌شود و مشتری کانفیگی می‌گیرد که روی سرور ایران خودش **outbound** بگذارد.

**مدل مولتی‌لوکیشن (اصلاح‌شده):**

```
سرویس اوتباند مشتری
├─ لوکیشن آلمان   → ساب ثنایی (subId: ns-<svc>-de)  حجم 100GB ، مصرف جدا
│    ├─ vless tcp 2083     ┐ همه با همان subId روی همان پنل
│    └─ vless ws 443 (CDN) ┘ → مصرفشان روی همین ساب جمع می‌شود
├─ لوکیشن هلند    → ساب ثنایی (subId: ns-<svc>-nl)  حجم 50GB ، مصرف جدا
└─ لوکیشن فنلاند  → ساب ثنایی (subId: ns-<svc>-fi)  حجم 50GB ، مصرف جدا
```

- **هر لوکیشن = یک ساب ثنایی مستقل** با حجم، تاریخ انقضا، سقف IP و وضعیت خودش. داخل یک لوکیشن، چند اینباند می‌توانند زیر همان ساب باشند و ترافیکشان (طبق قابلیت نسخه‌ی جدید ثنایی) یکجا حساب شود.
- **اعمال حجم هر لوکیشن توسط خود پنل ثنایی** (دقیق، بدون وابستگی به Neo Sell). Neo Sell فقط مصرف را می‌خواند و نمایش می‌دهد.
- تمدید، افزودن حجم و حذف **برای هر لوکیشن جدا** ممکن است (یا همه با هم، اگر ادمین مدل فروش را «بسته‌ای» تعریف کرده باشد).
- **خروجی تحویل به مشتری:**
  - برای هر لوکیشن: لینک ساب خودش (از راه Neo Sell پراکسی می‌شود تا آدرس پنل ثنایی لو نرود: `/sub/<token>/de`)، لینک‌های اشتراک (`vless://…`) و **JSON آماده‌ی outbound برای xray** با `tag` مخصوص لوکیشن.
  - یک **لینک تجمیعی** (`/sub/<token>`) که همه‌ی لوکیشن‌ها را با هم می‌دهد (فقط برای راحتی؛ حجم‌ها همچنان جدا هستند) — ادمین می‌تواند خاموشش کند.
  - هدر `subscription-userinfo` در هر ساب لوکیشن = مصرف و حجم همان لوکیشن.

### ۱.۳ افزودنی‌ها (Add-ons)
حجم اضافه (برای پورت یا برای یک لوکیشن مشخص)، روز اضافه، پورت اضافه، لوکیشن اضافه، افزایش سرعت، IP بیشتر. همه با قیمت‌گذاری ادمین (بخش ۵).

### ۱.۴ چرخه‌ی عمر سرویس
```
pending_payment → provisioning → (waiting_peer) → active
active → limited (حجم تمام)  → active (با خرید حجم)
active → expired (زمان تمام) → active (با تمدید)
limited/expired → suspended (بعد از مهلت قابل تنظیم) → deleted (پورت آزاد می‌شود)
هر وضعیت ⇄ paused (دستی توسط ادمین)
provisioning → failed → retry خودکار / بازگشت وجه به کیف پول
```
در اوتباند، وضعیت **برای هر لوکیشن جدا** هم نگه داشته می‌شود (مثلاً آلمان `limited`، هلند `active`).

---

## ۲. معماری

```
   Customer (browser)            Telegram (Customer / Admin)
          │ HTTPS                          │ long-poll یا webhook (با پراکسی اختیاری)
   ┌──────▼────────────────────────────────▼───────────────────┐
   │              neosell  (یک باینری Go، systemd)            │
   │  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────────┐ │
   │  │ HTTP API │ │ Web UI   │ │ Bot      │ │ Sub server   │ │
   │  │          │ │(go:embed)│ │          │ │ /sub /i      │ │
   │  └────┬─────┘ └──────────┘ └────┬─────┘ └──────────────┘ │
   │  ┌────▼────────────────────────▼────────────────────────┐│
   │  │ Core: auth, rbac, wallet, pricing, orders, services  ││
   │  ├──────────────────────────────────────────────────────┤│
   │  │ Jobs (صف داخلی روی SQLite): provision, usage,        ││
   │  │ reconcile, notify, expire, payg, backup              ││
   │  ├──────────────────────────────────────────────────────┤│
   │  │ License guard ◄──────► license.hmray.pro (Cloudflare)││
   │  ├──────────────────────────────────────────────────────┤│
   │  │ SQLite (WAL) ـ  /var/lib/neosell/neosell.db          ││
   │  └──────────────────────────────────────────────────────┘│
   └──────┬───────────────────────────────────────┬───────────┘
          ▼ WebSocket (Agent وصل می‌شود)          ▼ HTTPS
   ┌──────────── سرور تانل ایران ────────────┐   3x-ui #1 … #N
   │ neosell agent  (همان باینری، حالت agent) │   (هر لوکیشن)
   │  ├─ driver: neotunnel │ kariz │ backpack │
   │  ├─ nftables: counter + quota + expiry   │
   │  └─ tc: سرعت ؛ meters: سقف IP/اتصال     │
   └──────────────────────────────────────────┘
```

### ۲.۱ تانل‌ها: همه‌چیز در دست Neo Sell

**اصل:** هسته‌ی تانل فقط ترافیک را می‌برد. هر چیزی که به «فروش» مربوط است — شمارش، حجم، زمان، سرعت، IP، باز/بسته کردن — در **Neo Agent** و یکسان برای هر سه هسته انجام می‌شود. نتیجه:
- رفتار و دقت یکسان برای NeoTunnel، Kariz و Backpack.
- عوض کردن هسته‌ی یک سرویس (مثلاً از Kariz به NeoTunnel) بدون از دست دادن مصرف و لیمیت.
- وابستگی نداشتن به قابلیت‌های فروش هر هسته.

**Neo Agent (یک باینری Go، amd64/arm64):**

| بخش | کار |
|---|---|
| **Driver هسته** | فقط transport: نصب/به‌روزرسانی باینری هسته، ساخت کانفیگ از روی **قالبی که ادمین در پنل ویرایش می‌کند**، systemd unit مخصوص هر سرویس یا هر تانل (`neosell-kariz-<svc>.service`)، start/stop/restart، health، لاگ، ساخت دستور نصب سمت خارج برای حالت Dedicated |
| **NeoTunnel driver** | از API محلی NeoTunnel روی `127.0.0.1` فقط برای ساخت/حذف تانل و forward و گرفتن دستور اتصال سرور خارج استفاده می‌کند؛ **`/api/forwards/plan` و `renew` استفاده نمی‌شوند** |
| **Accounting** | جدول اختصاصی `inet neosell` در nftables: برای هر پورت فروخته‌شده counter ورودی/خروجی (tcp/udp) |
| **Enforcement در کرنل** | `quota over <bytes> drop` برای حجم؛ set زمان‌دار برای انقضا؛ `tc` (HTB/policer) برای سرعت؛ nft meters برای سقف IP و اتصال هم‌زمان؛ `paused` = rule drop |
| **خودمختاری** | لیمیت‌ها و تاریخ‌ها محلی ذخیره می‌شوند ⇒ اگر ارتباط با Neo Sell قطع شود (مثلاً اختلال بین‌الملل)، سرویس‌ها باز هم دقیق سر حجم/زمان بسته می‌شوند؛ دلتاهای مصرف بافر و بعد ارسال می‌شوند |
| **ارتباط** | Agent خودش به Core وصل می‌شود (WebSocket با reconnect) ⇒ پورت ورودی روی سرور ایران لازم نیست. توکن یکتا برای هر نود + امضای HMAC هر پیام. فرمان‌ها idempotent با `command_id` |
| **نصب** | ادمین «افزودن سرور» می‌زند و یک دستور یک‌خطی می‌گیرد؛ همان باینری `neosell` با حالت `agent` نصب می‌شود (بدون وابستگی؛ فقط `nft` و `tc` که در اوبونتو هست) |

نکته‌ی فنی: جدول nftables Agent باید طوری اولویت‌بندی شود که با قوانین NAT خود هسته‌ها (به‌خصوص NeoTunnel که در کرنل کار می‌کند) تداخل نداشته باشد و ترافیک **قبل از سربار تانل** شمرده شود. این در فاز ۱ روی هر سه هسته تست می‌شود.

```ts
// اینترفیس فرمان‌های Core → Agent
type AgentCommand =
  | { op: "core.install", core, version }
  | { op: "tunnel.create", core, tunnelId, template, params }      // Shared یا Dedicated
  | { op: "tunnel.peerInvite", tunnelId } → { installCommand }
  | { op: "port.open", svcId, ports, proto, toPort, tunnelId }
  | { op: "port.limits", svcId, quotaBytes, usedBaseBytes, expiresAt, rateMbit, maxIps, maxConns }
  | { op: "port.pause" | "port.resume" | "port.close", svcId }
  | { op: "port.resetQuota", svcId }
  | { op: "ports.check", ports, proto } → { free: bool, suggest: number[] }
// Agent → Core: usage.delta (هر ۱۰ ثانیه)، event (quota_hit، expired، tunnel_down/up، peer_joined)، health
```

### ۲.۲ ثنایی (3x-ui) Adapter
- ورود با `POST /{webBasePath}/login` (کوکی در حافظه، لاگین دوباره روی 401) یا توکن API اگر نسخه پشتیبانی کند. کاربر جداگانه‌ی پنل برای Neo Sell.
- `GET /panel/api/inbounds/list` ← همه‌ی اینباندها همراه clientStats ⇒ مصرف همه‌ی کلاینت‌های یک پنل با یک درخواست.
- ساخت ساب لوکیشن: برای هر اینباندِ آن لوکیشن `POST /panel/api/inbounds/addClient` با همان `subId` (`ns-<svc>-<loc>`)، `id` (uuid)، `email` یکتا، `flow`، `totalGB`، `expiryTime`، `limitIp`.
- `updateClient`، `delClient`، `resetClientTraffic` برای تمدید/حذف/ریست.
- **تشخیص نسخه** و پشتیبانی از ساب با ترافیک مشترک نسخه‌ی جدید؛ برای نسخه‌های قدیمی‌تر که این قابلیت را ندارند، ادمین می‌تواند هر لوکیشن را به یک اینباند محدود کند.
- ساخت لینک و JSON اوتباند سمت Neo Sell از روی `streamSettings` (network، security، reality/tls، sni، fp، pbk، sid، path، host…) + `public_host/port` که ادمین تعیین می‌کند.

### ۲.۳ لایسنس (از روز اول، فقط با سرور لایسنس Cloudflare)

همه‌ی تصمیم‌های لایسنس در **`hm-license-manager`** (Cloudflare Worker + D1) گرفته می‌شود؛ Neo Sell فقط توکن امضاشده را می‌گیرد، بررسی می‌کند و طبق آن رفتار می‌کند. الگو دقیقاً همان است که NeoSpoof (`Spoof/license.go` ↔ `src/routes/neospoof-license.ts`) دارد، به‌علاوه‌ی ماژول‌ها و سقف‌ها.

**تغییرات لازم در سرور لایسنس:**

| مورد | توضیح |
|---|---|
| migration `00xx_neosell.sql` | محصول `prod_neosell` (slug: `neosell`، `asset_name: neosell-linux-{arch}`) + جدول `neosell_entitlements(license_id, tier, modules, limits, updated_at)` مثل `neoconnect_entitlements` |
| route `src/routes/neosell-license.ts` | `POST /v1/neosell/license` با بدنه‌ی `{key, nonce, fingerprint, version, os, arch, hostname}` → `{payload, sig, your_ip}` |
| کلید امضا | جفت‌کلید Ed25519 **جدا** با secret `NEOSELL_LICENSE_PRIVATE_KEY`؛ کلید عمومی داخل باینری (مبهم‌شده مثل `licPubB64` در NeoSpoof) |
| قفل دستگاه | همان `claimDevice` / `license_devices` (fingerprint، نه IP) ⇒ تغییر IP سرور مشکلی ندارد؛ انتقال با «آزادسازی دستگاه» در پورتال `/license` |
| پنل ادمین لایسنس | تب Neo Sell: تعیین tier، ماژول‌ها و سقف‌ها برای هر لایسنس؛ پلن‌های فروش Neo Sell در storefront فعلی (`/v1/store/*`) و تریال (`/v1/store/trial`) |

**محتوای توکن (payload امضاشده):**
```jsonc
{
  "key": "HM-XXXX-XXXX-XXXX", "product": "neosell", "fingerprint": "…", "bound_ip": "…",
  "tier": "pro",
  "modules": ["ports", "outbound", "bot", "gateways", "payg", "reseller", "multi_admin", "white_label"],
  "limits": { "max_nodes": 5, "max_xui_panels": 10, "max_active_services": 1000, "max_admins": 3 },
  "license_expires_at": 1798761600,
  "issued_at": 1795000000, "expires_at": 1795172800,   // issued_at + 48h
  "nonce": "…"
}
```

**سمت Neo Sell (`internal/license`):**
- اولین اجرا: ویزارد نصب کلید لایسنس را می‌گیرد؛ بدون لایسنس معتبر، پنل فقط صفحه‌ی فعال‌سازی را نشان می‌دهد.
- **Fingerprint:** sha256 از `/etc/machine-id` | hostname | مسیر نصب (همان روش HMPanel).
- هر ۲۴ ساعت بررسی دوباره با nonce تازه؛ توکن ۴۸ ساعت اعتبار دارد ⇒ قطعی کوتاه سرور لایسنس یا اینترنت بی‌اثر است. توکن رمزنگاری‌شده روی دیسک ذخیره می‌شود.
- **آدرس اصلی و پشتیبان** (`license.hmray.pro` و `license.hmrayserver.com`) + **پراکسی SOCKS/HTTP اختیاری** (برای سرور ایران هنگام اختلال)، مثل NeoSpoof.
- ساعت: محافظت در برابر عقب کشیدن ساعت سیستم (`effectiveNow` = بیشینه‌ی زمان دیده‌شده).
- **ماژول‌ها و سقف‌ها در خود هسته اعمال می‌شوند**: منوی ربات/درگاه/payg/نمایندگی فقط با ماژول فعال؛ افزودن نود یا پنل ثنایی بیشتر از سقف رد می‌شود؛ ساخت سرویس فعال بیشتر از سقف رد می‌شود.
- **Agentها هیچ‌وقت با سرور لایسنس حرف نمی‌زنند** (سرورهای ایران ممکن است به Cloudflare دسترسی نداشته باشند)؛ فقط هسته لایسنس دارد و تعداد Agentها را طبق `max_nodes` کنترل می‌کند.

**رفتار هنگام نامعتبر شدن لایسنس** (پیشنهادی، برای محافظت از مشتری نهایی):

| وضعیت | رفتار |
|---|---|
| معتبر | همه‌چیز طبق ماژول‌ها |
| توکن منقضی ولی هنوز داخل مهلت ۴۸ ساعت | عادی + هشدار به Owner |
| لایسنس منقضی/باطل یا مهلت تمام | **حالت قفل:** فروش، شارژ و ساخت سرویس جدید متوقف؛ پنل ادمین فقط خواندنی + بنر تمدید؛ **سرویس‌های فعلی مشتری‌ها قطع نمی‌شوند** و Agentها مثل قبل لیمیت‌ها را اعمال می‌کنند |
| بعد از N روز در حالت قفل (قابل تعیین از سرور لایسنس) | تمدید/انقضا هم متوقف (اختیاری) |

**ضد دستکاری:** کلید عمومی و آدرس‌ها مبهم‌شده، build با `-trimpath -ldflags="-s -w"` (و garble اختیاری)، بررسی لایسنس در چند نقطه (شروع، هر job فروش، افزودن نود)، توکن امضاشده فقط از حافظه/دیسک رمزنگاری‌شده خوانده می‌شود.

---

## ۳. مصرف دقیق و هشدار

| منبع | روش | فاصله |
|---|---|---|
| Neo Agent (هر سه هسته) | push دلتای counterها + رویداد فوری quota/expire | ۱۰ ثانیه |
| 3x-ui | `inbounds/list` → clientStats، جمع به تفکیک subId | ۶۰ ثانیه |

- ذخیره: دلتاها در حافظه جمع و **هر ۶۰ ثانیه یکجا** در SQLite نوشته می‌شوند (`usage_samples`) ← تجمیع `usage_hourly` و `usage_daily`؛ نمونه‌های خام بعد از ۷ روز پاک. (لیمیت‌ها در Agent و ثنایی اعمال می‌شوند، پس این تأخیر نوشتن روی دقت قطع اثری ندارد.)
- تشخیص ریست شمارنده (مقدار جدید < قبلی).
- **منبع حقیقت قطع:** برای پورت‌ها Agent (کرنل)، برای اوتباند خود پنل ثنایی. Neo Sell هم‌گام می‌کند، نمایش می‌دهد و اگر ناهمسانی دید (Reconciler) اصلاح می‌کند.
- نمایش به مشتری: نوار حجم، دانلود/آپلود جدا، نمودار ۲۴ ساعت و ۳۰ روز، روزهای مانده، IPهای متصل (پورت)، سلامت تانل، **کارت جدا برای هر لوکیشن اوتباند**.
- هشدارها (تلگرام / ایمیل): آستانه‌ها و متن‌ها را ادمین تعیین می‌کند (پیش‌فرض ۵۰/۸۰/۹۵٪، ۳ و ۱ روز مانده، قطع، قطعی تانل).
- واحد GB = 2^30 بایت (قابل تغییر توسط ادمین به 10^9).

---

## ۴. پرداخت و کیف پول

- همه‌ی خریدها از کیف پول. شارژ با: درگاه‌های ایرانی (plugin)، **کارت‌به‌کارت با رسید** و «مبلغ یکتا»، رمزارز (اختیاری)، شارژ دستی ادمین.
- دفتر حساب تغییرناپذیر `wallet_transactions`؛ موجودی کش‌شده با قفل ردیف.
- کسر + ساخت سفارش در یک تراکنش DB؛ شکست provisioning ⇒ برگشت خودکار وجه.
- کد تخفیف، دعوت (Referral)، فاکتور.
- **مدل پس‌پرداخت (Pay-as-you-go)** اگر ادمین فعال کند: کسر ساعتی از کیف پول بر اساس مصرف/زمان؛ با رسیدن موجودی به حد تعیین‌شده هشدار و بعد تعلیق.

---

## ۵. کنترل کامل ادمین

### ۵.۱ موتور قیمت‌گذاری (Pricing Engine)

هر **محصول** (Product) یک **مدل فروش** دارد و قیمتش از **قوانین قیمت** (Price Rules) ساخته می‌شود. همه از پنل ادمین، بدون تغییر کد.

**مدل‌های فروش** (برای هر محصول یکی یا چند تا):

| مدل | توضیح | مثال |
|---|---|---|
| `package` | بسته‌ی ثابت | «۳ پورت، ۲۰۰ گیگ، ۳۰ روز = ۴۵۰ هزار» |
| `builder` | مشتری مقدارها را انتخاب می‌کند، قیمت با فرمول | تعداد پورت × قیمت پورت + گیگ × قیمت گیگ، ضرب در ضریب مدت |
| `per_port` | قیمت جدا برای هر پورت، بر اساس کلاس پورت | پورت 443 = ۱۵۰ هزار، پورت تصادفی = ۸۰ هزار |
| `per_gb` | فقط حجم، بدون محدودیت زمان (یا زمان طولانی) | هر گیگ ۲ هزار تومان |
| `unlimited` | حجم نامحدود با سقف سرعت/IP | ۵۰ مگابیت، ۲ IP، ۳۰ روز |
| `payg` | پس‌پرداخت ساعتی از کیف پول | هر گیگ مصرفی + هزینه‌ی روزانه‌ی پورت |

**ابعاد قیمت** (هر قانون می‌تواند به هر ترکیبی از این‌ها بسته شود):
- لوکیشن / نود / گروه نود (مثلاً سرور مخابرات گران‌تر از ایرانسل)
- هسته‌ی تانل (NeoTunnel / Kariz / Backpack)، پروتکل (tcp/udp/both)، حالت تحویل (Dedicated/Shared)
- **کلاس پورت:** پورت‌های مشخص، بازه‌ها، «پورت‌های ویژه» (80، 443، 2053، 8443…)، پورت تصادفی — هر کلاس قیمت/ضریب خودش
- حجم: قیمت هر گیگ **پلکانی** (۰–۱۰۰ گیگ X، ۱۰۰–۵۰۰ گیگ Y)
- مدت: قیمت روزانه یا **ضریب مدت** (۳۰ روز ×۱، ۹۰ روز ×۲.۷، ۱۸۰ روز ×۵)
- سرعت و سقف IP/اتصال: هزینه‌ی اضافه برای هر پله
- لوکیشن اوتباند: قیمت هر لوکیشن و هر گیگ آن لوکیشن جدا
- **گروه کاربری** (عادی، VIP، نماینده): تخفیف درصدی یا لیست قیمت کاملاً جدا
- **قیمت اختصاصی برای یک کاربر** (override)
- بازه‌ی زمانی (فروش ویژه با تاریخ شروع/پایان)، کد تخفیف
- حداقل/حداکثر هر مقدار در builder (مثلاً حداقل ۱۰ گیگ، حداکثر ۲۰ پورت)
- گرد کردن قیمت (مثلاً به هزار تومان)، ارز (تومان/دلار/تتر) و نرخ تبدیل

**قیمت تمدید و افزودنی** می‌تواند با قیمت خرید اول فرق کند (مثلاً تمدید ۱۰٪ ارزان‌تر).

**پیش‌نمایش:** ادمین هنگام تعریف قانون، ماشین‌حساب «اگر مشتری X بخرد قیمت چقدر می‌شود» با ریز محاسبه دارد؛ مشتری هم در فرم خرید ریز قیمت را می‌بیند. ترتیب اعمال قوانین: پایه ← ابعاد ← گروه کاربری ← override کاربر ← فروش ویژه ← کد تخفیف ← گرد کردن. قیمت نهایی در `orders.price_breakdown` ذخیره می‌شود (تغییر قیمت بعدی روی سفارش‌های قبلی اثر ندارد).

### ۵.۲ تنظیمات محصول و سرویس
- فعال/غیرفعال، نمایش/مخفی، فقط برای گروه‌های خاص، موجودی (حداکثر فروش)، ترتیب و توضیح و عکس.
- لیمیت‌های پیش‌فرض و قابل انتخاب: حجم، مدت، سرعت، IP، اتصال، ریست دوره‌ای حجم.
- کدام هسته‌ها، کدام نودها، کدام حالت تحویل، انتخاب پورت توسط مشتری مجاز است یا نه.
- برای اوتباند: کدام قالب‌ها/لوکیشن‌ها، لوکیشن اجباری/اختیاری، لینک تجمیعی روشن/خاموش.
- حداکثر سرویس فعال برای هر کاربر، تست رایگان (حجم/مدت، یک‌بار برای هر کاربر/تلگرام).

### ۵.۳ نودها و پورت‌ها
- بازه‌های پورت قابل فروش، پورت‌های ممنوع و رزرو، ظرفیت هر نود (حداکثر پورت/سرویس)، وزن برای انتخاب خودکار نود.
- **قالب کانفیگ هر هسته** (ویرایشگر با متغیرها) و نسخه‌ی هسته.
- نگهداری: تخلیه‌ی نود، انتقال سرویس‌ها به نود دیگر، توقف فروش روی یک نود.

### ۵.۴ کنترل روی هر کاربر و هر سرویس
- تغییر دستی هر لیمیت، تمدید/حجم هدیه، ریست مصرف، توقف/فعال‌سازی، تغییر پورت، انتقال، حذف — با ثبت دلیل.
- کیف پول: شارژ/کسر دستی، اعتبار منفی مجاز (سقف بدهی) برای کاربر خاص.
- گروه کاربری، قیمت اختصاصی، مسدودسازی، ورود به‌جای کاربر (impersonate، با لاگ).

### ۵.۵ تنظیمات سراسری
- ثبت‌نام باز/بسته/با دعوت، تأیید ایمیل/شماره، روش‌های ورود.
- مهلت‌ها: قبل از تعلیق، قبل از حذف، انتظار اتصال سرور خارج، سیاست برگشت وجه.
- روش‌های پرداخت، شماره کارت‌ها (چرخشی)، درگاه‌ها، حداقل/حداکثر شارژ.
- همه‌ی **متن‌ها و قالب پیام‌ها** (وب، ربات، اعلان‌ها) و منوهای ربات، قوانین سایت، لوگو و رنگ.
- آستانه‌های هشدار، فاصله‌ی polling، واحد GB.
- feature flags: هر بخش (اوتباند، کارت‌به‌کارت، دعوت، تست رایگان، payg…) روشن/خاموش.

### ۵.۶ نقش‌ها و دسترسی
- **Owner:** دسترسی کامل و بدون محدودیت به همه‌چیز (فقط Owner می‌تواند نقش‌ها و اعتبارنامه‌ی نودها را تغییر دهد).
- نقش‌های سفارشی با دسترسی‌های ریز (`pricing.edit`، `nodes.manage`، `payments.approve`، `users.wallet`، `tickets`…).
- 2FA اجباری برای ادمین‌ها، لاگ ممیزی برای هر تغییر (قبل/بعد).

---

## ۶. ربات تلگرام

**اتصال حساب:** لینک یک‌بارمصرف `t.me/<bot>?start=link_<token>` از پنل؛ ورود وب با Telegram Login؛ ثبت‌نام مستقیم از ربات.

منوی مشتری: 🛒 خرید (با همان مدل‌های فروش و ریز قیمت) • 📦 سرویس‌های من (مصرف هر پورت و **هر لوکیشن**، روزهای مانده، دستور نصب، ساب، QR) • ♻️ تمدید/افزودنی • 💳 کیف پول (درگاه / رسید کارت‌به‌کارت) • 🎟 تخفیف • 👥 دعوت • 🎫 پشتیبانی • 🔔 اعلان‌ها.

منوی ادمین: تأیید/رد رسید، آمار امروز، جست‌وجوی کاربر، شارژ دستی، تمدید سریع، وضعیت نودها، پیام همگانی.

پیاده‌سازی: داخل همان باینری (کتابخانه‌ی Go تلگرام)، long-poll پیش‌فرض یا webhook، **پراکسی SOCKS/HTTP اختیاری** برای سرور ایران، state گفتگو در SQLite، همه‌ی منطق از همان لایه‌ی Core؛ متن و منوها از تنظیمات ادمین. فعال فقط با ماژول لایسنس `bot`.

---

## ۷. مدل داده (SQLite داخلی، WAL، migrationهای embed‌شده)

```
license_state(id=1, key_enc, token_enc, fingerprint, last_ok_at, max_seen_time, status)
jobs(id, kind, payload, run_at, attempts, max_attempts, locked_until, status, last_error)  -- صف داخلی
users(id, email, phone, password_hash, telegram_chat_id, telegram_username, group_id, status,
      referral_code, referred_by, totp_secret, credit_limit, created_at)
user_groups(id, name, discount_pct, price_list_id)
roles(id, name, permissions[]) ; admin_roles(user_id, role_id)
wallets(user_id PK, balance, currency)
wallet_transactions(id, user_id, amount(+/-), type, ref_type, ref_id, balance_after, note, created_by, created_at)
payments(id, user_id, method, amount, status, authority, ref_id, receipt_file, unique_amount, reviewed_by, created_at)

nodes(id, name, kind[tunnel|xui], location, country, public_ip, api_base, credentials_enc,
      agent_token_hash, port_ranges, blocked_ports, reserved_ports, capacity, weight,
      sale_enabled, status, last_seen_at, agent_version)
node_cores(node_id, core[neotunnel|kariz|backpack], version, config_template_id, enabled)
tunnels(id, node_id, core, mode[shared|dedicated], service_id?, remote_ref, peer_status, params_json)
config_templates(id, core, side[server|client], body, variables_json)
outbound_templates(id, node_id, inbound_id, location_id, protocol, network, security,
      public_host, public_port, tag, extra_json, enabled)
locations(id, name, flag, sort)

products(id, type[port|outbound], title, description, sale_models[], enabled, visible_groups[],
      stock, sort, options_json)       -- هسته‌ها، نودها، حالت تحویل، حداقل/حداکثرها، لوکیشن‌ها، تست رایگان…
port_classes(id, name, ports_expr, kind[specific|range|random], sort)
price_rules(id, product_id?, scope_json, component[base|port|gb|day|duration_mult|rate|ip|location|addon|renew],
      tiers_json, value, unit, priority, group_id?, user_id?, valid_from, valid_to, enabled)
addons(id, product_id, kind, amount, enabled)

orders(id, user_id, kind[new|renew|addon], product_id, service_id, selection_json,
      price_breakdown_json, amount, discount_id, status, idempotency_key, created_at)
services(id, user_id, product_id, type, status, period_start, expires_at, sub_token, xray_uuid,
      quota_bytes, used_bytes, rate_mbit, max_ips, max_conns, reset_days, billing[prepaid|payg], created_at)
service_ports(id, service_id, node_id, tunnel_id, ports, proto, to_port, status)
service_locations(id, service_id, location_id, node_id, sub_id, quota_bytes, used_up, used_down,
      expires_at, limit_ip, status)
service_location_clients(id, service_location_id, outbound_template_id, client_email, client_uuid)

usage_samples(service_id, scope_ref, ts, rx_delta, tx_delta)
usage_hourly(service_id, scope_ref, hour, rx, tx) ; usage_daily(...)
counters_snapshot(source_ref, last_rx, last_tx, last_ts)

discounts, tickets, ticket_messages, notifications, telegram_link_tokens,
node_commands(id, node_id, command_id, payload, status, attempts, result, created_at),
message_templates(key, locale, channel, body),
audit_logs(id, actor_id, action, target_type, target_id, before_json, after_json, ip, created_at),
settings(key, value_json)
```

---

## ۸. API (خلاصه)

```
Auth      POST /auth/register | /auth/login | /auth/refresh | /auth/2fa | /auth/telegram
Shop      GET  /products ; POST /pricing/quote (ریز قیمت) ; POST /ports/check
Orders    POST /orders (new|renew|addon، با idempotency key) ; GET /orders
Services  GET  /services ; GET /services/:id ; GET /services/:id/usage?scope=&range=
          GET  /services/:id/clients ; GET /services/:id/configs ; POST /services/:id/rotate-sub
Wallet    GET  /wallet ; POST /wallet/topup ; POST /wallet/receipt ; GET /payments/callback/:gw
Sub       GET  /sub/:token/:location   (ساب هر لوکیشن، با subscription-userinfo همان لوکیشن)
          GET  /sub/:token             (تجمیعی، اگر ادمین روشن کرده) ; ?format=xray برای JSON outbound
Install   GET  /i/:inviteToken         (اسکریپت نصب سمت خارج برای حالت Dedicated)
Agent     WS   /agent/connect
Admin     /admin/{nodes,cores,templates,locations,outbound-templates,products,port-classes,
          price-rules,pricing/simulate,addons,users,groups,roles,orders,services,payments,
          discounts,messages,settings,reports,audit}
```

---

## ۹. جریان‌های کلیدی

**خرید پورت (Dedicated):**
1. مشتری محصول، مدل فروش و مقادیر را انتخاب و پورت‌ها را وارد می‌کند → `/ports/check` + `/pricing/quote`.
2. `POST /orders` → کسر کیف پول + سفارش + `service(provisioning)` در یک تراکنش.
3. انتخاب نود (طبق قوانین ادمین و ظرفیت) → Agent: `tunnel.create(dedicated)` + `tunnel.peerInvite` → سرویس `waiting_peer` → دستور نصب به مشتری.
4. Agent رویداد `peer_joined` → `port.open` + `port.limits` → `active` → اعلان.
5. وصل نشدن در مهلت تعیین‌شده: یادآوری، بعد لغو و برگشت وجه.

**خرید اوتباند مولتی‌لوکیشن:**
1. `POST /orders` → `service` + برای هر لوکیشن انتخاب‌شده یک `service_location` با حجم/انقضای خودش.
2. Job برای هر لوکیشن (موازی، با retry): `addClient` روی همه‌ی اینباندهای آن لوکیشن با `subId` مخصوص آن لوکیشن.
3. یک لوکیشن خطا داد ⇒ بقیه فعال، آن یکی در صف retry؛ اگر نهایتاً نشد ⇒ برگشت وجه همان لوکیشن.
4. تحویل: ساب هر لوکیشن + JSON outbound + QR (+ لینک تجمیعی).

**تمدید / افزودنی:** پورت → Agent `port.limits` (+ `port.resetQuota`)؛ لوکیشن → 3x-ui `updateClient` + `resetClientTraffic` روی همه‌ی کلاینت‌های همان subId.

**انقضا/حذف:** Cron: بعد از مهلت‌های ادمین `suspended` و بعد حذف (پورت آزاد، کلاینت‌ها پاک).

**Reconciler (هر ۵ دقیقه):** وضعیت مطلوب DB در برابر وضعیت واقعی هر Agent و هر پنل ثنایی (پورت/کلاینت گمشده یا اضافه، لیمیت ناهمسان، کلاینت دستی‌ساخته‌شده) ← اصلاح خودکار یا گزارش به ادمین.

---

## ۱۰. امنیت

- اعتبارنامه‌ها AES-256-GCM؛ هرگز در لاگ و پاسخ API.
- Agent: توکن یکتا + HMAC هر پیام + فرمان‌های امضاشده؛ Agent فقط فرمان‌های لیست سفید را اجرا می‌کند (هیچ shell دلخواه).
- رمزها Argon2id، JWT کوتاه‌عمر + refresh چرخشی، 2FA اجباری ادمین، RBAC ریز.
- Rate limit روی ورود، ثبت‌نام، `/ports/check`، `/sub`؛ توکن‌های ساب و دعوت ۲۵۶ بیتی.
- وریفای درگاه سمت سرور، idempotency روی سفارش و callback.
- بکاپ روزانه‌ی رمزنگاری‌شده‌ی DB.

---

## ۱۱. باینری تک‌فایلی: استک و ساختار پروژه

**هدف:** روی سرور فقط یک فایل `neosell` (linux amd64/arm64، `CGO_ENABLED=0`، استاتیک) + یک سرویس systemd. بدون PostgreSQL، Redis، Node، Docker، Nginx یا هیچ پکیج دیگری. هم‌سبک NeoTunnel.

| بخش | انتخاب (همه داخل باینری) |
|---|---|
| زبان | **Go** — هم پنل و هم Agent |
| HTTP | `net/http` استاندارد (router Go 1.22+) |
| DB | **SQLite** با درایور pure Go (`modernc.org/sqlite`)، حالت WAL، migrationهای `go:embed`؛ برای این مقیاس (هزاران کاربر و سرویس) کافی است |
| صف کارها | جدول `jobs` در SQLite + worker pool داخلی (retry با backoff، قفل با `locked_until`) — جای Redis/BullMQ |
| کش و rate limit | در حافظه |
| رابط وب | SPA (React + Vite + Tailwind، RTL، Vazirmatn، تیره/روشن) که **فقط هنگام build** ساخته و با `go:embed` داخل باینری می‌رود؛ فونت‌ها هم embed (بدون CDN، مهم برای ایران) |
| ربات | کتابخانه‌ی Go تلگرام، داخل همان پروسه |
| TLS | ACME خودکار (Let's Encrypt) برای دامنه، یا گواهی خودامضا برای IP؛ یا پشت پراکسی دلخواه ادمین |
| nftables / tc (Agent) | `google/nftables` و netlink — مستقیم از کرنل، بدون فراخوانی برنامه‌ی بیرونی |
| لایسنس | `internal/license` (Ed25519 از کتابخانه‌ی استاندارد) |
| فایل‌ها | رسیدها و فاکتورها در `/var/lib/neosell/files` |

**حالت‌های باینری:**
```
neosell install            # ویزارد: پورت، دامنه/TLS، کاربر ادمین، کلید لایسنس، systemd
neosell                    # اجرای پنل (پیش‌فرض)
neosell agent --join <url> # نصب و اجرا به‌عنوان Agent روی سرور تانل
neosell update             # به‌روزرسانی از GitHub Releases با بررسی checksum و امضا
neosell backup | restore   # فایل پشتیبان (DB + فایل‌ها، رمزنگاری‌شده)
neosell admin reset-password | license status | doctor
```

- **نصب یک‌خطی** (مثل NeoTunnel): `curl -fsSL …/install.sh | sudo bash` ← باینری مناسب معماری را دانلود، checksum را بررسی و `neosell install` را اجرا می‌کند.
- **مسیرها:** `/usr/local/bin/neosell`، `/etc/neosell/config.toml` (فقط چیزهای پایه؛ بقیه‌ی تنظیمات در DB و پنل)، `/var/lib/neosell/`.
- **بکاپ:** SQLite online backup، روزانه، رمزنگاری‌شده، ارسال به تلگرام Owner (اختیاری).
- **به‌روزرسانی بدون قطعی:** جایگزینی اتمی باینری + restart سرویس؛ migrationها خودکار و رو به جلو.

```
neo-sell/
├─ cmd/neosell/           # main: panel | agent | install | update | backup | admin
├─ internal/
│  ├─ app/                # راه‌اندازی، config، systemd
│  ├─ db/                 # SQLite، migrations (embed)، repositoryها
│  ├─ license/            # کلاینت سرور لایسنس، توکن، fingerprint، guard ماژول/سقف
│  ├─ auth/ rbac/ audit/
│  ├─ wallet/ payments/   # gateways/{zarinpal,zibal,…}, card2card, crypto
│  ├─ catalog/ pricing/   # محصولات، کلاس پورت، موتور قیمت (خالص و تست‌پذیر)
│  ├─ orders/ services/
│  ├─ jobs/               # صف داخلی و workerها: provision, usage, reconcile, notify, expire, payg, backup
│  ├─ agentgw/            # سرور WebSocket برای Agentها
│  ├─ xui/                # adapter ثنایی + ساخت لینک و outbound JSON
│  ├─ sub/                # ساب هر لوکیشن و تجمیعی
│  ├─ bot/                # ربات تلگرام
│  ├─ notify/ messages/ settings/
│  └─ httpapi/            # handlerها + سرو کردن SPA embed‌شده
├─ agent/                 # کد حالت agent: drivers/{neotunnel,kariz,backpack}, nft/, tc/, store/, ws/
├─ web/                   # سورس SPA (فقط build-time) → خروجی در internal/httpapi/dist
├─ scripts/ install.sh  build.sh (cross-compile amd64/arm64 + checksum + امضا)
└─ docs/
```

---

## ۱۲. نقشه‌ی راه

| فاز | محتوا | خروجی | تخمین |
|---|---|---|---|
| **۰. پایه + لایسنس** | اسکلت باینری Go، SQLite + migrationها، صف jobs، SPA embed، `install`/systemd، auth، RBAC، لاگ ممیزی، settings؛ **لایسنس کامل:** migration و route `neosell` در `hm-license-manager` + کلید Ed25519 جدا + `internal/license` + guard ماژول/سقف + حالت قفل | `neosell install` روی سرور خام، فعال‌سازی با کلید، پنل ادمین خالی | ۱.۵–۲ هفته |
| **۱. Agent + یک هسته** | Neo Agent: اتصال WS، nftables counter/quota/expiry، tc، driver اول (NeoTunnel، حالت Shared)، تست تداخل با NAT هسته | پورت با لیمیت دقیق از راه Agent | ۲–۳ هفته |
| **۲. فروش پورت + قیمت‌گذاری** | محصولات، موتور قیمت (همه‌ی مدل‌ها جز payg)، کیف پول، کارت‌به‌کارت، سفارش، usage، صفحه‌ی مصرف | اولین فروش واقعی پورت | ۲–۳ هفته |
| **۳. اوتباند** | 3x-ui adapter، لوکیشن‌ها، ساب جدا برای هر لوکیشن، JSON outbound، تمدید/حجم هر لوکیشن | فروش اوتباند مولتی‌لوکیشن | ۲ هفته |
| **۴. ربات تلگرام** | اتصال حساب، خرید، سرویس‌ها، رسید، اعلان‌ها، منوی ادمین | فروش کامل از ربات | ۱–۲ هفته |
| **۵. Dedicated + Kariz + Backpack** | دعوت و اسکریپت نصب سمت خارج، driverهای Kariz و Backpack، قالب‌های کانفیگ | پشتیبانی هر سه هسته | ۲–۳ هفته |
| **۶. رشد** | درگاه‌ها، payg، تخفیف، دعوت، تست رایگان، تیکت، گزارش، broadcast | | ۲ هفته |
| **۷. سخت‌سازی** | reconciler کامل، تست بار، بکاپ، مانیتورینگ | انتشار | ۱–۲ هفته |
| بعداً | نمایندگی (Reseller)، API عمومی، اپ موبایل، انتقال خودکار هنگام خرابی نود | | |

---

## ۱۳. تست

- **Unit:** موتور قیمت (هر مدل × هر بُعد × ترتیب قوانین)، دلتا/ریست شمارنده، لینک و outbound JSON برای هر ترکیب پروتکل.
- **Agent:** تست روی VM واقعی برای هر هسته: دقت شمارش در برابر iperf، قطع دقیق سر quota، سقف سرعت، سقف IP، رفتار بعد از ری‌استارت و قطع ارتباط با Core.
- **Integration:** 3x-ui در Docker روی ماشین تست (فقط محیط تست؛ چند نسخه، با و بدون ساب ترافیک مشترک)، mock درگاه‌ها.
- **E2E:** Playwright برای خرید کامل.
- **آشوب:** قطع Agent وسط provisioning، فرمان تکراری، callback دوباره، قطعی بین‌الملل.
- **لایسنس:** امضای نادرست، nonce تکراری، توکن منقضی، عقب کشیدن ساعت، fingerprint دیگر، سرور لایسنس در دسترس نیست (اصلی و پشتیبان)، کار از پشت پراکسی، رسیدن به هر سقف (نود، سرویس، ادمین)، ورود به حالت قفل و خروج از آن بدون قطع سرویس مشتری‌ها. سمت Worker: تست e2e کنار تست‌های موجود `hm-license-manager`.
- **باینری:** نصب روی Ubuntu 20.04/22.04/24.04 و Debian خام (amd64 و arm64) بدون نصب هیچ پکیج اضافه؛ update و rollback.

---

## ۱۴. سؤال‌های باز

1. **Kariz و Backpack:** فرمت کانفیگ سمت سرور و کلاینت، نحوه‌ی اجرا و اینکه یک پروسه چند مشتری را پشتیبانی می‌کند یا برای هر مشتری پروسه‌ی جدا لازم است.
2. **NeoTunnel driver:** آیا Agent اجازه دارد از API محلی NeoTunnel (با توکن `full` روی localhost) برای ساخت تانل/forward و گرفتن دستور اتصال سرور خارج استفاده کند، یا NeoTunnel را بدون پنل و مستقیم با کانفیگ اجرا کنیم؟
3. کدام نسخه‌ی ثنایی ساب با ترافیک مشترک دارد (حداقل نسخه‌ای که پشتیبانی کنیم)؟
4. میزبانی Core: ایران یا خارج؟ (با پراکسی داخلی برای لایسنس و تلگرام، هر دو ممکن است.)
5. درگاه پرداخت اولیه و ارز.
6. نمایندگی (Reseller) از روز اول یا بعداً؟
7. **لایسنس:** فهرست tierها و ماژول‌ها و سقف‌های پیش‌فرض هر tier؟ تریال داشته باشد (از `/v1/store/trial`) یا نه؟ رفتار حالت قفل همان پیشنهاد بخش ۲.۳ باشد؟
8. **لایسنس:** هر لایسنس روی یک سرور (یک fingerprint) یا چند سرور (`max_server_ips` > 1)؟

</div>

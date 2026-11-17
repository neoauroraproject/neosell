<div dir="rtl">

# Neo Sell

پنل فروش **پورت تانل** (NeoTunnel، Kariz، Backpack) و **اوتباند** (پنل‌های ثنایی / 3x-ui) با کیف پول، قیمت‌گذاری کاملاً قابل تنظیم، ربات تلگرام و مصرف دقیق — همه در **یک فایل اجرایی** بدون هیچ وابستگی.

- برنامه‌ی کامل طراحی: [docs/PLAN.md](docs/PLAN.md)

## نصب پنل

روی یک سرور لینوکس (Ubuntu/Debian، amd64 یا arm64) با root:

</div>

```bash
NEOSELL_URL=https://<آدرس-انتشار> bash <(curl -fsSL https://<آدرس-انتشار>/install.sh) --port 2087
```

<div dir="rtl">

یا دستی: باینری `neosell-linux-<arch>` را در `/usr/local/bin/neosell` بگذارید و اجرا کنید:

</div>

```bash
sudo neosell install -port 2087 -public-url https://panel.example.com -tls self
```

<div dir="rtl">

| گزینه | معنی |
|---|---|
| `-tls self` | گواهی خودامضا (برای IP). Agentها با پین گواهی به آن اعتماد می‌کنند |
| `-tls off` | پشت یک reverse proxy (مثلاً Caddy/Nginx با دامنه) |
| `-tls file -cert … -key …` | گواهی خودتان |
| `-proxy socks5://127.0.0.1:1080` | پراکسی خروجی برای سرور لایسنس، تلگرام و پنل‌های ثنایی (سرور ایران) |

بعد آدرس پنل را باز کنید: حساب **مالک** را بسازید و **کلید لایسنس** را وارد کنید.

## راه‌اندازی فروش (به ترتیب)

1. **تنظیمات** ← «آدرس عمومی پنل» (برای لینک ساب و اسکریپت‌های نصب)، نام فروشگاه، ثبت‌نام، مهلت‌ها.
2. **سرورهای تانل** ← افزودن سرور ایران ← «دستور نصب Agent» را روی همان سرور اجرا کنید. برای NeoTunnel آدرس محلی پنل NeoTunnel و یک توکن API با دسترسی full را وارد کنید.
3. **تانل‌های اشتراکی** (اگر پورت روی سرور خارج خودتان فروخته می‌شود): نام تانل موجود NeoTunnel (مثلاً `t1`)، یا برای Kariz/Backpack پورت اتصال + دستور نصب سمت خارج.
4. **هسته‌های تانل**: برای Kariz و Backpack آدرس دانلود باینری و قالب کانفیگ سمت ایران و خارج را بنویسید.
5. **پنل‌های ثنایی** ← افزودن پنل ← **اوتباندها** ← ثبت اینباندها برای هر لوکیشن.
6. **کلاس‌های پورت** ← **محصولات** ← **قوانین قیمت** ← امتحان در **ماشین‌حساب قیمت**.
7. **کارت‌های واریز** (شارژ کارت‌به‌کارت) و در تنظیمات، **ربات تلگرام** (توکن BotFather).

## دستورها

</div>

```text
neosell [serve] [-config /etc/neosell/config.json]
neosell install ...                         نصب پنل + سرویس systemd
neosell agent join -panel URL -token T      نصب Agent روی سرور تانل (معمولاً از راه agent.sh)
neosell agent run
neosell backup -out /root/neosell.db        پشتیبان سازگار از دیتابیس (secret.key را هم نگه دارید)
neosell admin reset-password <email> <pass>
neosell license status|recheck
neosell version
```

<div dir="rtl">

## ساخت از سورس

نیازمندی فقط Go 1.24+ است (رابط وب بدون build و داخل باینری است):

</div>

```bash
./scripts/build.sh        # vet + test + dist/neosell-linux-{amd64,arm64} + SHA256SUMS
go test ./...
```

<div dir="rtl">

## ساختار

| مسیر | کار |
|---|---|
| `cmd/neosell` | نقطه‌ی ورود: پنل، Agent، نصب، پشتیبان |
| `internal/license` | کلاینت سرور لایسنس (Ed25519، قفل دستگاه، ماژول‌ها و سقف‌ها) |
| `internal/pricing` | موتور قیمت (خالص، تست‌شده) |
| `internal/catalog` | محصولات، کلاس پورت، قوانین قیمت، تخفیف |
| `internal/wallet`, `internal/payments` | دفتر کیف پول، شارژ کارت‌به‌کارت |
| `internal/services` | سفارش، سرویس، تمدید، افزودنی، مصرف، چرخه‌ی عمر، ساب |
| `internal/agent`, `internal/agentproto`, `internal/agentgw` | Neo Agent (nftables، درایور هسته‌ها) و اتصال آن به پنل |
| `internal/xui` | کلاینت 3x-ui و ساخت لینک/اوتباند xray |
| `internal/bot`, `internal/telegram`, `internal/notify` | ربات تلگرام و اعلان‌ها |
| `internal/httpapi`, `internal/webui` | API و رابط وب (embed) |

## سرور لایسنس

مسیر `POST /v1/neosell/license` در `hm-license-manager` (برنچ `feat/neosell-license`). کلید خصوصی امضا باید به‌صورت secret روی Worker قرار بگیرد:

</div>

```bash
npx wrangler secret put NEOSELL_LICENSE_PRIVATE_KEY
npx wrangler d1 migrations apply hm-license-db --remote
```

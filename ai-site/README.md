# ai.mmodiary.com — bản gốc Kiro-Go + bot Telegram

Site độc lập với api.mmodiary.com. Nhân `kiro-go` giữ **nguyên bản gốc** từ
`Quorinex/Kiro-Go`, không sửa một dòng.

## Cách nó giữ được "bản gốc"

```
ai.mmodiary.com/
  kiro-go-upstream/    <- git clone ban goc, KHONG SUA -> git pull luc nao cung duoc
  keyadmin/            <- sidecar, copy tu fork
  telegram-bot/        <- bot, copy tu fork
  scripts/check-key-expiry.sh
  data/config.json     <- rieng, khong dung chung site cu
```

Sidecar nói chuyện với nhân qua **HTTP admin API**, không patch vào nó. Vì thế
`git pull` từ upstream không bao giờ xung đột.

Tôi đã kiểm trực tiếp bản gốc: nó **có đủ** 4 endpoint mà `keyadmin` cần
(`GET/POST/PUT/DELETE /admin/api/api-keys`) và xác thực bằng `X-Admin-Password`
đúng như `keyadmin` gửi. Nên bot chạy được với bản gốc mà không sửa gì.

---

## Ba điều cần biết trước

**1. Bản gốc không hiểu "hết hạn".**

Hạn dùng được mã hoá vào **tên key** (`#exp=<unix>`, `#pause=<giây>`). Bản gốc coi
tên key là chuỗi thường. Thứ thực thi hết hạn là cron `check-key-expiry.sh`: nó thấy
`#exp` đã qua thì `PUT enabled=false`.

> [!WARNING]
> Thiếu cron này thì `/newkey khachA 720` và `/addhours` chỉ là **trang trí** — bot
> hiển thị hạn đúng, nhưng key **không bao giờ hết hiệu lực thật**. Khách hết hạn
> vẫn dùng vô thời hạn. `setup-ai-site.sh` tự đặt cron này, đừng xoá.

**2. Bot in API key ra chat Telegram.** Telegram lưu tin nhắn trên server họ, và ai
mở được điện thoại bạn thì thấy key.

**3. Bản gốc thiếu 3 bản sửa mà api.mmodiary.com đang có:**

| Thiếu | Hệ quả |
|---|---|
| Ngân sách 10 phút/request | Khách chờ tới **36+ phút** rồi client tự bỏ (đã xảy ra thật) |
| `superfluous WriteHeader` | Khách nhận stream cắt giữa thay vì lỗi sạch |
| Watchdog cắt stream lành | Request context lớn bị cắt oan |

Bản gốc **có** retry cùng endpoint (`a18015d`) vì ta port từ họ. Ba cái trên là của
riêng fork. Nếu site mới gặp các triệu chứng đó, nguyên nhân nằm ở đây.

---

## Dựng site

### 1. Lấy 2 thông tin Telegram

- **Token bot:** chat `@BotFather` → `/newbot` → copy token
- **Chat ID:** chat `@userinfobot` → nó trả `Id: 123456789`

### 2. Tạo thư mục site

```bash
mkdir -p ~/ai.mmodiary.com && cd ~/ai.mmodiary.com
# copy 4 file nay tu repo fork (thu muc ai-site/):
#   docker-compose.yml  .env.example  setup-ai-site.sh  README.md
cp ~/api.mmodiary.com/ai-site/* .
```

### 3. Chạy setup

```bash
bash setup-ai-site.sh ~/api.mmodiary.com
```

Lần đầu nó sẽ sinh `ADMIN_PASSWORD` (in ra **một lần** — lưu lại) rồi dừng, yêu cầu
bạn điền thông tin Telegram. Điền vào `.env`:

```bash
nano .env    # dien TELEGRAM_BOT_TOKEN va TELEGRAM_ADMIN_IDS
bash setup-ai-site.sh ~/api.mmodiary.com
```

Script idempotent — chạy lại an toàn, không ghi đè `.env` đang dùng.

Cuối cùng nó tự kiểm và in:

```
[OK]   admin API ban goc tra 200 - keyadmin dung duoc
[OK]   keyadmin dang chay
[OK]   telegram-bot dang chay
```

Dòng đầu là phép thử quan trọng nhất: nó chứng minh bản gốc thật sự tương thích, chứ
không chỉ theo tôi đọc code.

### 4. Reverse proxy

```bash
sudo bash ~/api.mmodiary.com/scripts/setup-ols-proxy.sh ai.mmodiary.com 8090
```

### 5. Thêm tài khoản Kiro

Dùng **account riêng**, không dùng chung site cũ — dùng chung sẽ tranh cùng quota và
throttle một bên làm bên kia cùng lỗi.

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\add-account.ps1 -Vps <IP> -Port 8090
```

---

## Xác minh

Trong Telegram:

```
/help
/newkey test 24
/info test
```

Thử key thật:

```bash
curl -s https://ai.mmodiary.com/v1/chat/completions \
  -H "Authorization: Bearer <key>" -H "Content-Type: application/json" \
  -d '{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}' | head -20
```

Bốn phép thử:

| Thử | Mong đợi |
|---|---|
| `/pause test` → gọi API | Bị từ chối |
| `/resume test` → gọi lại | Chạy |
| Người khác chat với bot | **Không** phản hồi |
| `/v1/models \| grep -ci grok` | `0` |

Phép thử thứ ba quan trọng nhất. Bot im lặng có chủ ý: trả lời sẽ xác nhận cho người
lạ rằng bot này quản key. Kiểm log để thấy nó có ghi nhận:

```bash
docker logs aikiro-telegram-bot | grep unauthorised
```

Kiểm cron hết hạn thật sự hoạt động — đừng tin nó chạy mà chưa thử:

```bash
# Tao key han 1 phut, doi, chay cron tay, xem enabled co thanh false
# (trong Telegram: /newkey thu-het-han 0 roi /addhours thu-het-han 1)
bash scripts/check-key-expiry.sh
```

## Cập nhật sau này

Nhân bản gốc:

```bash
cd ~/ai.mmodiary.com && bash setup-ai-site.sh ~/api.mmodiary.com
```

Nó fetch lại upstream, copy lại sidecar, build lại. `.env` và `data/` giữ nguyên.

## Bảng lệnh bot

| Lệnh | Việc |
|---|---|
| `/newkey <tên> <giờ> [số lượng]` | Tạo key |
| `/addhours <key\|tên> <giờ>` | Cộng giờ (số âm để trừ) |
| `/pause <key\|tên>` | Tạm dừng, giữ thời gian còn lại |
| `/resume <key\|tên>` | Tiếp tục |
| `/info <key\|tên>` | Trạng thái, hạn, lượng dùng |
| `/list [active\|paused\|expired]` | Danh sách |

Dùng ID hoặc một phần tên. Tên khớp nhiều key thì bot **từ chối và liệt kê** thay vì
đoán — các lệnh này ảnh hưởng key khách đang trả tiền.

## Gỡ lỗi

| Triệu chứng | Nguyên nhân |
|---|---|
| `admin API tra 401` | `ADMIN_PASSWORD` trong `.env` không khớp container. `docker compose up -d` lại. |
| `admin API tra 000` | kiro-go chưa chạy. `docker logs aikiro-go --tail 30`. |
| Bot không khởi động, log `TELEGRAM_ADMIN_IDS is required` | Chưa điền chat ID. Cố tình chặn. |
| Bot chạy nhưng im lặng | Chat ID của bạn không trong allowlist. Xem `grep unauthorised` để thấy ID thật. |
| Key hết hạn vẫn dùng được | Cron chưa chạy. `crontab -l \| grep expiry`. |
| Khách chờ rất lâu rồi bị ngắt | Bản gốc thiếu ngân sách 10 phút. Xem mục "Ba điều cần biết". |

# keyadmin API — Hướng dẫn đấu nối (cho bot Telegram)

Service `keyadmin` là API **có quyền** để tạo và quản lý API key của Kiro-Go từ
bên ngoài (ví dụ bot Telegram). Khác với `keycheck` (chỉ đọc, công khai, không mật
khẩu), `keyadmin` giữ `ADMIN_PASSWORD` và gọi admin API của kiro-go, nên **bắt
buộc xác thực bằng bearer token**.

---

## 1. Thông tin kết nối

| Mục | Giá trị |
|---|---|
| Base URL (local) | `http://127.0.0.1:8082` |
| Base URL (qua reverse proxy) | `https://your-domain/keyadmin` (tùy cấu hình Nginx) |
| Cổng host | `KEYADMIN_PORT` trong [.env](../.env) (mặc định `8082`) |
| Chỉ bind | `127.0.0.1` — không mở ra Internet trực tiếp |

Bot phải chạy **cùng máy** (gọi `127.0.0.1:8082`) hoặc đi qua reverse proxy có
HTTPS. Không expose thẳng cổng 8082 ra ngoài.

---

## 2. Xác thực

Mọi endpoint (trừ `/healthz`) yêu cầu header:

```
Authorization: Bearer <KEYADMIN_TOKEN>
```

Token lấy từ `KEYADMIN_TOKEN` trong [.env](../.env). Sinh token mạnh mới cho mỗi
triển khai thật:

```bash
openssl rand -hex 32
```

Thiếu hoặc sai token → trả `401 Unauthorized`. So khớp token dùng constant-time.

---

## 3. Danh sách endpoint

Tất cả nhận/trả JSON (`Content-Type: application/json`).

### GET `/api/keys` — liệt kê key

Trả về danh sách key kèm trạng thái hạn dùng.

```bash
curl http://127.0.0.1:8082/api/keys \
  -H "Authorization: Bearer $TOKEN"
```

```json
{
  "keys": [
    {
      "id": "f69a6f26-...",
      "name": "test-bot",
      "enabled": true,
      "tokenLimit": 1000000,
      "creditLimit": 50,
      "tokensUsed": 0,
      "creditsUsed": 0,
      "expiry": { "mode": "active", "expiresAt": 1783526698, "secondsLeft": 7182 }
    }
  ]
}
```

`expiry.mode` là một trong: `none` (vĩnh viễn), `active` (đang đếm), `paused` (tạm dừng).

### POST `/api/keys/create` — tạo key

```bash
curl -X POST http://127.0.0.1:8082/api/keys/create \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"khach-a","count":1,"tokenLimit":1000000,"creditLimit":50,"days":30,"hours":0}'
```

| Trường | Kiểu | Ý nghĩa |
|---|---|---|
| `name` | string | Tên gốc. Nếu `count > 1`, tự thêm hậu tố `-1`, `-2`, ... |
| `count` | int | Số key cần tạo (mặc định 1, tối đa 500) |
| `tokenLimit` | int | Giới hạn token (0 = không giới hạn) |
| `creditLimit` | float | Giới hạn credit (0 = không giới hạn) |
| `days` | int | Số ngày hiệu lực (cộng dồn với `hours`) |
| `hours` | int | Số giờ hiệu lực. `days=0, hours=0` → key vĩnh viễn |

```json
{
  "created": [
    { "name": "khach-a #exp=1786118698", "key": "sk-543d2ac1..." }
  ],
  "errors": 0
}
```

> **Lưu ý:** trường `key` (key gốc) chỉ trả về **một lần duy nhất** lúc tạo. Bot
> phải gửi ngay cho khách / lưu lại. Không thể lấy lại key gốc sau này.

### POST `/api/keys/pause` — tạm dừng đếm giờ

Đóng băng thời gian còn lại. Key **không hết hạn** khi đang tạm dừng (cron bỏ qua).

```bash
curl -X POST http://127.0.0.1:8082/api/keys/pause \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"id":"f69a6f26-..."}'
```

### POST `/api/keys/resume` — tiếp tục đếm giờ

Kích hoạt lại đồng hồ, đếm tiếp từ thời gian đã đóng băng.

```bash
curl -X POST http://127.0.0.1:8082/api/keys/resume \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"id":"f69a6f26-..."}'
```

### POST `/api/keys/add-hours` — cộng/trừ giờ

`hours` dương = cộng thêm, âm = trừ bớt. Áp dụng cả khi key đang `active` hoặc
`paused`. Nếu key đang `none` (vĩnh viễn), thao tác này bắt đầu đếm từ bây giờ.

```bash
curl -X POST http://127.0.0.1:8082/api/keys/add-hours \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"id":"f69a6f26-...","hours":24}'
```

### POST `/api/keys/enable` — bật/tắt key

```bash
curl -X POST http://127.0.0.1:8082/api/keys/enable \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"id":"f69a6f26-...","enabled":false}'
```

### GET `/healthz` — kiểm tra sống (không cần token)

```bash
curl http://127.0.0.1:8082/healthz   # -> ok
```

---

## 4. Cơ chế hạn dùng (đồng bộ bot ↔ web admin ↔ cron)

Hạn dùng được mã hóa trong **tên key**, nên bot, trang admin và cron đều đọc cùng
một định dạng:

| Marker trong tên | Ý nghĩa |
|---|---|
| `#exp=<unix>` | Còn hạn, hết hạn vào thời điểm unix (giây) |
| `#pause=<giây>` | Đồng hồ tạm dừng, còn lại bấy nhiêu giây |
| `#exp=YYYY-MM-DD` | Định dạng cũ (vẫn đọc được, hết hạn cuối ngày đó) |
| (không marker) | Vĩnh viễn |

Bot **không cần** tự xử lý marker — chỉ gọi endpoint, service tự tính. Cron
[check-key-expiry.sh](../scripts/check-key-expiry.sh) sẽ tự **tắt** (không xóa)
key hết hạn và bỏ qua key đang tạm dừng.

---

## 5. Ví dụ bot Telegram (Python)

Đây là ví dụ tối giản dùng `python-telegram-bot`. Bot chỉ là client HTTP gọi
keyadmin — mọi logic key nằm ở service.

```python
import os, requests
from telegram import Update
from telegram.ext import Application, CommandHandler, ContextTypes

KEYADMIN = os.environ["KEYADMIN_URL"]      # http://127.0.0.1:8082
TOKEN    = os.environ["KEYADMIN_TOKEN"]    # bearer token
ADMIN_IDS = {int(x) for x in os.environ["TELEGRAM_ADMINS"].split(",")}

HEADERS = {"Authorization": f"Bearer {TOKEN}", "Content-Type": "application/json"}

def is_admin(update: Update) -> bool:
    return update.effective_user and update.effective_user.id in ADMIN_IDS

# /taokey <ten> <token_limit> <credit_limit> <so_ngay>
async def create_key(update: Update, ctx: ContextTypes.DEFAULT_TYPE):
    if not is_admin(update):
        return await update.message.reply_text("Không có quyền.")
    try:
        name, tok, credit, days = ctx.args
    except ValueError:
        return await update.message.reply_text(
            "Cú pháp: /taokey <tên> <token_limit> <credit_limit> <số_ngày>")

    r = requests.post(f"{KEYADMIN}/api/keys/create", headers=HEADERS, json={
        "name": name, "count": 1,
        "tokenLimit": int(tok), "creditLimit": float(credit),
        "days": int(days), "hours": 0,
    }, timeout=20)
    r.raise_for_status()
    created = r.json()["created"]
    if not created:
        return await update.message.reply_text("Tạo key thất bại.")
    k = created[0]
    await update.message.reply_text(f"Đã tạo key:\n`{k['key']}`", parse_mode="Markdown")

# /congio <id> <so_gio>
async def add_hours(update: Update, ctx: ContextTypes.DEFAULT_TYPE):
    if not is_admin(update):
        return await update.message.reply_text("Không có quyền.")
    key_id, hours = ctx.args
    r = requests.post(f"{KEYADMIN}/api/keys/add-hours", headers=HEADERS,
                      json={"id": key_id, "hours": int(hours)}, timeout=20)
    r.raise_for_status()
    st = r.json()["expiry"]
    await update.message.reply_text(
        f"OK. Trạng thái: {st['mode']}, còn {st['secondsLeft']} giây.")

# /tamdung <id>  và  /tieptuc <id>
async def pause(update: Update, ctx: ContextTypes.DEFAULT_TYPE):
    if not is_admin(update):
        return await update.message.reply_text("Không có quyền.")
    requests.post(f"{KEYADMIN}/api/keys/pause", headers=HEADERS,
                  json={"id": ctx.args[0]}, timeout=20).raise_for_status()
    await update.message.reply_text("Đã tạm dừng đếm giờ.")

async def resume(update: Update, ctx: ContextTypes.DEFAULT_TYPE):
    if not is_admin(update):
        return await update.message.reply_text("Không có quyền.")
    requests.post(f"{KEYADMIN}/api/keys/resume", headers=HEADERS,
                  json={"id": ctx.args[0]}, timeout=20).raise_for_status()
    await update.message.reply_text("Đã tiếp tục đếm giờ.")

def main():
    app = Application.builder().token(os.environ["BOT_TOKEN"]).build()
    app.add_handler(CommandHandler("taokey", create_key))
    app.add_handler(CommandHandler("congio", add_hours))
    app.add_handler(CommandHandler("tamdung", pause))
    app.add_handler(CommandHandler("tieptuc", resume))
    app.run_polling()

if __name__ == "__main__":
    main()
```

Cần: `pip install python-telegram-bot requests`. Đặt các biến môi trường
`BOT_TOKEN`, `KEYADMIN_URL`, `KEYADMIN_TOKEN`, `TELEGRAM_ADMINS` (danh sách user
ID Telegram được phép, cách nhau bằng dấu phẩy).

---

## 6. Lưu ý bảo mật

- **Chỉ bind localhost.** Không mở cổng 8082 ra Internet. Nếu bot chạy máy khác,
  đi qua reverse proxy HTTPS và vẫn gửi kèm bearer token.
- **Giới hạn quyền trong bot.** keyadmin không phân quyền theo người dùng — bất kỳ
  ai có token đều tạo/sửa được key. Bot phải tự kiểm tra Telegram user ID (xem
  `is_admin` ở trên) trước khi gọi API.
- **Token là bí mật.** Ai có `KEYADMIN_TOKEN` là có toàn quyền tạo key. Đừng commit
  token thật; [.env](../.env) đã được gitignore.
- **Key gốc chỉ hiện một lần.** Endpoint create trả key gốc duy nhất lúc tạo — lưu
  lại ngay.

---

## 7. Bảng lỗi thường gặp

| HTTP | Nguyên nhân |
|---|---|
| `401` | Thiếu/sai bearer token |
| `400` | Body JSON sai, thiếu `id`, hoặc pause key không có hạn |
| `404` | Không tìm thấy key theo `id` |
| `502` | keyadmin không gọi được admin API của kiro-go (kiểm tra `kiro-go` còn chạy) |

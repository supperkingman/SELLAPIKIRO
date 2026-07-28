# Dựng ai.mmodiary.com — Kiro thuần + bot Telegram

Site thứ hai trên cùng VPS với api.mmodiary.com. Chỉ luồng Kiro, quản key qua Telegram.

## Trước khi bắt đầu: 3 điều cần biết

**1. Bot in API key ra chat Telegram.** Telegram lưu tin nhắn trên server của họ, và
ai mở được điện thoại bạn thì thấy key. Đây là đánh đổi vốn có của việc quản key qua
chat, không phải lỗi cài đặt.

**2. Không dùng chung tài khoản Kiro với site cũ.** Hai site dùng chung sẽ tranh cùng
quota, và khi upstream throttle một account thì cả hai site cùng lỗi.

**3. Dùng fork, không dùng upstream gốc.** Fork `supperkingman/SELLAPIKIRO` chứa các
bản sửa không có ở upstream: watchdog cắt stream lành, `superfluous WriteHeader`,
ngân sách 10 phút cho một request, retry cùng endpoint. Dùng upstream gốc là gặp lại
đúng những lỗi đó.

---

## Bước 1 — Lấy 2 thông tin từ Telegram

**Token bot:** chat với `@BotFather` → `/newbot` → đặt tên → copy token.

**Chat ID của bạn:** chat với `@userinfobot` → nó trả về `Id: 123456789`.

> [!IMPORTANT]
> Chat ID này là thứ **duy nhất** ngăn người lạ tạo key. Username bot ai cũng tìm
> được và ai cũng chat được với nó. Bỏ trống thì bot từ chối khởi động — cố tình như
> vậy, vì bot trả lời mọi người còn tệ hơn bot không chạy.

## Bước 2 — FlashPanel: tạo site

- Thêm tên miền `ai.mmodiary.com`, bật SSL.
- Deploy from GitHub: repo `supperkingman/SELLAPIKIRO`, branch `main`.
- Post-deploy command: `bash deploy-hook.sh`
- Deploy lần đầu để clone code.

## Bước 3 — Tạo `.env` với cổng riêng

api.mmodiary.com đang dùng 8080–8083, nên site này dùng 8090–8093:

```bash
cd ~/ai.mmodiary.com
cp .env.example .env
nano .env
```

Đặt:

```ini
SITE_NAME=aikiro
KIRO_PORT=8090
KEYCHECK_PORT=8091
KEYADMIN_PORT=8092
STOREFRONT_PORT=8093

# Sinh 2 mật khẩu riêng, KHÔNG dùng lại của site cũ:
#   openssl rand -hex 32
ADMIN_PASSWORD=<sinh moi>
KEYADMIN_TOKEN=<sinh moi>

TELEGRAM_BOT_TOKEN=<token tu BotFather>
TELEGRAM_ADMIN_IDS=<chat ID cua ban>
SITE_LABEL=ai.mmodiary.com
```

## Bước 4 — Bootstrap + bật bot

```bash
sudo bash scripts/bootstrap.sh ai.mmodiary.com
docker compose --profile bot up -d
```

`--profile bot` là bắt buộc. Không có nó thì bot không chạy — chủ ý để site cũ không
bị ảnh hưởng khi pull cùng compose file.

Kiểm tra bot nhận token và thấy keyadmin:

```bash
docker logs aikiro-telegram-bot --tail 20
```

Mong đợi: `running as @tenbot for site "ai.mmodiary.com", 1 admin(s) allowed`.

## Bước 5 — Thêm tài khoản Kiro

Từ máy Windows, sau khi đăng nhập Kiro IDE:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\add-account.ps1 -Vps <IP_VPS> -Port 8090
```

## Bước 6 — Xác minh

Trong Telegram, gửi cho bot:

```
/help
/newkey test 24
/info test
/pause test
/resume test
```

Rồi thử key thật:

```bash
curl -s https://ai.mmodiary.com/v1/chat/completions \
  -H "Authorization: Bearer <key vua tao>" \
  -H "Content-Type: application/json" \
  -d '{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}' | head -20
```

**Ba phép thử quan trọng:**

| Thử | Mong đợi |
|---|---|
| `/pause test` rồi gọi API | Bị từ chối |
| `/resume test` rồi gọi lại | Chạy bình thường |
| Nhờ người khác chat với bot | **Không** phản hồi gì |

Phép thử thứ ba là quan trọng nhất. Bot im lặng có chủ ý: trả lời sẽ xác nhận cho
người lạ rằng bot đang quản key. Kiểm tra log để thấy nó đã ghi nhận:

```bash
docker logs aikiro-telegram-bot | grep unauthorised
```

Xác nhận không lộ Grok/Codex (site này không cấu hình account cho chúng, nên cascade
không bao giờ chạm tới):

```bash
curl -s https://ai.mmodiary.com/v1/models | grep -ciE "grok|gpt|codex"
```

Mong đợi: `0`.

---

## Bảng lệnh bot

| Lệnh | Việc |
|---|---|
| `/newkey <tên> <giờ> [số lượng]` | Tạo key |
| `/addhours <key\|tên> <giờ>` | Cộng giờ (số âm để trừ) |
| `/pause <key\|tên>` | Tạm dừng, giữ thời gian còn lại |
| `/resume <key\|tên>` | Tiếp tục, tính hạn từ bây giờ |
| `/info <key\|tên>` | Trạng thái, hạn, lượng dùng |
| `/list [active\|paused\|expired]` | Danh sách |

Dùng ID key hoặc một phần tên. Nếu tên khớp nhiều key, bot **từ chối** và liệt kê ra
thay vì đoán — các lệnh này ảnh hưởng key khách đang trả tiền, nên tạm dừng sai người
tệ hơn là hỏi lại.

## Gỡ lỗi

| Triệu chứng | Nguyên nhân |
|---|---|
| Bot không khởi động, log báo `TELEGRAM_ADMIN_IDS is required` | Chưa đặt chat ID. Cố tình chặn. |
| Bot chạy nhưng không trả lời | Chat ID của bạn không nằm trong allowlist. Xem `docker logs ... \| grep unauthorised` để thấy ID thật. |
| `telegram rejected the token` | Token sai hoặc đã bị BotFather thu hồi. |
| `keyadmin not reachable` | Bình thường lúc mới khởi động, bot tự phục hồi. Kéo dài thì kiểm `docker ps`. |
| Bot không xuất hiện trong `docker ps` | Thiếu `--profile bot`. |

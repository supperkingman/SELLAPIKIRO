# Hệ thống bán API — api.mmodiary.com (Kiro-Go)

Triển khai [Kiro-Go](https://github.com/Quorinex/Kiro-Go) lên VPS quản lý bằng **FlashPanel**, DNS qua **Cloudflare**, **auto-deploy từ GitHub**.

Kiro-Go biến tài khoản Kiro thành API tương thích OpenAI/Anthropic. Khách gọi API bằng `Authorization: Bearer <key>`; key do bạn tạo trong trang quản trị.

## Kiến trúc

```
Khách ──HTTPS──> Cloudflare ──> FlashPanel Nginx (SSL + reverse proxy)
                                      │
                                      ▼  http://127.0.0.1:8080
                               Kiro-Go (Docker, chỉ bind localhost)
                                      │
                                      ▼
                               data/config.json  (tài khoản Kiro + API key)
```

- **Cloudflare**: DNS cho `api.mmodiary.com`.
- **FlashPanel Nginx**: tự cấp SSL (Let's Encrypt) + reverse proxy về container.
- **Kiro-Go**: chạy Docker, chỉ bind `127.0.0.1:8080` (không lộ ra internet).
- **GitHub auto-deploy**: mỗi lần push, FlashPanel pull code rồi chạy `deploy-hook.sh`.

---

## Bước 1 — Cloudflare DNS

Thêm bản ghi:

| Type | Name | Content    | Proxy status        |
|------|------|------------|---------------------|
| A    | api  | `<IP_VPS>` | DNS only (xám) lúc đầu |

> Để **DNS only** khi cấp SSL lần đầu trong FlashPanel. Cấp xong có thể bật **Proxied** (cam). Nếu bật proxy, đặt SSL/TLS mode ở Cloudflare = **Full (strict)**.

## Bước 2 — Tạo site trong FlashPanel

1. Tạo site mới với domain `api.mmodiary.com`.
2. Cấp **SSL** (Let's Encrypt) cho site — FlashPanel lo phần này.
3. Ghi nhớ thư mục gốc của site (vd `/home/flashvps/api.mmodiary.com` hoặc tương tự).

## Bước 3 — Kết nối GitHub auto-deploy

1. Trong FlashPanel, mở site → mục **Git Deployment** (hoặc "Deploy from Git").
2. Repository: `https://github.com/supperkingman/kiro-sell-mmodiary`
3. Branch: `master`
4. **Deploy script / Post-deploy command**:
   ```bash
   bash deploy-hook.sh
   ```
5. Bật **Auto Deploy** (webhook) để mỗi lần push GitHub tự pull + deploy.

> FlashPanel sẽ clone repo vào thư mục site. `deploy-hook.sh` sẽ chạy `docker compose pull && up -d`.

## Bước 4 — Tạo file .env trên VPS (1 lần)

File `.env` chứa secret nên KHÔNG nằm trong git. SSH vào VPS, vào thư mục site:

```bash
cd /đường-dẫn-site    # nơi FlashPanel clone repo
cp .env.example .env
nano .env             # đặt ADMIN_PASSWORD mạnh
```

## Bước 5 — Cài Docker (nếu VPS chưa có)

```bash
curl -fsSL https://get.docker.com | sh
sudo apt-get install -y docker-compose-plugin
# Cho user của FlashPanel chạy docker không cần sudo:
sudo usermod -aG docker $USER     # đăng xuất/đăng nhập lại để có hiệu lực
```

## Bước 6 — Chạy lần đầu

Trigger deploy từ FlashPanel (hoặc push 1 commit), hoặc chạy tay:

```bash
cd /đường-dẫn-site
bash deploy-hook.sh
```

Kiểm tra:

```bash
docker compose ps                       # kiro-go phải Up
curl -I http://127.0.0.1:8080/admin     # trả 200/302 trên VPS
curl -I https://api.mmodiary.com/admin  # qua domain, cert hợp lệ
```

## Bước 7 — Cấu hình API trong panel

1. Mở `https://api.mmodiary.com/admin`, đăng nhập bằng `ADMIN_PASSWORD`.
2. **Import tài khoản Kiro** (JSON bạn đã có) vào pool.
3. **Tạo API key** cho khách (đây là phần check key — Kiro-Go xác thực `Authorization: Bearer <key>`).
4. **Export config** để backup, hoặc copy `data/config.json`.

---

## Khách gọi API

Base URL: `https://api.mmodiary.com`

```bash
# OpenAI-compatible
curl https://api.mmodiary.com/v1/chat/completions \
  -H "Authorization: Bearer <API_KEY>" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"Xin chào"}]}'

# Anthropic-compatible
curl https://api.mmodiary.com/v1/messages \
  -H "Authorization: Bearer <API_KEY>" \
  -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{"model":"claude-sonnet-4.5","max_tokens":1024,"messages":[{"role":"user","content":"Xin chào"}]}'
```

## Kiểm tra key check

```bash
# Key đúng -> 200
curl -s -o /dev/null -w "%{http_code}\n" https://api.mmodiary.com/v1/chat/completions \
  -H "Authorization: Bearer <API_KEY_THAT>" -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"ping"}]}'

# Key sai -> 401
curl -s -o /dev/null -w "%{http_code}\n" https://api.mmodiary.com/v1/chat/completions \
  -H "Authorization: Bearer sai-key" -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"ping"}]}'
```

## Trang kiểm tra key cho khách (có hiển thị usage)

Khách kiểm tra key tại: `https://api.mmodiary.com/check-key`

Trang này do **sidecar `keycheck`** (Go, trong thư mục `keycheck/`) phục vụ. Khách dán key,
trang gọi `POST /api/check-key` và hiển thị:
- Key hợp lệ / không hợp lệ
- Token đã dùng / còn lại (nếu key có giới hạn token)
- Credit đã dùng / còn lại (nếu key có giới hạn credit)
- Số request đã gọi

Khách **KHÔNG cần** mật khẩu admin. Sidecar đọc thẳng `data/config.json` (mount read-only)
để xác thực key — **không tốn token, không chạm pool**.

### Cơ chế chống hack / dò key
- Rate limit theo IP (token bucket ~10 lượt/phút)
- Tự khóa IP 15 phút sau 8 lần key sai liên tục (chống quét key)
- So sánh key constant-time + chuẩn hóa thời gian phản hồi (chống timing attack)
- Validate định dạng key, thông điệp lỗi chung (không rò rỉ), không log key đầy đủ

> [!IMPORTANT]
> Nginx phải route `/check-key` và `/api/check-key` sang sidecar (cổng 8081) và truyền
> header `X-Real-IP` để rate-limit theo IP thật. Xem `nginx-reverse-proxy.conf.example`.

## Vận hành

```bash
docker compose logs -f kiro-go             # log API
docker compose pull && docker compose up -d # cập nhật thủ công
docker compose restart kiro-go             # restart
```

Backup định kỳ thư mục `data/`.

---

## Lưu ý

> [!IMPORTANT]
> Nginx reverse proxy phải **tắt buffering** để stream SSE. Xem mẫu trong `nginx-reverse-proxy.conf.example` và dán vào Custom Config của site trong FlashPanel.

> [!WARNING]
> `data/config.json` chứa token tài khoản Kiro + API key của khách — coi như secret. Đã `.gitignore`. Backup riêng.

> [!CAUTION]
> Tự kiểm tra điều khoản sử dụng của Kiro/AWS trước khi bán lại để tránh khoá tài khoản nguồn.

## Các file trong repo

| File | Vai trò |
|------|---------|
| `docker-compose.yml` | Chạy Kiro-Go, bind `127.0.0.1:8080` |
| `deploy-hook.sh` | FlashPanel chạy sau mỗi lần pull GitHub |
| `nginx-reverse-proxy.conf.example` | Mẫu Nginx (tắt buffering cho SSE + map `/check-key`) |
| `web-custom/custom-bulk-keys.js` | Tính năng tạo key hàng loạt trong panel admin |
| `web-custom/check-key.html` | Trang khách tự kiểm tra key tại `/check-key` |
| `keycheck/` | Sidecar Go phục vụ trang check-key + API xem usage (có chống hack) |
| `scripts/create-keys.ps1` | Script tạo key hàng loạt qua dòng lệnh |
| `.env.example` | Mẫu biến môi trường (copy thành `.env` trên VPS) |
| `.gitignore` | Bỏ qua `.env`, `data/` |

# Vận hành & Tối ưu — Kiro-Go API

Tài liệu các công cụ quản lý/tối ưu đi kèm (không gồm thanh toán). Tất cả script nằm trong `scripts/`, chạy trên VPS Linux.

## Tổng quan công cụ

| Script | Mục đích | Tần suất khuyến nghị |
|--------|----------|----------------------|
| `backup-config.sh` | Sao lưu `config.json` (key + tài khoản), nén + xoay vòng | 6 tiếng/lần (cron) |
| `restore-config.sh` | Khôi phục `config.json` từ backup (có undo) | Khi cần |
| `healthcheck.sh` | Giám sát service + pool, cảnh báo Telegram/Discord | 5 phút/lần (cron) |
| `manage-keys.sh` | Báo cáo key: usage, gần hết hạn mức, chưa dùng, đã cạn | Khi cần |
| `create-keys.ps1` | Tạo key hàng loạt qua dòng lệnh (Windows) | Khi cần |
| `setup-cron.sh` | Cài cron tự động cho backup + healthcheck | 1 lần |

## Cài đặt tự động (1 lần)

```bash
cd /duong-dan-repo
bash scripts/setup-cron.sh
```

Sau đó: backup chạy mỗi 6 tiếng vào `data/backups/`, healthcheck mỗi 5 phút ghi `data/health.log`.

## Backup & Restore

```bash
# Sao lưu thủ công
bash scripts/backup-config.sh

# Xem các bản backup
bash scripts/restore-config.sh

# Khôi phục bản mới nhất (tự sao lưu bản hiện tại trước khi ghi đè)
bash scripts/restore-config.sh latest
docker compose restart kiro-go keycheck
```

> [!TIP]
> Nên thỉnh thoảng tải bản backup trong `data/backups/` về máy/khác nơi (off-site), vì nếu mất cả VPS thì backup tại chỗ cũng mất.

## Cảnh báo sự cố (Telegram / Discord)

Thêm vào `.env` (một trong hai):

```bash
# Telegram
TELEGRAM_TOKEN=123456:ABC...
TELEGRAM_CHAT_ID=987654321

# hoặc Discord webhook
ALERT_WEBHOOK=https://discord.com/api/webhooks/...
```

`healthcheck.sh` chỉ gửi khi **trạng thái đổi** (DOWN/khôi phục) để tránh spam.

## Báo cáo & quản lý key

```bash
# Bảng tổng hợp tất cả key + usage
bash scripts/manage-keys.sh report

# Xuất CSV
bash scripts/manage-keys.sh report --csv keys.csv

# Key đã dùng >= 80% hạn mức (nhắc khách gia hạn)
bash scripts/manage-keys.sh near-limit 80

# Key chưa từng dùng
bash scripts/manage-keys.sh unused

# Key đã hết hạn mức
bash scripts/manage-keys.sh exhausted
```

(Đọc `ADMIN_PASSWORD` từ `.env`; mặc định gọi `http://127.0.0.1:8080`.)

## Tạo key hàng loạt

- Trong panel admin: modal **Add API Key** có sẵn trường **Số lượng** (tính năng đã thêm).
- Hoặc dòng lệnh (Windows): `scripts/create-keys.ps1` (xem README-DEPLOY.md).

## Đã có sẵn trong Kiro-Go (không cần xây thêm)

Quản lý tài khoản (thêm/bật/tắt/xóa hàng loạt, quota, overage, weight, proxy riêng), nhật ký request, thống kê (request/token/credit, uptime, độ tin cậy), export tài khoản JSON, prompt filter, thinking mode, kiểm tra cập nhật. Các script trên **bổ sung** những gì Kiro-Go chưa có: backup tự động, cảnh báo chủ động, báo cáo key theo nghiệp vụ bán hàng.

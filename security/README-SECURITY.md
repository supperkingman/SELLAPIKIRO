# Bảo mật — Chống hack admin & chống dò key

Tài liệu các lớp bảo vệ cho `api.mmodiary.com`.

## Các lớp đã triển khai

| Lớp | Bảo vệ gì | Cơ chế |
|-----|-----------|--------|
| A | Rate-limit/ban đúng IP khách | keycheck đọc IP thật từ `X-Forwarded-For` |
| B | Brute-force mật khẩu admin | fail2ban ban IP sai login ở firewall |
| C | Dò key qua /api/check-key | keycheck rate-limit (app) + fail2ban (firewall) |
| D | (tùy chọn) Khóa /admin theo IP | OLS chỉ cho IP tin cậy vào /admin |

## Lớp A — keycheck nhận IP thật

Trước đây keycheck thấy IP gateway Docker (`172.18.0.1`) nên ban gộp mọi khách. Đã sửa: đọc IP **cuối cùng** trong `X-Forwarded-For` (do OLS ghi, không giả mạo được). Rate-limit nội bộ của keycheck: ~10 lượt/phút mỗi IP, ban 15 phút sau 8 lần key sai liên tục.

## Lớp B & C — fail2ban

Cài đặt tự động:
```bash
sudo bash security/setup-security.sh
```

Cấu hình:
- **kiro-admin**: IP sai `POST /admin/api/login` >= 5 lần / 10 phút → ban **1 giờ**
- **kiro-checkkey**: IP gọi `/api/check-key` > 40 lần / 10 phút → ban **30 phút**

> [!IMPORTANT]
> File `jail.d/kiro.local` có `ignoreip` whitelist IP quản trị của bạn để tránh tự khóa. Nếu IP của bạn đổi, sửa lại dòng `ignoreip` rồi chạy `sudo systemctl restart fail2ban`.

Lệnh hữu ích:
```bash
fail2ban-client status kiro-admin          # xem IP đang bị ban
fail2ban-client set kiro-admin unbanip <IP> # gỡ ban 1 IP
sudo tail -f /var/log/fail2ban.log          # theo dõi realtime
```

## Lớp D — Khóa /admin theo IP (tùy chọn, mạnh nhất)

Nếu bạn luôn vào admin từ IP cố định, thêm vào `context /admin` trong vhost OLS để chỉ IP đó truy cập được. Xem hướng dẫn khi cần (hỏi để được cấu hình).

## Kiểm thử

```bash
# Sai mat khau admin 6 lan tu 1 IP la (khong phai IP whitelist) -> lan 6 bi chan
# Kiem tra: fail2ban-client status kiro-admin  -> thay IP trong "Banned IP list"
```

> [!CAUTION]
> Đừng test brute-force từ chính IP whitelist của bạn — nó sẽ không bị ban (đúng thiết kế). Test từ IP khác (ví dụ điện thoại 4G) hoặc tạm bỏ IP khỏi `ignoreip`.

## Các biện pháp khác nên làm

1. **Đổi mật khẩu admin mạnh** (đã lộ plaintext lúc setup) — trong trang `/admin`.
2. **Bật Require API Key** trong Settings để bắt buộc khách có key hợp lệ.
3. **Đóng cổng WebAdmin OLS (7080)** khỏi internet nếu không dùng, hoặc giới hạn theo IP.
4. **Cloudflare**: bật proxy (cam) + WAF để thêm 1 lớp chặn trước khi tới VPS.

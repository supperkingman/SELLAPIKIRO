# Triển khai trang mới — chỉ vài bước

Quy trình dựng một site bán API Kiro-Go mới trên VPS (OpenLiteSpeed + FlashPanel + Docker).

## Điều kiện VPS (1 lần cho mỗi VPS)
- Đã cài: Docker, Docker Compose, OpenLiteSpeed, fail2ban.
- FlashPanel đã thêm tên miền + bật SSL + kết nối GitHub repo.

## Các bước dựng site mới

### 1. FlashPanel: tạo site + kết nối GitHub
- Thêm tên miền, bật SSL (Let's Encrypt).
- Deploy from GitHub: chọn repo `supperkingman/kiro-sell-mmodiary`, branch `main`.
- Đặt **Post-deploy command**: `bash deploy-hook.sh`
- Deploy lần đầu để clone code về.

### 2. (Đa site) Sửa .env nếu chạy nhiều site trên 1 VPS
Nếu đây là site thứ 2+ trên cùng VPS, trước khi bootstrap hãy tạo `.env` với cổng riêng:
```bash
cd <thu-muc-site>
cp .env.example .env
nano .env    # đặt SITE_NAME, KIRO_PORT, KEYCHECK_PORT khác site cũ
```
Site đầu tiên bỏ qua bước này (dùng mặc định 8080/8081).

### 3. Chạy bootstrap — 1 lệnh lo hết
```bash
cd <thu-muc-site>
sudo bash scripts/bootstrap.sh <ten-mien>
```
Ví dụ: `sudo bash scripts/bootstrap.sh api.mmodiary.com`

Bootstrap tự động:
1. Tạo `.env` + sinh mật khẩu admin mạnh (in ra 1 lần — **lưu lại**)
2. Thêm user vào group docker
3. Build + khởi động container
4. Cấu hình OpenLiteSpeed reverse proxy
5. Cài fail2ban chống hack
6. Cài cron backup + healthcheck

> [!IMPORTANT]
> Nếu bootstrap báo cần đăng nhập lại (quyền docker), thoát SSH đăng nhập lại rồi chạy lại lệnh bootstrap. Nó idempotent — chạy lại an toàn.

### 4. Thêm tài khoản Kiro (từ máy Windows)
Sau khi đăng nhập tài khoản trong Kiro IDE:
```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\add-account.ps1 -Vps <IP_VPS>
```

### 5. Xong
- Admin: `https://<ten-mien>/admin`
- Check key: `https://<ten-mien>/check-key`

## Các lần cập nhật sau
Chỉ cần **push code lên GitHub** → FlashPanel tự chạy `deploy-hook.sh` → xong. Không cần SSH.

Dữ liệu (`config.json`: tài khoản + key) nằm ở `data/`, không bị ghi đè khi deploy.

## Gỡ lỗi
| Triệu chứng | Cách xử lý |
|-------------|-----------|
| `/admin` trả 000 | kiro-go chưa bind 0.0.0.0 — `deploy-hook.sh` đã tự fix, chạy lại |
| Endpoint 502/404 | OLS proxy chưa đúng — `sudo bash scripts/setup-ols-proxy.sh <domain>` |
| Bulk-keys mất | entrypoint tự chèn lại khi restart — `docker compose restart kiro-go` |
| Tài khoản lỗi token | lấy token mới từ Kiro IDE, chạy lại `add-account.ps1` |

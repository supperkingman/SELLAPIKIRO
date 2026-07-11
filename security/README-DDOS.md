# Chống DDoS — nhiều tầng

Chống DDoS hiệu quả phải làm theo **lớp**. Một VPS đơn không thể tự chống tấn công thể tích lớn — phải chặn ở **edge (Cloudflare)** trước khi lưu lượng tới VPS.

## Tổng quan các lớp

| Lớp | Chặn gì | Ở đâu |
|-----|---------|-------|
| 1. Cloudflare edge | Tấn công thể tích (L3/L4), bot, flood | Trước VPS (mạnh nhất) |
| 2. Throttling OLS | Flood tầng ứng dụng từ 1 IP | Reverse proxy |
| 3. Turnstile captcha | Bot tự động dò key | Trang check-key |
| 4. keycheck rate-limit + fail2ban | Dò key / brute-force | App + firewall |

Lớp 3, 4 đã cấu hình ở nơi khác (xem README-SECURITY.md + Turnstile). Tài liệu này tập trung lớp 1 & 2.

---

## Lớp 1 — Cloudflare (QUAN TRỌNG NHẤT)

Domain của bạn đã trỏ qua Cloudflare nhưng **chưa bật proxy (đám mây cam)**. Bật lên là có ngay lớp chống DDoS miễn phí.

### 1a. Bật proxy (đám mây cam)
1. Vào **dashboard.cloudflare.com** → chọn domain → **DNS**.
2. Tìm record `api` (hoặc `api.mmodiary.com`).
3. Bấm vào đám mây xám để chuyển thành **cam (Proxied)**. Lưu.

> [!IMPORTANT]
> Khi bật proxy cam, khách sẽ thấy IP Cloudflare thay vì IP VPS. Đảm bảo reverse proxy đọc IP thật qua `X-Forwarded-For` (keycheck đã làm) để rate-limit/fail2ban không ban nhầm.

> [!WARNING]
> Sau khi bật proxy cam, IP admin của bạn (mà OLS thấy) sẽ là IP Cloudflare, KHÔNG phải IP thật. Điều này ảnh hưởng allowlist `/admin`. Xem mục "Lưu ý allowlist + Cloudflare" bên dưới.

### 1b. Bật các tính năng chống tấn công (miễn phí)
Trong Cloudflare dashboard:
- **Security → Bots → Bot Fight Mode**: BẬT (chặn bot tự động).
- **Security → Settings → Security Level**: để `Medium` hoặc cao hơn.
- **Security → WAF → Rate limiting rules**: tạo rule, ví dụ:
  - Nếu request tới `/api/check-key` vượt **20 lần / 1 phút** cùng IP → **Block** 10 phút.
- **SSL/TLS**: đặt `Full` hoặc `Full (strict)`.

### 1c. Khi ĐANG BỊ tấn công
- **Security → Settings → Security Level → "I'm Under Attack"**: bật tạm. Cloudflare sẽ hiện trang thử thách JS cho mọi khách (chặn gần hết bot). Tắt lại khi hết tấn công.

### 1d. CHỐNG BYPASS — bắt buộc làm (nếu không, Cloudflare vô dụng)

> [!CAUTION]
> Kẻ tấn công biết IP VPS thật (13.212.212.240) có thể gọi **thẳng vào VPS**, bỏ qua toàn bộ Cloudflare. Phải chặn ở firewall: **chỉ cho dải IP Cloudflare** kết nối tới cổng 80/443.

Chạy trên VPS (dùng ufw). Script này lấy dải IP Cloudflare mới nhất rồi chỉ mở 80/443 cho chúng:

```bash
# 1. Cho phep SSH truoc (tranh tu khoa minh khoi VPS!)
sudo ufw allow 22/tcp

# 2. Lay dai IP Cloudflare va cho phep 80/443 tu chung
for ip in $(curl -s https://www.cloudflare.com/ips-v4); do
  sudo ufw allow from "$ip" to any port 80,443 proto tcp
done
for ip in $(curl -s https://www.cloudflare.com/ips-v6); do
  sudo ufw allow from "$ip" to any port 80,443 proto tcp
done

# 3. CHAN moi IP khac toi 80/443 (chi Cloudflare vao duoc)
sudo ufw deny 80/tcp
sudo ufw deny 443/tcp

# 4. Bat ufw (neu chua bat)
sudo ufw enable && sudo ufw status numbered
```

> [!IMPORTANT]
> Sau bước này, truy cập thẳng `http://13.212.212.240` sẽ bị chặn — chỉ vào được qua `https://api.mmodiary.com` (qua Cloudflare). Dải IP Cloudflare hiếm khi đổi; nếu đổi, chạy lại bước 2. Có thể đặt cron chạy lại hằng tháng.

### 1e. Bảo vệ /admin bằng Cloudflare (thay allowlist OLS)

Vì bạn ưu tiên Cloudflare và hay đổi IP, đây là cách gọn nhất cho admin:

1. **Security → WAF → Custom rules → Create rule**.
2. Field: `URI Path` — Operator: `starts with` — Value: `/admin`.
3. `AND` — Field: `IP Source Address` — Operator: `is not in` — Value: (IP của bạn, có thể nhiều).
4. Action: **Block**. Deploy.

Đổi IP sau này: chỉ sửa danh sách IP trong rule trên dashboard, **không cần SSH**.

> [!TIP]
> Dùng cách này thì **không cần** context allowlist `/admin` trong OLS. Nhưng cứ để đó cũng không sao — nó là lớp phòng thủ chiều sâu. Nếu để, nhớ `allow` cả localhost để healthcheck nội bộ chạy được. Vì OLS giờ thấy IP Cloudflare (không phải IP thật), bạn nên **nới** allowlist OLS thành `allow ALL` cho `/admin` và để Cloudflare WAF làm rào chính — nếu không sẽ tự khóa mình.

---

## Lưu ý allowlist `/admin` khi bật Cloudflare proxy

Đây là điểm dễ sai. Khi proxy cam BẬT:
- OLS thấy **IP của Cloudflare**, không phải IP thật của bạn → allowlist theo IP thật sẽ **chặn nhầm chính bạn**.

Có 2 cách xử lý:

**Cách A (khuyến nghị): KHÔNG proxy cam cho subdomain admin.**
- Nếu bạn vào admin qua `api.mmodiary.com/admin`, tách một subdomain riêng (vd `admin-api.mmodiary.com`) để **đám mây xám** (DNS only) → OLS thấy IP thật → allowlist hoạt động đúng.

**Cách B: Dùng Cloudflare bảo vệ /admin thay cho allowlist OLS.**
- Để proxy cam BẬT, và dùng **Cloudflare → WAF → Custom rule**: chỉ cho IP của bạn vào path `/admin*`, còn lại Block. Khi đó bỏ (hoặc nới) allowlist OLS.
- Đổi IP thì sửa rule trên Cloudflare (không cần SSH).

> [!TIP]
> Nếu bạn hay đổi IP (mạng nhà/4G), Cách B tiện hơn vì đổi trên dashboard. Còn dùng allowlist OLS thì chạy `scripts/update-admin-ip.sh myip` mỗi khi đổi IP.

---

## Lớp 2 — Throttling OpenLiteSpeed

Đã thêm khối `perClientConnLimit` trong `openlitespeed-vhost.conf.example`. Nó giới hạn mỗi IP:
- `dynReqPerSec 25` — tối đa ~25 request động/giây.
- `softLimit 60` / `hardLimit 80` — số kết nối đồng thời.
- `banPeriod 60` — chặn IP vi phạm 60 giây.

Áp dụng: dán khối đó vào vHost Config trong FlashPanel/OLS → Save → Graceful Restart.

> [!CAUTION]
> Đây chỉ chặn flood tầng ứng dụng từ ít IP. Với DDoS thể tích (hàng nghìn IP), chỉ Cloudflare edge (Lớp 1) mới đỡ nổi. Đừng dựa vào riêng OLS.

Chỉnh số: nếu khách thật bị chặn (ví dụ dùng chung IP văn phòng), tăng `dynReqPerSec` và `softLimit`.

---

## Kiểm thử nhanh

```bash
# Bắn 100 request nhanh từ 1 IP tới check-key -> phai thay bi chan tam
for i in $(seq 1 100); do curl -s -o /dev/null -w "%{http_code} " \
  -X POST https://api.mmodiary.com/api/check-key \
  -H "Content-Type: application/json" -d '{"key":"test"}'; done; echo
# Ky vong: mot so 200/403 dau, sau do 429 hoac connection refused khi vuot nguong
```

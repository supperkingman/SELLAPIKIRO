# Thêm tài khoản Kiro — 2 cách

## Cách 1 (KHUYẾN NGHỊ): Export text → Dán vào web

Đơn giản nhất, không cần SSH, không cần nhập mật khẩu server.

### Bước 1 — Trên máy Windows (đã đăng nhập Kiro IDE)
```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\export-account.ps1
```
Script in ra JSON tài khoản và **tự copy vào clipboard**.

### Bước 2 — Dán vào trang import
1. Mở `https://api.mmodiary.com/admin/import-account.html`
2. Nhập mật khẩu quản trị
3. Dán (Ctrl+V) vào ô nội dung
4. Bấm **Import tài khoản**

Xong. Tài khoản vào pool ngay, không cần restart.

> [!TIP]
> Dán được **nhiều tài khoản** một lúc: mỗi dòng 1 JSON (chạy export nhiều lần, ghép các dòng lại), hoặc 1 mảng JSON.

---

## Cách 2: Script tự động qua SSH (1 lệnh)

Nếu thích tự động hoàn toàn từ máy (cần nhập mật khẩu SSH):
```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\add-account.ps1 -Vps 13.212.212.240
```
Script tự: đọc token → upload → nạp vào config → restart. Hỏi mật khẩu SSH (2 lần) + sudo (1 lần).

Khỏi nhập mật khẩu mỗi lần → thiết lập SSH key (xem cuối file).

---

## So sánh

| | Cách 1 (export/web) | Cách 2 (add-account.ps1) |
|-|---------------------|--------------------------|
| Cần SSH | Không | Có |
| Nhập mật khẩu server | Không | Có (SSH + sudo) |
| Restart container | Không | Có |
| Nhiều tài khoản | Dán nhiều dòng | Chạy lại nhiều lần |

Cả hai đều hỗ trợ tài khoản Microsoft Entra (external_idp), Google/GitHub (social), AWS (idc).

---

## Thiết lập SSH key (tùy chọn, cho Cách 2)

Tạo key một lần trên máy Windows:
```powershell
ssh-keygen -t ed25519 -f "$env:USERPROFILE\.ssh\id_ed25519" -N '""'
type "$env:USERPROFILE\.ssh\id_ed25519.pub" | ssh flashpanel@13.212.212.240 "mkdir -p ~/.ssh && cat >> ~/.ssh/authorized_keys"
```
Sau đó `add-account.ps1` không hỏi mật khẩu SSH nữa.

@echo off
REM ============================================================
REM  Them tai khoan Kiro - Double-click de chay
REM ============================================================
REM  Sau khi da dang nhap tai khoan Kiro trong Kiro IDE,
REM  double-click file nay -> no se lay token + copy vao clipboard.
REM  Roi mo trang Add Account -> Import bang text -> dan (Ctrl+V).
REM ============================================================
cd /d "%~dp0"
powershell -ExecutionPolicy Bypass -NoProfile -File "%~dp0scripts\export-account.ps1"
echo.
echo ============================================================
echo  Da copy JSON tai khoan vao clipboard (neu thanh cong).
echo  Buoc tiep theo:
echo    1. Mo https://api.mmodiary.com/admin
echo    2. Bam Add Account -^> Import bang text
echo    3. Dan (Ctrl+V) roi bam Import
echo ============================================================
echo.
pause

// i18n.go - he thong song ngu Anh (en) / Viet (vi).
// Ngon ngu luu trong cookie "lang"; mac dinh vi. Template goi {{t .Lang "key"}}.
package main

import (
	"net/http"
	"time"
)

const (
	langCookie  = "lang"
	defaultLang = "en" // mac dinh tieng Anh (co the doi sang vi bang nut tren nav)
	fallbackLang = "vi"
)

// currentLang doc ngon ngu tu cookie, mac dinh vi.
func currentLang(r *http.Request) string {
	if c, err := r.Cookie(langCookie); err == nil {
		if c.Value == "en" || c.Value == "vi" {
			return c.Value
		}
	}
	return defaultLang
}

// t tra ve chuoi da dich theo ngon ngu. Fallback: vi -> key.
func t(lang, key string) string {
	if m, ok := i18n[key]; ok {
		if v, ok := m[lang]; ok && v != "" {
			return v
		}
		if v, ok := m[fallbackLang]; ok && v != "" {
			return v
		}
		if v, ok := m["en"]; ok {
			return v
		}
	}
	return key
}

// handleSetLang doi ngon ngu roi quay lai trang truoc do.
func (app *App) handleSetLang(w http.ResponseWriter, r *http.Request) {
	lang := r.URL.Query().Get("l")
	if lang != "en" && lang != "vi" {
		lang = defaultLang
	}
	http.SetCookie(w, &http.Cookie{
		Name:     langCookie,
		Value:    lang,
		Path:     "/",
		Expires:  time.Now().Add(365 * 24 * time.Hour),
		HttpOnly: false,
		SameSite: http.SameSiteLaxMode,
	})
	ref := r.Header.Get("Referer")
	if ref == "" {
		ref = "/"
	}
	http.Redirect(w, r, ref, http.StatusSeeOther)
}

// i18n: tu dien dich. Moi key co ban vi + en.
var i18n = map[string]map[string]string{
	// ===== Nav / chung =====
	"nav_features": {"vi": "Tính năng", "en": "Features"},
	"nav_pricing":  {"vi": "Bảng giá", "en": "Pricing"},
	"nav_faq":      {"vi": "FAQ", "en": "FAQ"},
	"nav_checkkey": {"vi": "Kiểm tra Key", "en": "Check Key"},
	"nav_account":  {"vi": "Tài khoản", "en": "Account"},
	"nav_logout":   {"vi": "Đăng xuất", "en": "Log out"},
	"nav_login":    {"vi": "Đăng nhập", "en": "Log in"},
	"nav_start":    {"vi": "Bắt đầu", "en": "Get started"},
	"footer_tagline": {"vi": "Cung cấp API Kiro chất lượng cao, giá tốt, giao key tức thì sau khi thanh toán.",
		"en": "High-quality Kiro API at great prices, with instant key delivery after payment."},
	"footer_product": {"vi": "Sản phẩm", "en": "Product"},
	"footer_support": {"vi": "Hỗ trợ", "en": "Support"},
	"footer_contact": {"vi": "Liên hệ", "en": "Contact"},
	"footer_rights":  {"vi": "Bảo lưu mọi quyền.", "en": "All rights reserved."},

	// ===== Landing =====
	"hero_badge":   {"vi": "Opus 4.8 · Credit chính xác 100% từ Kiro", "en": "Opus 4.8 · 100% accurate credits from Kiro"},
	"hero_title1":  {"vi": "API Kiro với Opus 4.8,", "en": "Kiro API with Opus 4.8,"},
	"hero_title2":  {"vi": "giá chỉ từ $3", "en": "from just $3"},
	"hero_desc":    {"vi": "Truy cập API Kiro chạy model Opus 4.8. Credit được tính chính xác 100% thật từ Kiro, không cộng thêm. Hệ thống dùng Kiro-Go minh bạch, theo dõi usage realtime.", "en": "Access the Kiro API powered by Opus 4.8. Credits are billed 100% accurately from Kiro with zero markup. Built on the transparent Kiro-Go system with real-time usage tracking."},
	"hero_cta1":    {"vi": "Xem bảng giá →", "en": "View pricing →"},
	"hero_cta2":    {"vi": "Tạo tài khoản", "en": "Create account"},
	"hero_stat1":   {"vi": "credit mỗi gói MAX", "en": "credits in MAX plan"},
	"hero_stat2":   {"vi": "tiết kiệm so với giá gốc", "en": "savings vs original"},
	"hero_stat3":   {"vi": "giao key sau thanh toán", "en": "key delivery after payment"},
	"hero_instant": {"vi": "Tức thì", "en": "Instant"},
	"card_yourkey": {"vi": "API Key của bạn", "en": "Your API Key"},
	"card_active":  {"vi": "Đang hoạt động", "en": "Active"},
	"card_used":    {"vi": "Credit đã dùng", "en": "Credits used"},

	"feat_eyebrow": {"vi": "Vì sao chọn chúng tôi", "en": "Why choose us"},
	"feat_title":   {"vi": "Mọi thứ bạn cần cho API Kiro Opus 4.8", "en": "Everything you need for the Kiro Opus 4.8 API"},
	"feat1_t":      {"vi": "Giao key tức thì", "en": "Instant key delivery"},
	"feat1_d":      {"vi": "Sau khi thanh toán được xác nhận, API key được tạo tự động và hiện ngay trong tài khoản của bạn.", "en": "Once payment is confirmed, your API key is auto-generated and appears instantly in your account."},
	"feat2_t":      {"vi": "Credit chính xác 100%", "en": "100% accurate credits"},
	"feat2_d":      {"vi": "Credit tính đúng thật từ Kiro, không cộng phí ẩn. Xem đã dùng và còn lại realtime, minh bạch từng request.", "en": "Credits billed exactly as Kiro reports, no hidden markup. See used and remaining in real time, transparent per request."},
	"feat3_t":      {"vi": "An toàn & bảo mật", "en": "Safe & secure"},
	"feat3_d":      {"vi": "Key mã hóa, xác thực constant-time, chống dò key với rate-limit và captcha.", "en": "Encrypted keys, constant-time auth, anti-bruteforce with rate-limit and captcha."},
	"feat4_t":      {"vi": "Giá cực tốt", "en": "Great prices"},
	"feat4_d":      {"vi": "Tiết kiệm tới 85% so với giá gốc. Nhiều gói phù hợp mọi nhu cầu.", "en": "Save up to 85% off original prices. Multiple plans for every need."},
	"feat5_t":      {"vi": "Hệ thống Kiro-Go minh bạch", "en": "Transparent Kiro-Go system"},
	"feat5_d":      {"vi": "Vận hành trên Kiro-Go mã nguồn mở. Usage đọc trực tiếp từ Kiro, minh bạch và kiểm chứng được.", "en": "Runs on the open-source Kiro-Go. Usage is read directly from Kiro, transparent and verifiable."},
	"feat6_t":      {"vi": "Nhiều gói linh hoạt", "en": "Flexible plans"},
	"feat6_d":      {"vi": "Từ Pro 1.000 credit đến MAX 50.000 credit. Nâng cấp bất cứ lúc nào.", "en": "From Pro 1,000 credits to MAX 50,000 credits. Upgrade anytime."},

	"price_eyebrow": {"vi": "Bảng giá", "en": "Pricing"},
	"price_title":   {"vi": "Chọn gói phù hợp với bạn", "en": "Choose the plan that fits you"},
	"price_sub":     {"vi": "Giá ưu đãi, thanh toán một lần, dùng đến hết credit.", "en": "Discounted prices, one-time payment, use until credits run out."},
	"price_credits": {"vi": "credit", "en": "credits"},
	"price_buy":     {"vi": "Mua ngay", "en": "Buy now"},
	"price_popular": {"vi": "PHỔ BIẾN NHẤT", "en": "MOST POPULAR"},
	"price_f1":      {"vi": "credit sử dụng", "en": "usable credits"},
	"price_f2":      {"vi": "Giao API key tức thì", "en": "Instant API key delivery"},
	"price_f3":      {"vi": "Theo dõi usage realtime", "en": "Real-time usage tracking"},
	"price_f4":      {"vi": "Không giới hạn thời gian", "en": "No time limit"},

	"faq_eyebrow": {"vi": "FAQ", "en": "FAQ"},
	"faq_title":   {"vi": "Câu hỏi thường gặp", "en": "Frequently asked questions"},
	"faq_q1":      {"vi": "Sau khi mua tôi nhận key thế nào?", "en": "How do I receive my key after buying?"},
	"faq_a1":      {"vi": "Sau khi bạn thanh toán và admin xác nhận, hệ thống tự tạo API key và hiển thị ngay trong trang tài khoản của bạn.", "en": "After you pay and it is confirmed, the system auto-generates the API key and shows it in your account page."},
	"faq_q2":      {"vi": "Credit dùng để làm gì?", "en": "What are credits for?"},
	"faq_a2":      {"vi": "Mỗi request tới API Kiro (Opus 4.8) tiêu tốn credit đúng như Kiro báo — chính xác 100%, không cộng thêm. Bạn theo dõi credit đã dùng và còn lại realtime trong dashboard.", "en": "Each request to the Kiro API (Opus 4.8) consumes credits exactly as Kiro reports — 100% accurate, no markup. Track used and remaining credits in real time in your dashboard."},
	"faq_q3":      {"vi": "Key có hết hạn không?", "en": "Do keys expire?"},
	"faq_a3":      {"vi": "Mỗi gói có thời hạn sử dụng riêng (hiển thị trên thẻ giá). Key hoạt động cho đến khi hết credit hoặc hết thời hạn của gói, tùy điều kiện nào đến trước.", "en": "Each plan has its own validity period (shown on the pricing card). A key works until either its credits are used up or the plan's period ends, whichever comes first."},
	"faq_q4":      {"vi": "Thanh toán bằng cách nào?", "en": "How can I pay?"},
	"faq_a4":      {"vi": "Hỗ trợ chuyển khoản ngân hàng (VND), USDT (BEP20) và PayPal. Nhiều cổng tự động giao key ngay.", "en": "We support bank transfer (VND), USDT (BEP20) and PayPal. Several gateways deliver keys automatically."},

	// ===== Auth =====
	"login_title":    {"vi": "Chào mừng trở lại", "en": "Welcome back"},
	"login_sub":      {"vi": "Đăng nhập để quản lý API key của bạn.", "en": "Log in to manage your API keys."},
	"login_google":   {"vi": "Đăng nhập với Google", "en": "Sign in with Google"},
	"or":             {"vi": "hoặc", "en": "or"},
	"label_email":    {"vi": "Email", "en": "Email"},
	"label_password": {"vi": "Mật khẩu", "en": "Password"},
	"label_fullname": {"vi": "Họ tên", "en": "Full name"},
	"btn_login":      {"vi": "Đăng nhập", "en": "Log in"},
	"no_account":     {"vi": "Chưa có tài khoản?", "en": "No account yet?"},
	"register_now":   {"vi": "Đăng ký ngay", "en": "Register now"},
	"reg_title":      {"vi": "Tạo tài khoản", "en": "Create account"},
	"reg_sub":        {"vi": "Bắt đầu mua và quản lý API Kiro trong vài giây.", "en": "Start buying and managing Kiro API in seconds."},
	"reg_google":     {"vi": "Đăng ký với Google", "en": "Sign up with Google"},
	"pw_hint":        {"vi": "Tối thiểu 6 ký tự.", "en": "At least 6 characters."},
	"btn_register":   {"vi": "Tạo tài khoản", "en": "Create account"},
	"have_account":   {"vi": "Đã có tài khoản?", "en": "Already have an account?"},
	"err_login":      {"vi": "Email hoặc mật khẩu không đúng.", "en": "Incorrect email or password."},
	"err_reg_email":  {"vi": "Email không hợp lệ hoặc mật khẩu dưới 6 ký tự.", "en": "Invalid email or password shorter than 6 characters."},
	"err_reg_dup":    {"vi": "Email này đã được đăng ký.", "en": "This email is already registered."},

	// ===== Pricing page =====
	"pricing_h1":    {"vi": "Giá minh bạch, không phí ẩn", "en": "Transparent pricing, no hidden fees"},
	"pricing_lead":  {"vi": "Thanh toán một lần, nhận key ngay, dùng đến hết credit. Tiết kiệm tới 85% so với giá gốc.", "en": "One-time payment, instant key, use until credits run out. Save up to 85% off original prices."},
	"pricing_note":  {"vi": "Cần gói lớn hơn hoặc báo giá riêng?", "en": "Need a bigger plan or custom quote?"},
	"pricing_contact": {"vi": "Liên hệ với chúng tôi", "en": "Contact us"},

	// ===== Checkout =====
	"co_title":   {"vi": "Xác nhận đơn hàng", "en": "Confirm your order"},
	"co_sub":     {"vi": "Kiểm tra lại thông tin gói trước khi tạo đơn.", "en": "Review the plan details before creating the order."},
	"co_plan":    {"vi": "Gói", "en": "Plan"},
	"co_total":   {"vi": "Tổng cộng", "en": "Total"},
	"co_create":  {"vi": "Tạo đơn & tiếp tục thanh toán →", "en": "Create order & continue to payment →"},
	"co_steps":   {"vi": "Quy trình", "en": "Process"},
	"co_step1":   {"vi": "Tạo đơn hàng", "en": "Create order"},
	"co_step2":   {"vi": "Thanh toán theo hướng dẫn", "en": "Pay as instructed"},
	"co_step3":   {"vi": "Xác nhận thanh toán", "en": "Confirm payment"},
	"co_step4":   {"vi": "Nhận API key ngay", "en": "Get API key instantly"},

	// ===== Dashboard =====
	"dash_hello":    {"vi": "Xin chào 👋", "en": "Hello 👋"},
	"dash_sub":      {"vi": "Quản lý API key và theo dõi đơn hàng của bạn.", "en": "Manage your API keys and track your orders."},
	"dash_mykeys":   {"vi": "API Key của tôi", "en": "My API Keys"},
	"dash_buymore":  {"vi": "+ Mua thêm", "en": "+ Buy more"},
	"dash_order":    {"vi": "Đơn", "en": "Order"},
	"dash_nokeys_t": {"vi": "Chưa có API key nào", "en": "No API keys yet"},
	"dash_nokeys_d": {"vi": "Mua một gói để nhận API key đầu tiên của bạn.", "en": "Buy a plan to get your first API key."},
	"dash_viewprice": {"vi": "Xem bảng giá", "en": "View pricing"},
	"dash_history":  {"vi": "Lịch sử đơn hàng", "en": "Order history"},
	"dash_noorders": {"vi": "Chưa có đơn hàng nào.", "en": "No orders yet."},
	"col_code":      {"vi": "Mã đơn", "en": "Order code"},
	"col_plan":      {"vi": "Gói", "en": "Plan"},
	"col_price":     {"vi": "Giá", "en": "Price"},
	"col_status":    {"vi": "Trạng thái", "en": "Status"},
	"col_detail":    {"vi": "Chi tiết →", "en": "Details →"},
	"copy":          {"vi": "Copy", "en": "Copy"},
	"copied":        {"vi": "✔ Đã copy", "en": "✔ Copied"},

	// ===== Order statuses =====
	"st_pending":   {"vi": "Chờ thanh toán", "en": "Awaiting payment"},
	"st_reported":  {"vi": "Chờ duyệt", "en": "Pending review"},
	"st_approved":  {"vi": "Đã giao key", "en": "Key delivered"},
	"st_cancelled": {"vi": "Đã hủy", "en": "Cancelled"},

	// ===== Order detail / payment =====
	"ord_back":       {"vi": "← Về tài khoản", "en": "← Back to account"},
	"ord_plan":       {"vi": "Gói", "en": "Plan"},
	"ord_amount":     {"vi": "Số tiền", "en": "Amount"},
	"ord_created":    {"vi": "Ngày tạo", "en": "Created"},
	"ord_yourkey":    {"vi": "🎉 API Key của bạn", "en": "🎉 Your API Key"},
	"ord_key_note":   {"vi": "Sao chép và sử dụng key dưới đây. Bạn cũng có thể xem lại trong trang tài khoản.", "en": "Copy and use the key below. You can also find it in your account page."},
	"ord_cancelled":  {"vi": "Đơn hàng này đã bị hủy. Nếu có nhầm lẫn, vui lòng liên hệ hỗ trợ.", "en": "This order was cancelled. If this is a mistake, please contact support."},
	"pay_choose":     {"vi": "Chọn phương thức thanh toán", "en": "Choose a payment method"},
	"pay_bank":       {"vi": "🏦 Chuyển khoản (VND)", "en": "🏦 Bank transfer (VND)"},
	"pay_usdt":       {"vi": "₮ USDT (BEP20)", "en": "₮ USDT (BEP20)"},
	"pay_paypal":     {"vi": "🅿️ PayPal (USD)", "en": "🅿️ PayPal (USD)"},
	"pay_bankname":   {"vi": "Ngân hàng", "en": "Bank"},
	"pay_account":    {"vi": "Số tài khoản", "en": "Account number"},
	"pay_holder":     {"vi": "Chủ tài khoản", "en": "Account holder"},
	"pay_amount":     {"vi": "Số tiền", "en": "Amount"},
	"pay_content":    {"vi": "Nội dung chuyển khoản (bắt buộc đúng)", "en": "Transfer memo (must be exact)"},
	"pay_auto_note":  {"vi": "Hệ thống tự động xác nhận và giao key sau khi nhận được tiền (thường trong 1 phút).", "en": "The system auto-confirms and delivers the key after payment is received (usually within 1 minute)."},
	"pay_network":    {"vi": "Mạng", "en": "Network"},
	"pay_token":      {"vi": "Token", "en": "Token"},
	"pay_exact":      {"vi": "Số tiền (chính xác)", "en": "Amount (exact)"},
	"pay_wallet":     {"vi": "Địa chỉ ví nhận", "en": "Receiving wallet address"},
	"pay_usdt_note":  {"vi": "Chuyển đúng số USDT trên tới ví (số lẻ giúp nhận diện đơn của bạn). Sau khi chuyển, bấm nút dưới để kiểm tra.", "en": "Send the exact USDT amount to the wallet (the decimal helps identify your order). After sending, click below to check."},
	"pay_usdt_check": {"vi": "Tôi đã chuyển USDT — Kiểm tra", "en": "I've sent USDT — Check"},
	"pay_paid":       {"vi": "Tôi đã thanh toán ✔", "en": "I have paid ✔"},
	"pay_none":       {"vi": "Hiện chưa có cổng thanh toán nào được kích hoạt. Vui lòng liên hệ với chúng tôi để được hỗ trợ hoàn tất đơn hàng.", "en": "No payment method is currently enabled. Please contact us to complete your order."},
	"pay_checking":   {"vi": "Đang kiểm tra giao dịch trên blockchain...", "en": "Checking the transaction on-chain..."},
	"pay_pp_checking": {"vi": "Đang xác nhận thanh toán PayPal...", "en": "Confirming PayPal payment..."},

	// ===== SEO =====
	"seo_home_title": {"vi": "Mua API Kiro (Opus 4.8) · Tương thích OpenAI", "en": "Buy Kiro API (Opus 4.8) · OpenAI-Compatible API"},
	"seo_home_desc":  {"vi": "Mua API Kiro chạy Claude Opus 4.8, tương thích OpenAI, giao key tức thì. Credit tính chính xác 100% từ Kiro, hệ thống Kiro-Go minh bạch. Dùng ngay với Cursor, VS Code, Claude Code.", "en": "Buy the Kiro API powered by Claude Opus 4.8 — OpenAI-compatible, instant key delivery. Credits billed 100% accurately by Kiro on the transparent Kiro-Go system. Works with Cursor, VS Code, Claude Code."},

	// ===== Dashboard expiry =====
	"dash_expiry":    {"vi": "Thời gian còn lại", "en": "Time remaining"},
	"dash_permanent": {"vi": "Không giới hạn", "en": "No expiry"},
	"dash_expired":   {"vi": "Đã hết hạn", "en": "Expired"},
	"dash_paused":    {"vi": "Tạm dừng", "en": "Paused"},
	"dur_min":        {"vi": "phút", "en": "m"},
	"dur_sec":        {"vi": "giây", "en": "s"},
	"dash_renew":      {"vi": "Gia hạn", "en": "Renew"},
	"dash_renew_pick": {"vi": "Chọn gói gia hạn…", "en": "Choose a renewal plan…"},

	// ===== Order reported =====
	"ord_reported_title": {"vi": "Đã ghi nhận báo thanh toán", "en": "Payment reported"},
	"ord_reported_msg":   {"vi": "Cảm ơn bạn! Chúng tôi đã nhận thông báo thanh toán và đang kiểm tra. Key sẽ được giao ngay sau khi xác nhận (thường trong vài phút). Bạn có thể tải lại trang này để xem trạng thái.", "en": "Thank you! We've received your payment report and are verifying it. Your key will be delivered right after confirmation (usually within a few minutes). You can refresh this page to check the status."},

	// ===== Duration =====
	"dur_days":     {"vi": "ngày", "en": "days"},
	"dur_hours":    {"vi": "giờ", "en": "hours"},
	"dur_lifetime": {"vi": "Vĩnh viễn", "en": "Lifetime"},

	// ===== Hero extra =====
	"hero_cta_docs":  {"vi": "Xem tài liệu", "en": "Read the docs"},
	"hero_sub_note":  {"vi": "Tương thích OpenAI · Dùng ngay với Cursor, VS Code, Claude Code", "en": "OpenAI-compatible · Works with Cursor, VS Code, Claude Code"},

	// ===== Trusted by =====
	"trust_title":    {"vi": "Được tin dùng bởi các nhà phát triển & đội ngũ AI", "en": "Trusted by developers and AI teams"},

	// ===== Supported models =====
	"models_eyebrow": {"vi": "Mô hình", "en": "Models"},
	"models_title":   {"vi": "Chạy trên Claude Opus 4.8", "en": "Powered by Claude Opus 4.8"},
	"models_sub":     {"vi": "Truy cập model coding hàng đầu qua một endpoint tương thích OpenAI duy nhất.", "en": "Access the top-tier coding model through a single OpenAI-compatible endpoint."},
	"cap_stream":     {"vi": "Streaming (SSE)", "en": "Streaming (SSE)"},
	"cap_stream_d":   {"vi": "Nhận token trả về theo thời gian thực cho trải nghiệm gõ mượt.", "en": "Receive tokens in real time for a smooth typing experience."},
	"cap_tool":       {"vi": "Tool Calling", "en": "Tool calling"},
	"cap_tool_d":     {"vi": "Gọi hàm và agent workflow theo chuẩn function calling.", "en": "Function calling and agent workflows out of the box."},
	"cap_vision":     {"vi": "Vision", "en": "Vision"},
	"cap_vision_d":   {"vi": "Gửi kèm hình ảnh để phân tích, đọc screenshot, UI.", "en": "Send images for analysis, screenshots and UI understanding."},
	"cap_json":       {"vi": "JSON Mode", "en": "JSON mode"},
	"cap_json_d":     {"vi": "Ép đầu ra JSON hợp lệ cho pipeline tự động.", "en": "Force valid JSON output for automated pipelines."},
	"cap_ctx":        {"vi": "Long Context", "en": "Long context"},
	"cap_ctx_d":      {"vi": "Cửa sổ ngữ cảnh lớn để làm việc với cả codebase.", "en": "Large context window to work across entire codebases."},
	"cap_mcp":        {"vi": "MCP Ready", "en": "MCP ready"},
	"cap_mcp_d":      {"vi": "Kết nối Model Context Protocol cho công cụ agent.", "en": "Model Context Protocol support for agentic tooling."},

	// ===== Performance =====
	"perf_eyebrow":   {"vi": "Hiệu năng", "en": "Performance"},
	"perf_title":     {"vi": "Hạ tầng ổn định, độ trễ thấp", "en": "Low-latency, reliable infrastructure"},
	"perf_uptime":    {"vi": "Uptime cam kết", "en": "Uptime target"},
	"perf_latency":   {"vi": "Độ trễ khởi tạo (TTFB)", "en": "Time to first byte"},
	"perf_stream_lbl":{"vi": "Streaming realtime", "en": "Realtime streaming"},
	"perf_concurrent":{"vi": "Request đồng thời", "en": "Concurrent requests"},
	"perf_compat":    {"vi": "Tương thích OpenAI", "en": "OpenAI compatibility"},
	"perf_monitor":   {"vi": "Giám sát usage", "en": "Usage monitoring"},
	"perf_yes":       {"vi": "Có · realtime", "en": "Yes · realtime"},
	"perf_unlimited": {"vi": "Không giới hạn*", "en": "Unlimited*"},

	// ===== API demo =====
	"demo_eyebrow":   {"vi": "Bắt đầu nhanh", "en": "Quickstart"},
	"demo_title":     {"vi": "Một request là chạy", "en": "One request and you're live"},
	"demo_sub":       {"vi": "Endpoint tương thích OpenAI. Chỉ cần đổi base URL và API key — giữ nguyên code sẵn có.", "en": "OpenAI-compatible endpoint. Just swap the base URL and API key — keep your existing code."},
	"demo_copy":      {"vi": "Sao chép", "en": "Copy"},
	"demo_copied":    {"vi": "✔ Đã sao chép", "en": "✔ Copied"},

	// ===== SDK =====
	"sdk_eyebrow":    {"vi": "SDK & ngôn ngữ", "en": "SDKs & languages"},
	"sdk_title":      {"vi": "Dùng với ngôn ngữ bạn thích", "en": "Use it in your favorite language"},
	"sdk_sub":        {"vi": "Vì tương thích OpenAI, mọi SDK chính thức và thư viện cộng đồng đều hoạt động.", "en": "Because it's OpenAI-compatible, every official SDK and community library just works."},

	// ===== Integrations =====
	"integ_eyebrow":  {"vi": "Tích hợp", "en": "Integrations"},
	"integ_title":    {"vi": "Cắm vào công cụ bạn đang dùng", "en": "Plug into the tools you already use"},
	"integ_sub":      {"vi": "Đặt base URL Kiro API vào bất kỳ công cụ nào hỗ trợ endpoint tùy chỉnh tương thích OpenAI.", "en": "Point any tool that supports a custom OpenAI-compatible endpoint at the Kiro API base URL."},

	// ===== Testimonials =====
	"tm_eyebrow":     {"vi": "Khách hàng nói gì", "en": "What developers say"},
	"tm_title":       {"vi": "Được yêu thích bởi builder", "en": "Loved by builders"},
	"tm1":            {"vi": "\"Đổi base URL sang Kiro API trong Cursor là chạy ngay. Credit trừ đúng như dashboard, không lệch.\"", "en": "\"Switched Cursor's base URL to the Kiro API and it just worked. Credits match the dashboard exactly — no surprises.\""},
	"tm1_by":         {"vi": "Lập trình viên độc lập", "en": "Indie developer"},
	"tm2":            {"vi": "\"Giá rẻ hơn hẳn mà vẫn là Opus 4.8. Team mình dùng cho agent nội bộ cả tháng nay ổn định.\"", "en": "\"Way cheaper and still Opus 4.8. Our team has run internal agents on it for a month — rock solid.\""},
	"tm2_by":         {"vi": "Founder startup", "en": "Startup founder"},
	"tm3":            {"vi": "\"Trang check-key và theo dõi usage minh bạch giúp mình kiểm soát chi phí dễ dàng.\"", "en": "\"The transparent check-key and usage tracking make cost control effortless.\""},
	"tm3_by":         {"vi": "Kỹ sư tự động hóa", "en": "Automation engineer"},

	// ===== Docs preview =====
	"docs_eyebrow":   {"vi": "Tài liệu", "en": "Documentation"},
	"docs_title":     {"vi": "Mọi thứ để tích hợp", "en": "Everything you need to integrate"},
	"docs_auth":      {"vi": "Xác thực", "en": "Authentication"},
	"docs_auth_d":    {"vi": "Bearer token qua header Authorization.", "en": "Bearer token via the Authorization header."},
	"docs_models":    {"vi": "Models", "en": "Models"},
	"docs_models_d":  {"vi": "Liệt kê model khả dụng qua /v1/models.", "en": "List available models via /v1/models."},
	"docs_limits":    {"vi": "Giới hạn & credit", "en": "Limits & credits"},
	"docs_limits_d":  {"vi": "Credit trừ theo mức Kiro báo, xem realtime.", "en": "Credits deducted as Kiro reports, viewable in real time."},
	"docs_errors":    {"vi": "Xử lý lỗi", "en": "Error handling"},
	"docs_errors_d":  {"vi": "Mã lỗi chuẩn HTTP + body JSON rõ ràng.", "en": "Standard HTTP error codes with clear JSON bodies."},

	// ===== Final CTA =====
	"cta_title":      {"vi": "Sẵn sàng dùng API Kiro?", "en": "Ready to build with the Kiro API?"},
	"cta_sub":        {"vi": "Mua gói, nhận key tức thì và gọi request đầu tiên trong vài phút.", "en": "Buy a plan, get your key instantly, and make your first request in minutes."},
	"cta_btn":        {"vi": "Bắt đầu ngay", "en": "Get started"},

	// ===== FAQ extra =====
	"faq_q5":  {"vi": "Endpoint API là gì và có tương thích OpenAI không?", "en": "What is the API endpoint and is it OpenAI-compatible?"},
	"faq_a5":  {"vi": "Có. Endpoint tương thích OpenAI hoàn toàn: đặt base URL của chúng tôi và API key vào bất kỳ SDK OpenAI nào (hoặc công cụ như Cursor, VS Code) là dùng được ngay, không cần đổi code.", "en": "Yes. The endpoint is fully OpenAI-compatible: point any OpenAI SDK (or a tool like Cursor or VS Code) at our base URL with your API key and it works with no code changes."},
	"faq_q6":  {"vi": "Tôi dùng được với Cursor / VS Code / Claude Code không?", "en": "Can I use it with Cursor / VS Code / Claude Code?"},
	"faq_a6":  {"vi": "Được. Bất kỳ công cụ nào cho phép cấu hình base URL tương thích OpenAI đều dùng được — Cursor, VS Code (Continue/Cline), Claude Code, Roo Code, OpenHands, Codex và nhiều công cụ khác.", "en": "Yes. Any tool that lets you set a custom OpenAI-compatible base URL works — Cursor, VS Code (Continue/Cline), Claude Code, Roo Code, OpenHands, Codex and more."},
	"faq_q7":  {"vi": "Có hỗ trợ streaming không?", "en": "Do you support streaming?"},
	"faq_a7":  {"vi": "Có. Chúng tôi hỗ trợ streaming SSE để nhận token trả về theo thời gian thực, giống API OpenAI.", "en": "Yes. We support SSE streaming so you get tokens in real time, just like the OpenAI API."},
	"faq_q8":  {"vi": "Credit được tính như thế nào?", "en": "How are credits calculated?"},
	"faq_a8":  {"vi": "Credit trừ đúng bằng mức Kiro báo cho mỗi request — chính xác 100%, không cộng phụ phí. Bạn xem realtime trong dashboard và trang check-key.", "en": "Credits are deducted exactly as Kiro reports for each request — 100% accurate, no surcharge. You can see it in real time in your dashboard and on the check-key page."},
	"faq_q9":  {"vi": "Key có bị lộ/không an toàn không?", "en": "Are my keys secure?"},
	"faq_a9":  {"vi": "Key được lưu mã hóa, xác thực constant-time, kèm rate-limit và captcha chống dò. Bạn có thể tra cứu trạng thái key bất kỳ lúc nào.", "en": "Keys are stored encrypted with constant-time authentication, plus rate-limiting and captcha to prevent brute-forcing. You can check a key's status anytime."},
	"faq_q10": {"vi": "Tôi thanh toán bằng cách nào?", "en": "How do I pay?"},
	"faq_a10": {"vi": "Chuyển khoản ngân hàng (VND) tự động qua SePay, USDT (BEP20) và PayPal. Nhiều cổng giao key tự động ngay sau khi nhận thanh toán.", "en": "Automatic bank transfer (VND) via SePay, USDT (BEP20), and PayPal. Several gateways deliver your key automatically once payment is received."},
}

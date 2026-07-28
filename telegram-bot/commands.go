package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// command is one parsed instruction from a chat message.
type command struct {
	Name string
	Args []string
}

// parseCommand splits a message into a command and its arguments.
//
// Returns ok=false for anything that is not a slash command, so ordinary chatter is
// ignored instead of producing error replies.
func parseCommand(text string) (command, bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return command{}, false
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return command{}, false
	}
	name := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	// Telegram appends @botname when several bots share a group chat.
	if at := strings.Index(name, "@"); at >= 0 {
		name = name[:at]
	}
	if name == "" {
		return command{}, false
	}
	return command{Name: name, Args: fields[1:]}, true
}

const helpText = `Quản lý API key

/newkey <tên> <giờ> [số lượng]
    Tạo key mới. VD: /newkey khachA 720
/addhours <key|tên> <giờ>
    Cộng thêm giờ. VD: /addhours khachA 168
/pause <key|tên>
    Tạm dừng, giữ lại thời gian còn lại
/resume <key|tên>
    Tiếp tục, tính lại hạn từ bây giờ
/info <key|tên>
    Xem trạng thái một key
/list [active|paused|expired]
    Danh sách key
/help
    Bảng lệnh này

Có thể dùng ID key hoặc một phần tên, miễn là chỉ khớp duy nhất một key.`

// handler executes commands against keyadmin.
type handler struct {
	cfg config
	api *keyadminClient
}

// Handle runs a command and returns the reply text.
//
// Every error is returned as text rather than an error value: the caller's job is to
// deliver a reply either way, and an operator needs to see what went wrong.
func (h *handler) Handle(ctx context.Context, cmd command) string {
	switch cmd.Name {
	case "start", "help":
		return helpText
	case "newkey":
		return h.newKey(ctx, cmd.Args)
	case "addhours":
		return h.addHours(ctx, cmd.Args)
	case "pause":
		return h.pause(ctx, cmd.Args)
	case "resume":
		return h.resume(ctx, cmd.Args)
	case "info":
		return h.info(ctx, cmd.Args)
	case "list":
		return h.list(ctx, cmd.Args)
	default:
		return fmt.Sprintf("Không rõ lệnh /%s. Gõ /help để xem danh sách.", cmd.Name)
	}
}

func (h *handler) newKey(ctx context.Context, args []string) string {
	if len(args) < 2 {
		return "Cách dùng: /newkey <tên> <giờ> [số lượng]\nVD: /newkey khachA 720"
	}
	name := args[0]
	hours, err := strconv.Atoi(args[1])
	if err != nil || hours <= 0 {
		return fmt.Sprintf("Số giờ %q không hợp lệ. Phải là số nguyên dương.", args[1])
	}
	count := 1
	if len(args) >= 3 {
		count, err = strconv.Atoi(args[2])
		if err != nil || count <= 0 {
			return fmt.Sprintf("Số lượng %q không hợp lệ.", args[2])
		}
		if count > 50 {
			// keyadmin allows 500, but that many keys in one chat message is almost
			// certainly a typo, and the reply would be unreadable anyway.
			return "Tối đa 50 key mỗi lần để tránh gõ sai."
		}
	}

	keys, err := h.api.CreateKeys(ctx, name, count, hours)
	if err != nil {
		return "Tạo key thất bại: " + err.Error()
	}
	if len(keys) == 0 {
		return "keyadmin không trả về key nào. Kiểm tra log keyadmin."
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Đã tạo %d key trên %s, hạn %s:\n\n",
		len(keys), h.cfg.SiteLabel, humanDuration(int64(hours)*3600))
	for _, k := range keys {
		fmt.Fprintf(&b, "%s\n%s\n\n", k.Name, k.Key)
	}
	b.WriteString("Lưu key ngay: về sau không xem lại được toàn bộ key.")
	return b.String()
}

func (h *handler) addHours(ctx context.Context, args []string) string {
	if len(args) < 2 {
		return "Cách dùng: /addhours <key|tên> <giờ>\nVD: /addhours khachA 168"
	}
	hours, err := strconv.Atoi(args[1])
	if err != nil || hours == 0 {
		return fmt.Sprintf("Số giờ %q không hợp lệ.", args[1])
	}
	target, reply := h.resolve(ctx, args[0])
	if target == nil {
		return reply
	}
	v, err := h.api.AddHours(ctx, target.ID, hours)
	if err != nil {
		return "Cộng giờ thất bại: " + err.Error()
	}
	verb := "Đã cộng"
	if hours < 0 {
		verb = "Đã trừ"
	}
	return fmt.Sprintf("%s %s cho %s\nTrạng thái: %s",
		verb, humanDuration(int64(abs(hours))*3600), v.Name, describeExpiry(v.Expiry))
}

func (h *handler) pause(ctx context.Context, args []string) string {
	if len(args) < 1 {
		return "Cách dùng: /pause <key|tên>"
	}
	target, reply := h.resolve(ctx, args[0])
	if target == nil {
		return reply
	}
	v, err := h.api.Pause(ctx, target.ID)
	if err != nil {
		return "Tạm dừng thất bại: " + err.Error()
	}
	return fmt.Sprintf("Đã tạm dừng %s\nGiữ lại: %s", v.Name, describeExpiry(v.Expiry))
}

func (h *handler) resume(ctx context.Context, args []string) string {
	if len(args) < 1 {
		return "Cách dùng: /resume <key|tên>"
	}
	target, reply := h.resolve(ctx, args[0])
	if target == nil {
		return reply
	}
	v, err := h.api.Resume(ctx, target.ID)
	if err != nil {
		return "Tiếp tục thất bại: " + err.Error()
	}
	return fmt.Sprintf("Đã tiếp tục %s\nTrạng thái: %s", v.Name, describeExpiry(v.Expiry))
}

func (h *handler) info(ctx context.Context, args []string) string {
	if len(args) < 1 {
		return "Cách dùng: /info <key|tên>"
	}
	v, reply := h.resolve(ctx, args[0])
	if v == nil {
		return reply
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", v.Name)
	fmt.Fprintf(&b, "ID: %s\n", v.ID)
	fmt.Fprintf(&b, "Bật: %v\n", v.Enabled)
	fmt.Fprintf(&b, "Hạn: %s\n", describeExpiry(v.Expiry))
	if v.TokenLimit > 0 {
		fmt.Fprintf(&b, "Token: %d / %d\n", v.TokensUsed, v.TokenLimit)
	} else {
		fmt.Fprintf(&b, "Token đã dùng: %d (không giới hạn)\n", v.TokensUsed)
	}
	if v.CreditLimit > 0 {
		fmt.Fprintf(&b, "Credit: %.2f / %.2f\n", v.CreditsUsed, v.CreditLimit)
	} else {
		fmt.Fprintf(&b, "Credit đã dùng: %.2f (không giới hạn)\n", v.CreditsUsed)
	}
	return b.String()
}

func (h *handler) list(ctx context.Context, args []string) string {
	keys, err := h.api.List(ctx)
	if err != nil {
		return "Không lấy được danh sách: " + err.Error()
	}
	filter := ""
	if len(args) > 0 {
		filter = strings.ToLower(strings.TrimSpace(args[0]))
	}

	var shown []keyView
	for _, k := range keys {
		if filter != "" && !matchesFilter(k, filter) {
			continue
		}
		shown = append(shown, k)
	}
	if len(shown) == 0 {
		if filter != "" {
			return fmt.Sprintf("Không có key nào ở trạng thái %q.", filter)
		}
		return "Chưa có key nào."
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d key trên %s:\n\n", len(shown), h.cfg.SiteLabel)
	for _, k := range shown {
		state := describeExpiry(k.Expiry)
		if !k.Enabled {
			state = "đã tắt, " + state
		}
		fmt.Fprintf(&b, "%s\n  %s\n", k.Name, state)
	}
	return b.String()
}

// matchesFilter narrows /list output. Mirrors keyadmin's own vocabulary so the two
// interfaces do not disagree about what "active" means.
func matchesFilter(k keyView, filter string) bool {
	expired := k.Expiry.Mode == "active" && k.Expiry.SecondsLeft <= 0
	switch filter {
	case "active":
		return k.Expiry.Mode == "active" && !expired
	case "paused":
		return k.Expiry.Mode == "paused"
	case "expired":
		return expired
	case "permanent":
		return k.Expiry.Mode == "none"
	default:
		return true
	}
}

// resolve finds the key a command refers to, by exact ID or by name fragment.
//
// An ambiguous fragment is refused rather than resolved to the first match: these
// commands change what a paying customer can do, so acting on a guess is worse than
// asking again. Returns (nil, reply) when the caller should send reply instead.
func (h *handler) resolve(ctx context.Context, ref string) (*keyView, string) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, "Thiếu key hoặc tên."
	}
	keys, err := h.api.List(ctx)
	if err != nil {
		return nil, "Không tra được key: " + err.Error()
	}

	// Exact ID wins outright, so a full ID is never treated as a fragment.
	for i := range keys {
		if keys[i].ID == ref {
			return &keys[i], ""
		}
	}
	// Then an exact name.
	for i := range keys {
		if strings.EqualFold(keys[i].Name, ref) {
			return &keys[i], ""
		}
	}

	var matches []int
	for i := range keys {
		if containsFold(keys[i].Name, ref) {
			matches = append(matches, i)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Sprintf("Không tìm thấy key nào khớp %q. Gõ /list để xem.", ref)
	case 1:
		return &keys[matches[0]], ""
	default:
		var b strings.Builder
		fmt.Fprintf(&b, "%q khớp %d key, hãy nói rõ hơn:\n", ref, len(matches))
		for _, i := range matches {
			if b.Len() > 3000 {
				b.WriteString("...\n")
				break
			}
			fmt.Fprintf(&b, "  %s\n", keys[i].Name)
		}
		return nil, b.String()
	}
}

// containsFold reports whether s contains substr, ignoring case.
//
// Key names are typed by hand into a chat, often on a phone, so matching has to
// tolerate case differences.
func containsFold(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

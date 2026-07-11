// docs.go - mo hinh du lieu + render markdown toi gian cho trang tai lieu /docs.
package main

import (
	"html"
	"regexp"
	"strings"
	"time"
)

// Doc la mot bai tai lieu do admin soan.
type Doc struct {
	ID          int64
	Slug        string
	Category    string
	Title       string
	Body        string // markdown tho
	SortOrder   int
	IsPublished bool
	UpdatedAt   int64
}

// UpdatedTime dinh dang ngay cap nhat.
func (d Doc) UpdatedTime() string {
	return time.Unix(d.UpdatedAt, 0).Format("02/01/2006 15:04")
}

// listDocs tra ve danh sach docs. Neu publishedOnly=true chi lay bai da xuat ban.
func (app *App) listDocs(publishedOnly bool) []Doc {
	q := `SELECT id,slug,category,title,body,sort_order,is_published,updated_at FROM docs`
	if publishedOnly {
		q += ` WHERE is_published=1`
	}
	q += ` ORDER BY sort_order ASC, id ASC`
	rows, err := app.db.Query(q)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Doc
	for rows.Next() {
		var d Doc
		var pub int
		if err := rows.Scan(&d.ID, &d.Slug, &d.Category, &d.Title, &d.Body, &d.SortOrder, &pub, &d.UpdatedAt); err != nil {
			continue
		}
		d.IsPublished = pub == 1
		out = append(out, d)
	}
	return out
}

// findDoc tim 1 bai theo slug.
func (app *App) findDoc(slug string) (Doc, bool) {
	var d Doc
	var pub int
	err := app.db.QueryRow(
		`SELECT id,slug,category,title,body,sort_order,is_published,updated_at FROM docs WHERE slug=?`, slug,
	).Scan(&d.ID, &d.Slug, &d.Category, &d.Title, &d.Body, &d.SortOrder, &pub, &d.UpdatedAt)
	if err != nil {
		return Doc{}, false
	}
	d.IsPublished = pub == 1
	return d, true
}

// findDocByID tim 1 bai theo id (cho admin edit).
func (app *App) findDocByID(id int64) (Doc, bool) {
	var d Doc
	var pub int
	err := app.db.QueryRow(
		`SELECT id,slug,category,title,body,sort_order,is_published,updated_at FROM docs WHERE id=?`, id,
	).Scan(&d.ID, &d.Slug, &d.Category, &d.Title, &d.Body, &d.SortOrder, &pub, &d.UpdatedAt)
	if err != nil {
		return Doc{}, false
	}
	d.IsPublished = pub == 1
	return d, true
}

var (
	reHeading  = regexp.MustCompile(`^(#{1,4})\s+(.*)$`)
	reBold     = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	reItalic   = regexp.MustCompile(`\*([^*]+)\*`)
	reInline   = regexp.MustCompile("`([^`]+)`")
	reLink     = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	reSlugStrip = regexp.MustCompile(`[^a-z0-9]+`)
)

// slugify chuyen tieu de thanh slug an toan.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = reSlugStrip.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// renderInline xu ly cac dinh dang inline sau khi da escape HTML.
func renderInline(s string) string {
	s = html.EscapeString(s)
	s = reInline.ReplaceAllString(s, "<code>$1</code>")
	s = reBold.ReplaceAllString(s, "<strong>$1</strong>")
	s = reItalic.ReplaceAllString(s, "<em>$1</em>")
	// Link: escape da chay nen dau ngoac van an toan; chi cho phep http/https va duong dan noi bo.
	s = reLink.ReplaceAllStringFunc(s, func(m string) string {
		sub := reLink.FindStringSubmatch(m)
		text, href := sub[1], sub[2]
		if !strings.HasPrefix(href, "http://") && !strings.HasPrefix(href, "https://") && !strings.HasPrefix(href, "/") && !strings.HasPrefix(href, "#") {
			return text
		}
		return `<a href="` + href + `">` + text + `</a>`
	})
	return s
}

// renderMarkdown chuyen markdown tho thanh HTML an toan (da escape).
// Ho tro: heading (#..####), list (-/*), code block (```), blockquote (>), doan van, inline.
func renderMarkdown(md string) string {
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	var b strings.Builder
	inCode := false
	inList := false
	var para []string

	flushPara := func() {
		if len(para) > 0 {
			b.WriteString("<p>" + strings.Join(para, "<br>") + "</p>\n")
			para = nil
		}
	}
	closeList := func() {
		if inList {
			b.WriteString("</ul>\n")
			inList = false
		}
	}

	for _, ln := range lines {
		// Code fence
		if strings.HasPrefix(strings.TrimSpace(ln), "```") {
			flushPara()
			closeList()
			if !inCode {
				b.WriteString("<pre class=\"doc-code\"><code>")
				inCode = true
			} else {
				b.WriteString("</code></pre>\n")
				inCode = false
			}
			continue
		}
		if inCode {
			b.WriteString(html.EscapeString(ln) + "\n")
			continue
		}

		trimmed := strings.TrimSpace(ln)
		if trimmed == "" {
			flushPara()
			closeList()
			continue
		}

		// Heading
		if m := reHeading.FindStringSubmatch(ln); m != nil {
			flushPara()
			closeList()
			level := len(m[1])
			text := renderInline(strings.TrimSpace(m[2]))
			id := slugify(m[2])
			lv := string(rune('0' + level))
			b.WriteString("<h" + lv + " id=\"" + id + "\">" + text + "</h" + lv + ">\n")
			continue
		}

		// List item
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
			flushPara()
			if !inList {
				b.WriteString("<ul>\n")
				inList = true
			}
			b.WriteString("<li>" + renderInline(trimmed[2:]) + "</li>\n")
			continue
		}

		// Blockquote
		if strings.HasPrefix(trimmed, "> ") {
			flushPara()
			closeList()
			b.WriteString("<blockquote>" + renderInline(trimmed[2:]) + "</blockquote>\n")
			continue
		}

		// Paragraph line
		closeList()
		para = append(para, renderInline(trimmed))
	}
	flushPara()
	closeList()
	if inCode {
		b.WriteString("</code></pre>\n")
	}
	return b.String()
}

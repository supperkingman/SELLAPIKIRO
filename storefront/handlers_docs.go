// handlers_docs.go - trang tai lieu cong khai (/docs) + quan tri (/frontadmin/docs).
package main

import (
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// handleDocsIndex: trang danh sach tai lieu cong khai.
func (app *App) handleDocsIndex(w http.ResponseWriter, r *http.Request) {
	docs := app.listDocs(true)
	app.render(w, "docs_index.html", app.pageData(r, map[string]interface{}{
		"Docs": docs,
	}))
}

// handleDocDetail: xem 1 bai theo slug tai /docs/<slug>.
func (app *App) handleDocDetail(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimPrefix(r.URL.Path, "/docs/")
	slug = strings.Trim(slug, "/")
	if slug == "" {
		http.Redirect(w, r, "/docs", http.StatusSeeOther)
		return
	}
	d, ok := app.findDoc(slug)
	if !ok || !d.IsPublished {
		w.WriteHeader(http.StatusNotFound)
		app.render(w, "docs_index.html", app.pageData(r, map[string]interface{}{
			"Docs":     app.listDocs(true),
			"NotFound": true,
		}))
		return
	}
	app.render(w, "doc_detail.html", app.pageData(r, map[string]interface{}{
		"Doc":      d,
		"Docs":     app.listDocs(true),
		"BodyHTML": template.HTML(renderMarkdown(d.Body)),
	}))
}

// ===== Admin =====

// handleAdminDocs: danh sach docs trong admin.
func (app *App) handleAdminDocs(w http.ResponseWriter, r *http.Request) {
	app.render(w, "admin_docs.html", app.adminPage(r, map[string]interface{}{
		"Active": "docs",
		"Title":  "Tài liệu",
		"Docs":   app.listDocs(false),
	}))
}

// handleAdminDocEdit: form tao/sua 1 bai.
func (app *App) handleAdminDocEdit(w http.ResponseWriter, r *http.Request) {
	var d Doc
	d.IsPublished = true // mac dinh xuat ban khi tao moi
	if idStr := r.URL.Query().Get("id"); idStr != "" {
		id, _ := strconv.ParseInt(idStr, 10, 64)
		if found, ok := app.findDocByID(id); ok {
			d = found
		}
	}
	app.render(w, "admin_doc_edit.html", app.adminPage(r, map[string]interface{}{
		"Active": "docs",
		"Title":  "Soạn tài liệu",
		"Doc":    d,
	}))
}

// handleAdminDocSave: luu (tao moi hoac cap nhat) 1 bai.
func (app *App) handleAdminDocSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/frontadmin/docs", http.StatusSeeOther)
		return
	}
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	title := strings.TrimSpace(r.FormValue("title"))
	slug := strings.TrimSpace(r.FormValue("slug"))
	if slug == "" {
		slug = slugify(title)
	} else {
		slug = slugify(slug)
	}
	category := strings.TrimSpace(r.FormValue("category"))
	body := r.FormValue("body")
	sortOrder, _ := strconv.Atoi(r.FormValue("sort_order"))
	published := 0
	if r.FormValue("is_published") == "on" {
		published = 1
	}
	now := time.Now().Unix()

	if id > 0 {
		_, _ = app.db.Exec(
			`UPDATE docs SET slug=?,category=?,title=?,body=?,sort_order=?,is_published=?,updated_at=? WHERE id=?`,
			slug, category, title, body, sortOrder, published, now, id,
		)
	} else {
		_, _ = app.db.Exec(
			`INSERT INTO docs(slug,category,title,body,sort_order,is_published,updated_at) VALUES(?,?,?,?,?,?,?)`,
			slug, category, title, body, sortOrder, published, now,
		)
	}
	http.Redirect(w, r, "/frontadmin/docs", http.StatusSeeOther)
}

// handleAdminDocDelete: xoa 1 bai.
func (app *App) handleAdminDocDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/frontadmin/docs", http.StatusSeeOther)
		return
	}
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	_, _ = app.db.Exec(`DELETE FROM docs WHERE id=?`, id)
	http.Redirect(w, r, "/frontadmin/docs", http.StatusSeeOther)
}

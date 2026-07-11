// seo.go - phuc vu robots.txt va sitemap.xml cho SEO.
package main

import (
	"fmt"
	"net/http"
	"strings"
)

// handleRobots tra ve robots.txt, tro toi sitemap.
func (app *App) handleRobots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "User-agent: *\nAllow: /\nDisallow: /admin\nDisallow: /dashboard\nDisallow: /order/\nDisallow: /checkout\n\nSitemap: %s/sitemap.xml\n", app.baseURL)
}

// handleSitemap tra ve sitemap.xml cho cac trang cong khai.
func (app *App) handleSitemap(w http.ResponseWriter, r *http.Request) {
	pages := []struct {
		loc  string
		prio string
	}{
		{"/", "1.0"},
		{"/pricing", "0.9"},
		{"/login", "0.5"},
		{"/register", "0.6"},
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
	for _, p := range pages {
		b.WriteString("  <url>\n")
		b.WriteString(fmt.Sprintf("    <loc>%s%s</loc>\n", app.baseURL, p.loc))
		b.WriteString(fmt.Sprintf("    <changefreq>weekly</changefreq>\n    <priority>%s</priority>\n", p.prio))
		b.WriteString("  </url>\n")
	}
	b.WriteString(`</urlset>` + "\n")
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

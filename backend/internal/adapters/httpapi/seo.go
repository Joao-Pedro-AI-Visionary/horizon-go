package httpapi

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jpdev/horizon-go/internal/domain"
)

func validHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" || len(host) > 253 {
		return ""
	}
	for _, r := range host {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == ':' {
			continue
		}
		return ""
	}
	return host
}

func safeHTTPURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	if u.Host == "" {
		return ""
	}
	return u.String()
}

func (s *Server) publicSiteBase(r *http.Request) string {
	if b := strings.TrimRight(strings.TrimSpace(s.FrontURL), "/"); b != "" {
		return b
	}
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	if h := validHost(r.Host); h != "" {
		return scheme + "://" + h
	}
	return "https://horizon-web-eight.vercel.app"
}

func (s *Server) apiBase(r *http.Request) string {
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	if h := validHost(r.Host); h != "" {
		return scheme + "://" + h
	}
	return s.publicSiteBase(r)
}

func urlHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Hostname(), "www.")
}

type rssDoc struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	AtomNS  string     `xml:"xmlns:atom,attr"`
	Channel rssChannel `xml:"channel"`
}

type rssAtomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
}

type rssChannel struct {
	Title       string      `xml:"title"`
	Link        string      `xml:"link"`
	Description string      `xml:"description"`
	Language    string      `xml:"language"`
	LastBuild   string      `xml:"lastBuildDate,omitempty"`
	AtomLink    rssAtomLink `xml:"atom:link"`
	Items       []rssItem   `xml:"item"`
}

type rssItem struct {
	Title       string   `xml:"title"`
	Link        string   `xml:"link"`
	GUID        rssGUID  `xml:"guid"`
	PubDate     string   `xml:"pubDate,omitempty"`
	Description string   `xml:"description"`
	Categories  []string `xml:"category,omitempty"`
}

type rssGUID struct {
	IsPermaLink string `xml:"isPermaLink,attr"`
	Value       string `xml:",chardata"`
}

func writeXML(w http.ResponseWriter, contentType string, doc any) {
	b, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "xml serialization failed")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=300, stale-while-revalidate=600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(b)
}

func (s *Server) rssFeed(w http.ResponseWriter, r *http.Request) {
	page, err := s.Store.Feed(r.Context(), domain.FeedQuery{
		Sort:     domain.SortRecent,
		Page:     1,
		PageSize: 30,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	site := s.publicSiteBase(r)
	self := s.apiBase(r) + r.URL.Path
	items := make([]rssItem, 0, len(page.Items))
	for _, it := range page.Items {
		when := it.ProjectedAt
		if it.PublishedAt != nil && !it.PublishedAt.IsZero() {
			when = *it.PublishedAt
		}
		itemURL := fmt.Sprintf("%s/items/%d", site, it.ItemID)
		desc := strings.TrimSpace(it.Summary)
		if desc == "" {
			desc = strings.TrimSpace(it.ScoreReason)
		}
		desc = fmt.Sprintf("[%s · Nota %.1f/10] %s", it.SourceName, it.Score, desc)
		items = append(items, rssItem{
			Title:       it.Title,
			Link:        itemURL,
			GUID:        rssGUID{IsPermaLink: "true", Value: itemURL},
			PubDate:     when.UTC().Format(time.RFC1123Z),
			Description: strings.TrimSpace(desc),
			Categories:  it.Tags,
		})
	}
	doc := rssDoc{
		Version: "2.0",
		AtomNS:  "http://www.w3.org/2005/Atom",
		Channel: rssChannel{
			Title:       "Radar by JpDev — Feed",
			Link:        site + "/",
			Description: "Notícias de tecnologia e engenharia curadas e pontuadas por IA.",
			Language:    "pt-br",
			LastBuild:   time.Now().UTC().Format(time.RFC1123Z),
			AtomLink:    rssAtomLink{Href: self, Rel: "self", Type: "application/rss+xml"},
			Items:       items,
		},
	}
	writeXML(w, "application/rss+xml; charset=utf-8", doc)
}

func (s *Server) rssDigests(w http.ResponseWriter, r *http.Request) {
	ds, err := s.Store.ListDigests(r.Context(), 20)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	site := s.publicSiteBase(r)
	self := s.apiBase(r) + r.URL.Path
	items := make([]rssItem, 0, len(ds))
	for _, d := range ds {
		u := fmt.Sprintf("%s/digests/%s", site, url.PathEscape(d.Date))
		when := d.UpdatedAt
		if when.IsZero() {
			if parsed, perr := time.Parse(time.DateOnly, d.Date); perr == nil {
				when = parsed
			}
		}
		var pub string
		if !when.IsZero() {
			pub = when.UTC().Format(time.RFC1123Z)
		}
		items = append(items, rssItem{
			Title:       d.Title,
			Link:        u,
			GUID:        rssGUID{IsPermaLink: "true", Value: u},
			PubDate:     pub,
			Description: fmt.Sprintf("Edição diária de %s com %d notícias selecionadas (%d avaliadas).", d.Date, d.ItemCount, d.Considered),
		})
	}
	doc := rssDoc{
		Version: "2.0",
		AtomNS:  "http://www.w3.org/2005/Atom",
		Channel: rssChannel{
			Title:       "Radar by JpDev — Digests",
			Link:        site + "/digests",
			Description: "Edições diárias do resumo curado do Radar by JpDev.",
			Language:    "pt-br",
			LastBuild:   time.Now().UTC().Format(time.RFC1123Z),
			AtomLink:    rssAtomLink{Href: self, Rel: "self", Type: "application/rss+xml"},
			Items:       items,
		},
	}
	writeXML(w, "application/rss+xml; charset=utf-8", doc)
}

type sitemapURLSet struct {
	XMLName xml.Name     `xml:"urlset"`
	XMLNS   string       `xml:"xmlns,attr"`
	URLs    []sitemapURL `xml:"url"`
}

type sitemapURL struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}

func (s *Server) sitemapXML(w http.ResponseWriter, r *http.Request) {
	site := s.publicSiteBase(r)
	urls := []sitemapURL{
		{Loc: site + "/"},
		{Loc: site + "/digests"},
		{Loc: site + "/metrics"},
	}
	if page, err := s.Store.Feed(r.Context(), domain.FeedQuery{Sort: domain.SortRecent, Page: 1, PageSize: 100}); err == nil {
		for _, it := range page.Items {
			when := it.ProjectedAt
			if it.PublishedAt != nil && !it.PublishedAt.IsZero() {
				when = *it.PublishedAt
			}
			u := sitemapURL{Loc: fmt.Sprintf("%s/items/%d", site, it.ItemID)}
			if !when.IsZero() {
				u.LastMod = when.UTC().Format(time.DateOnly)
			}
			urls = append(urls, u)
		}
	}
	if ds, err := s.Store.ListDigests(r.Context(), 30); err == nil {
		for _, d := range ds {
			urls = append(urls, sitemapURL{Loc: fmt.Sprintf("%s/digests/%s", site, url.PathEscape(d.Date)), LastMod: d.Date})
		}
	}
	writeXML(w, "application/xml; charset=utf-8", sitemapURLSet{
		XMLNS: "http://www.sitemaps.org/schemas/sitemap/0.9",
		URLs:  urls,
	})
}

var aiSearchBots = []string{
	"OAI-SearchBot",
	"ChatGPT-User",
	"GPTBot",
	"Claude-SearchBot",
	"Claude-User",
	"ClaudeBot",
	"PerplexityBot",
	"Perplexity-User",
	"Google-Extended",
	"Applebot-Extended",
}

func robotsTxtBody(sitemapBase string) string {
	var b strings.Builder
	b.WriteString("# Radar by JpDev — regras para buscadores e IAs\n")
	b.WriteString("# Content-Signal: https://contentsignals.org/\n")
	b.WriteString("Content-Signal: search=yes, ai-input=yes, ai-train=yes\n\n")
	b.WriteString("User-agent: *\n")
	b.WriteString("Allow: /\n")
	for _, path := range []string{"/login", "/sources", "/forgot", "/reset", "/api/admin", "/api/pipeline", "/api/newsletter/send", "/api/items/ingest"} {
		fmt.Fprintf(&b, "Disallow: %s\n", path)
	}
	b.WriteString("\n# Robôs de busca e citação das IAs (permissão explícita).\n")
	for _, bot := range aiSearchBots {
		fmt.Fprintf(&b, "\nUser-agent: %s\nAllow: /\n", bot)
	}
	fmt.Fprintf(&b, "\nSitemap: %s/sitemap.xml\n", strings.TrimRight(sitemapBase, "/"))
	return b.String()
}

func (s *Server) robotsTxt(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(robotsTxtBody(s.publicSiteBase(r))))
}

func llmsTxtBody(site string) string {
	site = strings.TrimRight(site, "/")
	return fmt.Sprintf(`# Radar by JpDev

> Curadoria diária de notícias de tecnologia, engenharia e IA, pontuada automaticamente e publicada em português. Também conhecido como Horizon by JpDev.

O feed é público. Não é preciso criar conta para ler, buscar, ver o digest do dia ou reagir com like e deslike.

## Site

- [Página inicial](%[1]s/): feed principal, busca e seção Em alta
- [Digests](%[1]s/digests): resumo diário das notícias mais relevantes
- [Métricas](%[1]s/metrics): visão do volume coletado e das fontes

## Feeds e descoberta

- [RSS do feed](%[1]s/rss.xml): itens mais recentes do radar
- [RSS dos digests](%[1]s/digests/rss.xml): edições diárias
- [Sitemap](%[1]s/sitemap.xml): URLs canônicas das notícias e dos digests
- [Índice completo para IAs](%[1]s/llms-full.txt): últimas notícias em texto

## Sobre

- [Radar by JpDev](%[1]s/): projeto de João Pedro (JpDev), com nota de IA, digest às 08:00 (horário de Brasília) e páginas próprias por notícia, com link para a fonte original.
- Busca pública: %[1]s/?q={termo}
`, site)
}

func mdLinkText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.ReplaceAll(s, "[", "(")
	s = strings.ReplaceAll(s, "]", ")")
	return s
}

func oneLineSummary(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if rs := []rune(s); len(rs) > 240 {
		return strings.TrimSpace(string(rs[:237])) + "…"
	}
	return s
}

func (s *Server) llmsTxt(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(llmsTxtBody(s.publicSiteBase(r))))
}

func (s *Server) llmsFullTxt(w http.ResponseWriter, r *http.Request) {
	site := s.publicSiteBase(r)
	var b strings.Builder
	b.WriteString(llmsTxtBody(site))
	b.WriteString("\n## Últimas notícias\n\n")
	if s.Store != nil {
		page, err := s.Store.Feed(r.Context(), domain.FeedQuery{Sort: "recent", Page: 1, PageSize: 20})
		if err == nil {
			for _, it := range page.Items {
				title := mdLinkText(it.Title)
				if title == "" {
					continue
				}
				link := fmt.Sprintf("%s/items/%d", strings.TrimRight(site, "/"), it.ItemID)
				if summary := oneLineSummary(it.Summary); summary != "" {
					fmt.Fprintf(&b, "- [%s](%s): %s\n", title, link, summary)
				} else {
					fmt.Fprintf(&b, "- [%s](%s)\n", title, link)
				}
			}
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300, stale-while-revalidate=600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(b.String()))
}

func encodeJSONLD(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	s := string(b)
	s = strings.ReplaceAll(s, "<", `\u003c`)
	s = strings.ReplaceAll(s, ">", `\u003e`)
	s = strings.ReplaceAll(s, "&", `\u0026`)
	return s
}

func newsArticleJSONLD(it domain.ItemDetail, site, canonical, ogImg, desc string) map[string]any {
	site = strings.TrimRight(site, "/")
	org := map[string]any{
		"@type":         "Organization",
		"@id":           site + "/#organization",
		"name":          "Radar by JpDev",
		"alternateName": "Horizon by JpDev",
		"url":           site + "/",
		"logo":          map[string]any{"@type": "ImageObject", "url": "https://github.com/Joao-Pedro-AI-Visionary.png"},
	}
	published := ""
	if it.PublishedAt != nil && !it.PublishedAt.IsZero() {
		published = it.PublishedAt.UTC().Format(time.RFC3339)
	} else if !it.FetchedAt.IsZero() {
		published = it.FetchedAt.UTC().Format(time.RFC3339)
	}
	modified := ""
	if !it.FetchedAt.IsZero() {
		modified = it.FetchedAt.UTC().Format(time.RFC3339)
	} else {
		modified = published
	}
	ld := map[string]any{
		"@context":         "https://schema.org",
		"@type":            "NewsArticle",
		"headline":         it.Title,
		"description":      desc,
		"inLanguage":       "pt-BR",
		"url":              canonical,
		"mainEntityOfPage": map[string]any{"@type": "WebPage", "@id": canonical},
		"image":            []string{ogImg},
		"author":           org,
		"publisher":        org,
	}
	if published != "" {
		ld["datePublished"] = published
	}
	if modified != "" {
		ld["dateModified"] = modified
	}
	if src := safeHTTPURL(it.URL); src != "" {
		ld["isBasedOn"] = src
		name := strings.TrimSpace(it.SourceName)
		if name == "" {
			name = strings.TrimSpace(it.Author)
		}
		if name == "" {
			name = "Fonte original"
		}
		ld["citation"] = map[string]any{"@type": "CreativeWork", "name": name, "url": src}
	}
	return ld
}

func (s *Server) itemPublicHTML(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "id must be a positive integer")
		return
	}
	it, err := s.Store.Item(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	site := s.publicSiteBase(r)
	apiBase := s.apiBase(r)
	canonical := fmt.Sprintf("%s/items/%d", site, it.ID)
	ogImg := fmt.Sprintf("%s/api/og/items/%d.png", apiBase, it.ID)
	desc := strings.TrimSpace(it.Summary)
	if desc == "" {
		desc = strings.TrimSpace(it.ScoreReason)
	}
	if desc == "" {
		desc = fmt.Sprintf("Notícia de %s comentada e pontuada no Radar by JpDev.", it.SourceName)
	}
	if rs := []rune(desc); len(rs) > 160 {
		desc = strings.TrimSpace(string(rs[:157])) + "…"
	}
	title := it.Title + " · Radar by JpDev"
	scoreText := ""
	if it.Score != nil {
		scoreText = fmt.Sprintf(" · Nota %.1f/10", *it.Score)
	}
	linkHTML := ""
	if safeOrig := safeHTTPURL(it.URL); safeOrig != "" {
		linkHTML = fmt.Sprintf(` · <a href="%s" rel="noopener noreferrer" style="color:#415045;">Ler original</a>`, html.EscapeString(safeOrig))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300, stale-while-revalidate=600")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `<!doctype html>
<html lang="pt-BR">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>%[1]s</title>
  <meta name="description" content="%[2]s" />
  <link rel="canonical" href="%[3]s" />
  <meta property="og:site_name" content="Radar by JpDev" />
  <meta property="og:type" content="article" />
  <meta property="og:title" content="%[4]s" />
  <meta property="og:description" content="%[2]s" />
  <meta property="og:url" content="%[3]s" />
  <meta property="og:image" content="%[5]s" />
  <meta property="og:image:width" content="1200" />
  <meta property="og:image:height" content="630" />
  <meta name="twitter:card" content="summary_large_image" />
  <meta name="twitter:title" content="%[4]s" />
  <meta name="twitter:description" content="%[2]s" />
  <meta name="twitter:image" content="%[5]s" />
  <script type="application/ld+json">%[8]s</script>
</head>
<body style="margin:0;padding:32px;background:#eef2ee;font-family:Inter,system-ui,sans-serif;color:#121a14;">
  <main style="max-width:720px;margin:0 auto;background:#ffffff;border-radius:24px;padding:32px;">
    <p style="font-size:13px;font-weight:700;color:#276b43;margin:0 0 8px;">Radar by JpDev%[6]s</p>
    <h1 style="font-size:28px;line-height:1.2;margin:0 0 12px;">%[4]s</h1>
    <p style="font-size:16px;line-height:1.6;color:#415045;margin:0 0 20px;">%[2]s</p>
    <p style="margin:0;"><a href="%[3]s" style="color:#276b43;font-weight:700;">Abrir no Radar</a>%[7]s</p>
  </main>
</body>
</html>`,
		html.EscapeString(title),
		html.EscapeString(desc),
		html.EscapeString(canonical),
		html.EscapeString(it.Title),
		html.EscapeString(ogImg),
		html.EscapeString(scoreText),
		linkHTML,
		encodeJSONLD(newsArticleJSONLD(it, site, canonical, ogImg, desc)),
	)
}

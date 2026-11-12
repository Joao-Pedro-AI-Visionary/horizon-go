package digest

import (
	"bytes"
	"fmt"
	"html"
	"html/template"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jpdev/horizon-go/internal/domain"
)

// Language of the rendered digest.
const Language = "pt-BR"

// Brand is the digest header.
const Brand = "Radar by JpDev"

// View is what the renderers need.
type View struct {
	Date       string // YYYY-MM-DD
	Threshold  float64
	Considered int
	Items      []domain.FeedItem
	Loc        *time.Location
}

// Title is the digest title.
func (v View) Title() string { return Brand + " — " + v.Date }

var (
	mdSpecial    = regexp.MustCompile("([\\\\`*_{}\\[\\]()<>#!|~])")
	mdBlockStart = regexp.MustCompile(`(?m)^( {0,3})(>|[-+] |\d+[.)] )`)
	spaces       = regexp.MustCompile(`\s+`)
)

// EscapeMarkdown renders untrusted text literally on one line (HTML entities
// escaped, Markdown punctuation backslashed, block starters neutralized).
func EscapeMarkdown(s string) string {
	s = strings.TrimSpace(spaces.ReplaceAllString(s, " "))
	s = html.EscapeString(s)
	s = mdSpecial.ReplaceAllString(s, `\$1`)
	return mdBlockStart.ReplaceAllString(s, `$1\$2`)
}

// SafeURL returns raw when it is an absolute http(s) URL without control
// characters, percent-encoding the characters that would break a Markdown
// link; otherwise "".
func SafeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	for _, r := range raw {
		if r < 32 || r == 127 {
			return ""
		}
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	r := strings.NewReplacer(" ", "%20", "(", "%28", ")", "%29", "<", "%3C", ">", "%3E", `"`, "%22", "`", "%60")
	return r.Replace(raw)
}

func formatScore(s float64) string { return fmt.Sprintf("%.1f", s) }

func (v View) loc() *time.Location {
	if v.Loc == nil {
		return time.UTC
	}
	return v.Loc
}

func (v View) when(it domain.FeedItem) string {
	if it.PublishedAt == nil {
		return ""
	}
	return it.PublishedAt.In(v.loc()).Format("02/01 15:04")
}

func dupLabel(n int) string {
	switch n {
	case 0:
		return ""
	case 1:
		return "+1 fonte com a mesma notícia"
	default:
		return fmt.Sprintf("+%d fontes com a mesma notícia", n)
	}
}

func (v View) metaParts(it domain.FeedItem) []string {
	parts := []string{"Nota " + formatScore(it.Score) + "/10", it.SourceKind, it.SourceName}
	if w := v.when(it); w != "" {
		parts = append(parts, w)
	}
	if d := dupLabel(it.DuplicateCount); d != "" {
		parts = append(parts, d)
	}
	return parts
}

func (v View) intro() string {
	return fmt.Sprintf("De %d itens analisados nas últimas 24 horas, %d foram selecionados (nota mínima %s).",
		v.Considered, len(v.Items), formatScore(v.Threshold))
}

const emptyText = "Nenhum item atingiu a nota mínima neste período. Possíveis causas: dia calmo nas fontes acompanhadas, nota mínima alta demais ou poucas fontes configuradas."

// Markdown renders the digest.
func Markdown(v View) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n> %s\n\n---\n\n", EscapeMarkdown(v.Title()), EscapeMarkdown(v.intro()))
	if len(v.Items) == 0 {
		b.WriteString(EscapeMarkdown(emptyText) + "\n")
		return b.String()
	}
	for i, it := range v.Items {
		fmt.Fprintf(&b, "%d. [%s](#item-%d) ⭐ %s/10\n", i+1, EscapeMarkdown(it.Title), i+1, formatScore(it.Score))
	}
	b.WriteString("\n---\n\n")
	for i, it := range v.Items {
		title := EscapeMarkdown(it.Title)
		if u := SafeURL(it.URL); u != "" {
			title = "[" + title + "](" + u + ")"
		}
		fmt.Fprintf(&b, "<a id=\"item-%d\"></a>\n\n## %d. %s\n\n", i+1, i+1, title)
		meta := v.metaParts(it)
		for k := range meta {
			meta[k] = EscapeMarkdown(meta[k])
		}
		fmt.Fprintf(&b, "**%s** · %s\n\n", meta[0], strings.Join(meta[1:], " · "))
		if s := EscapeMarkdown(it.Summary); s != "" {
			b.WriteString(s + "\n\n")
		}
		if r := EscapeMarkdown(it.ScoreReason); r != "" {
			b.WriteString("*Por que importa:* " + r + "\n\n")
		}
		if len(it.Tags) > 0 {
			tags := make([]string, len(it.Tags))
			for k, t := range it.Tags {
				tags[k] = EscapeMarkdown(t)
			}
			b.WriteString("Tags: " + strings.Join(tags, " · ") + "\n\n")
		}
		b.WriteString("---\n\n")
	}
	return b.String()
}

type htmlItem struct {
	N       int
	Title   string
	URL     string
	Score   string
	Meta    []string
	Summary string
	Reason  string
	Tags    []string
}

var page = template.Must(template.New("digest").Parse(`<!doctype html>
<html lang="pt-BR">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
/* Sage palette of the horizon-web design (light, AA contrast). */
:root{color-scheme:light}
*{box-sizing:border-box}
body{margin:0;background:#eef2ee;color:#121a14;font:16px/1.65 Inter,"Segoe UI",system-ui,-apple-system,sans-serif;-webkit-text-size-adjust:100%}
main{max-width:760px;margin:0 auto;padding:clamp(16px,4vw,40px) clamp(14px,4vw,24px)}
h1,h2{font-family:"Plus Jakarta Sans",Inter,"Segoe UI",system-ui,sans-serif;line-height:1.25;letter-spacing:-0.01em;margin:0}
h1{font-size:clamp(24px,5vw,32px);font-weight:800}
h2{font-size:clamp(18px,3.6vw,21px);font-weight:700;margin-bottom:8px;overflow-wrap:anywhere}
p{margin:0 0 10px}
a{color:#276b43;text-decoration:underline;text-decoration-thickness:1px;text-underline-offset:3px}
a:hover{color:#121a14}
a:focus-visible{outline:2px solid #276b43;outline-offset:2px;border-radius:4px}
h2 a{color:#121a14;text-decoration:none}
h2 a:hover{color:#276b43;text-decoration:underline}
.intro{color:#56635a;margin:10px 0 20px;padding:12px 16px;background:#e3f1e6;border-radius:16px}
.toc{background:#fff;border:1px solid #dbe3dc;border-radius:20px;padding:16px 20px 16px 40px;margin:0 0 20px}
.toc li{margin:4px 0}
.toc a{color:#121a14;text-decoration:none}
.toc a:hover{color:#276b43;text-decoration:underline}
.item{background:#fff;border:1px solid rgb(18 26 20 / .06);border-radius:24px;padding:clamp(16px,3.5vw,24px);margin:14px 0;box-shadow:0 1px 2px rgb(18 26 20 / .04),0 12px 32px -12px rgb(18 26 20 / .14)}
.score{display:inline-block;font-family:"JetBrains Mono",ui-monospace,monospace;font-weight:700;font-size:13px;color:#276b43;background:#e3f1e6;border:1px solid rgb(39 107 67 / .25);border-radius:999px;padding:1px 10px}
.meta{color:#56635a;font-size:14px}
.reason{color:#2b352d}
.tag{display:inline-block;color:#121a14;background:#eef2ee;border:1px solid #dbe3dc;border-radius:999px;padding:1px 10px;margin:4px 6px 0 0;font-size:13px}
</style>
</head>
<body>
<main>
<h1>{{.Title}}</h1>
<p class="intro">{{.Intro}}</p>
{{if .Items}}
<ol class="toc">{{range .Items}}<li><a href="#item-{{.N}}">{{.Title}}</a> <span class="meta">{{.Score}}/10</span></li>{{end}}</ol>
{{range .Items}}
<article class="item" id="item-{{.N}}">
<h2>{{.N}}. {{if .URL}}<a href="{{.URL}}" rel="noopener noreferrer" target="_blank">{{.Title}}</a>{{else}}{{.Title}}{{end}}</h2>
<p class="meta"><span class="score">{{.Score}}</span>{{range .Meta}} · {{.}}{{end}}</p>
{{if .Summary}}<p>{{.Summary}}</p>{{end}}
{{if .Reason}}<p class="reason"><strong>Por que importa:</strong> {{.Reason}}</p>{{end}}
{{if .Tags}}<p>{{range .Tags}}<span class="tag">{{.}}</span>{{end}}</p>{{end}}
</article>
{{end}}
{{else}}
<p>{{.Empty}}</p>
{{end}}
</main>
</body>
</html>
`))

// HTML renders the digest as a standalone page (html/template escapes the
// untrusted text and sanitizes the links).
func HTML(v View) (string, error) {
	items := make([]htmlItem, len(v.Items))
	for i, it := range v.Items {
		meta := v.metaParts(it)
		items[i] = htmlItem{
			N: i + 1, Title: it.Title, URL: SafeURL(it.URL), Score: formatScore(it.Score),
			Meta: meta[1:], Summary: it.Summary, Reason: it.ScoreReason, Tags: it.Tags,
		}
	}
	var buf bytes.Buffer
	err := page.Execute(&buf, map[string]any{
		"Title": v.Title(), "Intro": v.intro(), "Items": items, "Empty": emptyText,
	})
	return buf.String(), err
}

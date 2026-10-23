package web

import (
	"fmt"
	"html/template"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/sonic0002/eavesong/internal/store"
)

var sourceNames = map[string]string{
	"hackernews": "Hacker News", "reddit": "Reddit", "x": "X", "devto": "DEV", "searchconsole": "Search Console", "googleanalytics": "Google Analytics", "lobsters": "Lobsters",
	"stackoverflow": "Stack Overflow", "mastodon": "Mastodon", "youtube": "YouTube", "facebook": "Facebook", "googlealerts": "Google Alerts", "github": "GitHub", "bluesky": "Bluesky",
}

// sourceMarks are the short labels on each mention's source badge.
var sourceMarks = map[string]string{
	"hackernews": "HN", "reddit": "R", "x": "X", "devto": "DEV", "searchconsole": "G", "googleanalytics": "A", "lobsters": "L",
	"stackoverflow": "SO", "mastodon": "M", "youtube": "YT", "facebook": "f", "googlealerts": "GA", "github": "GH", "bluesky": "B",
}

var bskyProfile = regexp.MustCompile(`^https://bsky\.app/profile/([^/]+)/post/`)

// contextAuthor is the handle of the person whose post a reply answers, when the link shows it.
func contextAuthor(link string) string {
	if m := bskyProfile.FindStringSubmatch(link); m != nil {
		return m[1]
	}
	return ""
}

func sourceName(s string) string {
	if n, ok := sourceNames[s]; ok {
		return n
	}
	return s
}

func sourceMark(s string) string {
	if m, ok := sourceMarks[s]; ok {
		return m
	}
	return strings.ToUpper(s[:1])
}

// boilerplate is text sources add that says nothing (stored by older versions too).
var boilerplate = regexp.MustCompile(`(?s)submitted by\s+/u/\S+(\s+to\s+\S+)?\s*\[link\]\s*\[comments\]`)

func clean(s string) string {
	return strings.TrimSpace(boilerplate.ReplaceAllString(s, ""))
}

// pulse draws the mentions-per-day line as a small inline SVG. Only numbers
// computed here go into the markup, so it is safe to mark as HTML.
func pulse(days []store.Count, width, height int) template.HTML {
	if len(days) < 2 {
		return ""
	}
	max := 1
	for _, d := range days {
		if d.N > max {
			max = d.N
		}
	}
	step := float64(width-4) / float64(len(days)-1)
	var line, area strings.Builder
	for i, d := range days {
		x := 2 + float64(i)*step
		y := float64(height-3) - float64(d.N)/float64(max)*float64(height-8)
		cmd := "L"
		if i == 0 {
			cmd = "M"
		}
		fmt.Fprintf(&line, "%s%.1f %.1f ", cmd, x, y)
	}
	fmt.Fprintf(&area, "%sL%d %d L2 %d Z", line.String(), width-2, height, height)
	last := days[len(days)-1]
	lx := float64(width - 2)
	ly := float64(height-3) - float64(last.N)/float64(max)*float64(height-8)
	return template.HTML(fmt.Sprintf(`<svg class="pulse-line" viewBox="0 0 %d %d" width="%d" height="%d" aria-hidden="true">`+
		`<path class="pulse-area" d="%s"/><path class="pulse-stroke" d="%s"/><circle class="pulse-dot" cx="%.1f" cy="%.1f" r="3"/></svg>`,
		width, height, width, height, area.String(), line.String(), lx, ly))
}

// bar draws one horizontal bar of a share-of-voice chart.
func bar(n, max int, product bool) template.HTML {
	if max < 1 {
		max = 1
	}
	w := 2 + n*196/max
	class := "bar-competitor"
	if product {
		class = "bar-product"
	}
	return template.HTML(fmt.Sprintf(`<svg viewBox="0 0 200 10" width="200" height="10" aria-hidden="true"><rect class="bar-track" width="200" height="10" rx="2"/><rect class="%s" width="%d" height="10" rx="2"/></svg>`, class, w))
}

// links builds inbox URLs that change one filter and keep the others.
type links struct {
	ProjectID int64
	F         store.Filter
}

func (l links) url(f store.Filter) string {
	q := url.Values{}
	q.Set("status", f.Status)
	if f.Source != "" {
		q.Set("source", f.Source)
	}
	if f.About != "" {
		q.Set("about", f.About)
	}
	if f.IncludeOurs {
		q.Set("ours", "1")
	}
	return "/p/" + strconv.FormatInt(l.ProjectID, 10) + "?" + q.Encode()
}

func (l links) Status(s string) string { f := l.F; f.Status = s; return l.url(f) }
func (l links) Source(s string) string { f := l.F; f.Source = s; return l.url(f) }
func (l links) About(s string) string  { f := l.F; f.About = s; return l.url(f) }
func (l links) Ours() string           { f := l.F; f.IncludeOurs = !f.IncludeOurs; return l.url(f) }

// failure shows an error whose first line is the problem and whose other
// lines, if any, are the steps to fix it.
func failure(msg string) template.HTML {
	sentence := func(s string) string {
		s = strings.TrimSpace(s)
		if s == "" {
			return ""
		}
		s = strings.ToUpper(s[:1]) + s[1:]
		if !strings.HasSuffix(s, ".") && !strings.HasSuffix(s, "?") && !strings.HasSuffix(s, "!") {
			s += "."
		}
		return emphasis(template.HTMLEscapeString(s))
	}
	lines := strings.Split(strings.TrimSpace(msg), "\n")
	var b strings.Builder
	b.WriteString(`<p class="fail-problem">` + sentence(lines[0]) + `</p>`)
	var steps []string
	for _, l := range lines[1:] {
		if strings.TrimSpace(l) != "" {
			steps = append(steps, sentence(l))
		}
	}
	if len(steps) > 0 {
		b.WriteString(`<ol class="fix-steps">`)
		for _, s := range steps {
			b.WriteString("<li>" + s + "</li>")
		}
		b.WriteString(`</ol>`)
	}
	return template.HTML(b.String())
}

var (
	boldMark = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	codeMark = regexp.MustCompile("`([^`]+)`")
)

// emphasis turns **name** into bold and `text` into code in an escaped
// message, for the names of buttons, menus and accounts in fix-it steps.
func emphasis(escaped string) string {
	escaped = boldMark.ReplaceAllString(escaped, "<strong>$1</strong>")
	return codeMark.ReplaceAllString(escaped, "<code>$1</code>")
}

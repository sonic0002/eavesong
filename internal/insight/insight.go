// Package insight turns Search Console data into a short list of things to do,
// each tied to a specific search and page, most valuable first.
package insight

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/sonic0002/eavesong/internal/gsc"
)

// Kinds of recommendation.
const (
	FixTitle = "title"   // ranks well, clicked far less than that position earns
	PushUp   = "push"    // on page two with real demand
	Compete  = "compete" // several of your pages split one search
	Missing  = "missing" // shown far down and never clicked: nothing really answers it
	Rising   = "rising"  // gaining impressions: write more on it while it grows
	Falling  = "falling" // losing impressions: refresh before it drops away
	Sitemap  = "sitemap" // Google reports sitemap errors
)

// Recommendation is one thing to do.
type Recommendation struct {
	Kind   string
	Label  string // short name of the kind, for the badge
	Title  string // what to do, in one line
	Detail string // why, with the numbers
	Query  string
	Page   string
	Value  float64 // rough extra clicks a month, used for ordering
}

// expectedCTR is a typical click-through rate (percent) for organic results
// at a position. Real rates vary a lot by search; this is only used to find
// results that are far below normal.
func expectedCTR(position float64) float64 {
	table := []float64{0, 28, 15, 11, 8, 7, 5, 4, 3, 2.5, 2}
	p := int(math.Round(position))
	switch {
	case p < 1:
		return table[1]
	case p <= 10:
		return table[p]
	case p <= 20:
		return 1
	}
	return 0.3
}

func path(page string) string {
	if i := strings.Index(page, "://"); i >= 0 {
		if j := strings.Index(page[i+3:], "/"); j >= 0 {
			return page[i+3+j:]
		}
	}
	return page
}

// Analyze builds recommendations from a report, most valuable first.
func Analyze(r gsc.Report) []Recommendation {
	var out []Recommendation
	monthly := 30.0 / 28.0 // the report covers 28 days

	// The page that ranks best for each search, and how many pages share it.
	bestPage := map[string]gsc.Row{}
	pagesFor := map[string][]gsc.Row{}
	for _, qp := range r.QueryPages {
		pagesFor[qp.Key] = append(pagesFor[qp.Key], qp)
		if b, ok := bestPage[qp.Key]; !ok || qp.Impressions > b.Impressions {
			bestPage[qp.Key] = qp
		}
	}

	for _, q := range r.Queries {
		if operator(q.Key) {
			continue
		}
		page := bestPage[q.Key].Page
		offTopic := page != "" && !about(page, q.Key)
		switch {
		case offTopic && q.Impressions >= 20 && q.CTR < expectedCTR(q.Position)/2:
			gain := (expectedCTR(min(q.Position, 5)) - q.CTR) / 100 * float64(q.Impressions) * monthly
			out = append(out, Recommendation{Kind: Missing, Label: "Missing page", Query: q.Key, Page: page, Value: gain,
				Title: fmt.Sprintf("Write a page that answers \"%s\"", q.Key),
				Detail: fmt.Sprintf("Google shows %s for it %d times (position %.1f), but that page is about something else, so %s. A page that answers \"%s\" directly would catch these searches.%s",
					path(page), q.Impressions, q.Position, clickers(q.Clicks), q.Key, gainText("It could bring", gain))})
		case q.Position <= 6 && q.Impressions >= 20 && q.CTR < expectedCTR(q.Position)/2:
			gain := (expectedCTR(q.Position) - q.CTR) / 100 * float64(q.Impressions) * monthly
			out = append(out, Recommendation{Kind: FixTitle, Label: "Fix the title", Query: q.Key, Page: page, Value: gain,
				Title: fmt.Sprintf("Rewrite the title and description of %s for \"%s\"", orThe(path(page), "the page that ranks"), q.Key),
				Detail: fmt.Sprintf("It sits at position %.1f but only %.1f%% of %d searchers click (about %.0f%% is usual there). Put \"%s\" in words a searcher would recognise in the title and description.%s",
					q.Position, q.CTR, q.Impressions, expectedCTR(q.Position), q.Key, gainText("That could bring", gain))})
		case q.Position >= 8 && q.Position <= 20 && q.Impressions >= 5:
			gain := max(0, (expectedCTR(5)-q.CTR)/100*float64(q.Impressions)*monthly)
			out = append(out, Recommendation{Kind: PushUp, Label: "Push to page one", Query: q.Key, Page: page, Value: gain,
				Title: fmt.Sprintf("Strengthen %s for \"%s\"", orThe(path(page), "a page"), q.Key),
				Detail: fmt.Sprintf("Shown %d times at position %.1f, on page two. Answer \"%s\" near the top of the page, use the words in a heading, and link to the page from related posts.%s",
					q.Impressions, q.Position, q.Key, gainText("Reaching the top five could bring", gain))})
		case q.Position > 20 && q.Impressions >= 10 && q.Clicks == 0:
			gain := expectedCTR(5) / 100 * float64(q.Impressions) * monthly
			out = append(out, Recommendation{Kind: Missing, Label: "Missing page", Query: q.Key, Page: page, Value: gain * 0.5,
				Title:  fmt.Sprintf("Write a page that answers \"%s\"", q.Key),
				Detail: fmt.Sprintf("Google shows you for it %d times, but only around position %.0f and nobody clicks: nothing on the site answers it directly yet.", q.Impressions, q.Position)})
		}
		if pages := pagesFor[q.Key]; len(pages) >= 2 && q.Impressions >= 20 {
			sort.Slice(pages, func(i, j int) bool { return pages[i].Impressions > pages[j].Impressions })
			if pages[1].Impressions*4 >= pages[0].Impressions { // the second page gets a real share
				out = append(out, Recommendation{Kind: Compete, Label: "Pages compete", Query: q.Key, Page: pages[0].Page, Value: float64(q.Impressions) * 0.01,
					Title:  fmt.Sprintf("Pick one page for \"%s\"", q.Key),
					Detail: fmt.Sprintf("%s and %s both appear for it, which splits Google's attention. Make one the main answer and link to it from the other.", path(pages[0].Page), path(pages[1].Page))})
			}
		}
	}

	// Rising and falling searches between the two halves of the period.
	earlier := map[string]gsc.Row{}
	for _, e := range r.Earlier {
		earlier[e.Key] = e
	}
	for _, now := range r.Recent {
		before := earlier[now.Key]
		switch {
		case now.Impressions >= 30 && now.Impressions >= 2*before.Impressions+10:
			out = append(out, Recommendation{Kind: Rising, Label: "Rising", Query: now.Key, Value: float64(now.Impressions-before.Impressions) * 0.02,
				Title:  fmt.Sprintf("Interest in \"%s\" is growing", now.Key),
				Detail: fmt.Sprintf("Shown %d times in the last two weeks against %d before. Follow up while it grows: a deeper or related post, and links to it from your best pages.", now.Impressions, before.Impressions)})
		}
	}
	for _, before := range r.Earlier {
		now := gsc.Row{}
		for _, n := range r.Recent {
			if n.Key == before.Key {
				now = n
			}
		}
		if before.Impressions >= 40 && now.Impressions*2 < before.Impressions && before.Clicks > 0 {
			out = append(out, Recommendation{Kind: Falling, Label: "Falling", Query: before.Key, Value: float64(before.Clicks) * monthly,
				Title:  fmt.Sprintf("\"%s\" is fading", before.Key),
				Detail: fmt.Sprintf("Shown %d times two weeks ago, %d in the last two. If it was news, that is expected; if it should last, refresh the page with current information.", before.Impressions, now.Impressions)})
		}
	}

	for _, m := range r.Sitemaps {
		if m.Errors > 0 {
			out = append(out, Recommendation{Kind: Sitemap, Label: "Sitemap", Value: 1000,
				Title:  "Fix the sitemap errors Google reports",
				Detail: fmt.Sprintf("%s has %d errors, which can keep new pages out of Google.", m.Path, m.Errors)})
		}
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Value > out[j].Value })
	var kept []Recommendation
	rising := 0
	for _, rec := range out {
		if rec.Kind == Rising {
			if rising++; rising > 3 {
				continue
			}
		}
		kept = append(kept, rec)
	}
	return kept
}

func clickers(n int) string {
	if n == 0 {
		return "nobody clicks"
	}
	return "few people click"
}

// gainText describes a click estimate without false precision.
func gainText(prefix string, gain float64) string {
	switch {
	case gain >= 1.5:
		return fmt.Sprintf(" %s about %.0f more clicks a month.", prefix, gain)
	case gain > 0:
		return fmt.Sprintf(" %s a few more clicks a month.", prefix)
	}
	return ""
}

// operator reports searches that use search operators or quotes; they are
// people researching, not demand to write for.
func operator(q string) bool {
	return strings.ContainsAny(q, `"`) || strings.Contains(q, "site:") || strings.HasPrefix(q, "site.") || strings.Contains(q, "inurl:")
}

var stopWords = map[string]bool{"the": true, "and": true, "for": true, "how": true, "what": true, "with": true, "vs": true, "a": true, "to": true, "in": true, "of": true, "is": true}

// about reports whether a page's address shares most of a search's words, a
// rough sign that the page is about that search.
func about(page, query string) bool {
	p := strings.ToLower(path(page))
	var words, hits int
	for _, w := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool { return r == ' ' || r == '-' || r == '/' }) {
		w = strings.Trim(w, ".?!,")
		if len(w) < 2 || stopWords[w] {
			continue
		}
		words++
		if strings.Contains(p, strings.ReplaceAll(w, ".", "-")) || strings.Contains(p, w) {
			hits++
		}
	}
	return words == 0 || hits*2 >= words
}

func orThe(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// Key identifies the same finding across refreshes even when its numbers change.
func Key(r Recommendation) string {
	data, _ := json.Marshal([]string{r.Kind, r.Query, r.Page})
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

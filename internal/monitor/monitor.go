// Package monitor runs the checks: it asks every source for recent items,
// keeps the ones that really mention a project's domains, keywords or
// competitors, stores them, and asks the AI drafter for reply suggestions.
package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sonic0002/eavesong/internal/ai"
	"github.com/sonic0002/eavesong/internal/ga"
	"github.com/sonic0002/eavesong/internal/gsc"
	"github.com/sonic0002/eavesong/internal/notify"
	"github.com/sonic0002/eavesong/internal/source"
	"github.com/sonic0002/eavesong/internal/store"
)

type Monitor struct {
	Store    *store.Store
	Sources  []source.Source
	Drafter  ai.Drafter // nil when no AI provider is configured
	Interval time.Duration
	Log      *slog.Logger
	Notifier *notify.Service

	trigger chan struct{}
	once    sync.Once
	mu      sync.Mutex   // one check at a time
	cfg     sync.RWMutex // guards Sources, Drafter and search, which settings can replace

	search    []*gsc.Client
	analytics []*ga.Client

	lastRun map[string]time.Time // project and source -> last fetch, for paced sources
	scopes  map[string][]int64   // your account -> the projects it is used for (missing: all)
	own     map[string]bool      // "source:name" of your connected accounts
	busy    atomic.Bool          // a check is running

	fetching sync.Map // "kind:project ID" -> struct{}, while that data is being fetched
}

// Checking reports whether a check is running right now.
func (m *Monitor) Checking() bool { return m.busy.Load() }

// ConfigureOwn sets your connected accounts, whose posts count as your own.
func (m *Monitor) ConfigureOwn(own map[string]bool) {
	m.cfg.Lock()
	defer m.cfg.Unlock()
	m.own = own
}

func (m *Monitor) isConnected(source, author string) bool {
	m.cfg.RLock()
	defer m.cfg.RUnlock()
	return m.own[source+":"+strings.ToLower(author)]
}

// ConfigureScopes sets which projects each of your accounts is used for.
func (m *Monitor) ConfigureScopes(scopes map[string][]int64) {
	m.cfg.Lock()
	defer m.cfg.Unlock()
	m.scopes = scopes
}

// belongs reports whether an item may go to a project: replies and mentions
// that came through one of your accounts only reach that account's projects.
func (m *Monitor) belongs(projectID int64, it source.Item) bool {
	if it.Account == "" {
		return true
	}
	m.cfg.RLock()
	defer m.cfg.RUnlock()
	projects, limited := m.scopes[it.Account]
	if !limited {
		return true
	}
	for _, p := range projects {
		if p == projectID {
			return true
		}
	}
	return false
}

// due reports whether a paced source (X, YouTube, keyless Stack Overflow) may
// run again for a project, and records the run when it may.
func (m *Monitor) due(projectID int64, src source.Source) bool {
	paced, ok := src.(source.Paced)
	if !ok || paced.MinInterval() == 0 {
		return true
	}
	key := fmt.Sprintf("%d/%s", projectID, src.Name())
	m.cfg.Lock()
	defer m.cfg.Unlock()
	if m.lastRun == nil {
		m.lastRun = map[string]time.Time{}
	}
	if time.Since(m.lastRun[key]) < paced.MinInterval() {
		return false
	}
	m.lastRun[key] = time.Now()
	return true
}

// ConfigureSearch swaps in the Search Console clients after accounts change.
func (m *Monitor) ConfigureSearch(clients []*gsc.Client) {
	m.cfg.Lock()
	defer m.cfg.Unlock()
	m.search = clients
}

// ConfigureAnalytics swaps in the Google Analytics clients after accounts change.
func (m *Monitor) ConfigureAnalytics(clients []*ga.Client) {
	m.cfg.Lock()
	defer m.cfg.Unlock()
	m.analytics = clients
}

// HasAnalytics reports whether any Google Analytics key is connected.
func (m *Monitor) HasAnalytics() bool {
	m.cfg.RLock()
	defer m.cfg.RUnlock()
	return len(m.analytics) > 0
}

// RefreshAnalytics fetches a project's Google Analytics data now and saves it.
// It returns the property used, or an error saying what to fix.
func (m *Monitor) RefreshAnalytics(ctx context.Context, p store.Project) (string, error) {
	m.cfg.RLock()
	clients := m.analytics
	m.cfg.RUnlock()
	if len(clients) == 0 {
		return "", errors.New("connect a Google Analytics key in Settings first")
	}
	var lastErr error
	for _, c := range clients {
		props, err := c.Properties(ctx)
		if err != nil {
			lastErr = err
			continue
		}
		prop, ok := ga.PropertyFor(props, p.Domains)
		if !ok {
			continue
		}
		report, err := c.Fetch(ctx, prop)
		if err != nil {
			lastErr = err
			break
		}
		data, _ := json.Marshal(report)
		return prop.Name, m.Store.SaveAnalyticsReport(ctx, p.ID, string(data), "")
	}
	if lastErr == nil {
		d := strings.Join(p.Domains, ", ")
		lastErr = fmt.Errorf("none of the connected keys can read a Google Analytics property for %s\n"+
			"Open **Google Analytics** and select the property whose web data stream measures **%s**\n"+
			"Go to **Admin**, then **Property access management**, and add a user\n"+
			"Add `%s` with the **Viewer** role\n"+
			"Come back to this tab. Eavesong tries again when you open it", d, d, emails(clients))
	}
	if err := m.Store.SaveAnalyticsReport(ctx, p.ID, "", lastErr.Error()); err != nil {
		return "", err
	}
	return "", lastErr
}

// RefreshAnalyticsInBackground starts fetching a project's Analytics data
// unless a fetch for it is already running. It reports whether one is running.
func (m *Monitor) RefreshAnalyticsInBackground(p store.Project) bool {
	if !m.HasAnalytics() {
		return false
	}
	return m.inBackground("analytics", p, m.RefreshAnalytics)
}

// AnalyticsFetching reports whether a project's Analytics data is being fetched.
func (m *Monitor) AnalyticsFetching(projectID int64) bool {
	_, ok := m.fetching.Load(fetchKey("analytics", projectID))
	return ok
}

func fetchKey(kind string, projectID int64) string {
	return kind + ":" + strconv.FormatInt(projectID, 10)
}

// inBackground runs one fetch per kind and project at a time.
func (m *Monitor) inBackground(kind string, p store.Project, fetch func(context.Context, store.Project) (string, error)) bool {
	key := fetchKey(kind, p.ID)
	if _, running := m.fetching.LoadOrStore(key, struct{}{}); running {
		return true
	}
	go func() {
		defer m.fetching.Delete(key)
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		if property, err := fetch(ctx, p); err != nil {
			m.Log.Warn(kind, "project", p.Name, "err", err)
		} else {
			m.Log.Info(kind, "project", p.Name, "property", property)
		}
	}()
	return true
}

// HasSearch reports whether any Search Console key is connected.
func (m *Monitor) HasSearch() bool {
	m.cfg.RLock()
	defer m.cfg.RUnlock()
	return len(m.search) > 0
}

// RefreshSearch fetches a project's Search Console data now and saves it.
// It returns the property used, or an error saying what to fix.
func (m *Monitor) RefreshSearch(ctx context.Context, p store.Project) (string, error) {
	m.cfg.RLock()
	clients := m.search
	m.cfg.RUnlock()
	if len(clients) == 0 {
		return "", errors.New("connect a Search Console key in Settings first")
	}
	var lastErr error
	for _, c := range clients {
		sites, err := c.Sites(ctx)
		if err != nil {
			lastErr = err
			continue
		}
		property := gsc.PropertyFor(sites, p.Domains)
		if property == "" {
			continue
		}
		report, err := c.Fetch(ctx, property)
		if err != nil {
			lastErr = err
			break
		}
		data, _ := json.Marshal(report)
		return property, m.Store.SaveSearchReport(ctx, p.ID, string(data), "")
	}
	if lastErr == nil {
		d := strings.Join(p.Domains, ", ")
		lastErr = fmt.Errorf("none of the connected keys can read a Search Console property for %s\n"+
			"Open **Search Console** and select the **%s** property. If it isn't listed, add the site and verify it first\n"+
			"Go to **Settings**, then **Users and permissions**, and click **Add user**\n"+
			"Add `%s` with **Restricted** permission\n"+
			"Come back to this tab. Eavesong tries again when you open it", d, d, emails(clients))
	}
	if err := m.Store.SaveSearchReport(ctx, p.ID, "", lastErr.Error()); err != nil {
		return "", err
	}
	return "", lastErr
}

// RefreshSearchInBackground starts fetching a project's search data unless a
// fetch for it is already running. It reports whether a fetch is running.
func (m *Monitor) RefreshSearchInBackground(p store.Project) bool {
	if !m.HasSearch() {
		return false
	}
	return m.inBackground("search console", p, m.RefreshSearch)
}

// SearchFetching reports whether a project's search data is being fetched.
func (m *Monitor) SearchFetching(projectID int64) bool {
	_, ok := m.fetching.Load(fetchKey("search console", projectID))
	return ok
}

// analyticsDue refreshes Analytics data every 6 hours; Google updates it
// through the day, and the Traffic tab also refreshes it when opened.
func (m *Monitor) analyticsDue(ctx context.Context, projects []store.Project) {
	if !m.HasAnalytics() {
		return
	}
	for _, p := range projects {
		if len(p.Domains) == 0 {
			continue
		}
		last, err := m.Store.AnalyticsReport(ctx, p.ID)
		if err != nil || time.Since(last.FetchedAt) < 6*time.Hour {
			continue
		}
		if property, err := m.RefreshAnalytics(ctx, p); err != nil {
			m.Log.Warn("analytics", "project", p.Name, "err", err)
		} else {
			m.Log.Info("analytics", "project", p.Name, "property", property)
		}
	}
}

// searchDue refreshes search data at most every 20 hours; Google updates it daily.
func (m *Monitor) searchDue(ctx context.Context, projects []store.Project) {
	if !m.HasSearch() {
		return
	}
	for _, p := range projects {
		if len(p.Domains) == 0 {
			continue
		}
		last, err := m.Store.SearchReport(ctx, p.ID)
		if err != nil || time.Since(last.FetchedAt) < 20*time.Hour {
			continue
		}
		if property, err := m.RefreshSearch(ctx, p); err != nil {
			m.Log.Warn("search console", "project", p.Name, "err", err)
		} else {
			m.Log.Info("search console", "project", p.Name, "property", property)
		}
	}
}

// Configure swaps in new sources and a new drafter after the settings change.
func (m *Monitor) Configure(sources []source.Source, drafter ai.Drafter) {
	m.cfg.Lock()
	defer m.cfg.Unlock()
	m.Sources, m.Drafter = sources, drafter
}

func (m *Monitor) current() ([]source.Source, ai.Drafter) {
	m.cfg.RLock()
	defer m.cfg.RUnlock()
	return m.Sources, m.Drafter
}

// SourceNames lists the sources currently in use.
func (m *Monitor) SourceNames() []string {
	sources, _ := m.current()
	var names []string
	for _, s := range sources {
		names = append(names, s.Name())
	}
	return names
}

// Writer returns the configured AI provider for free-form requests, or nil.
func (m *Monitor) Writer() ai.Writer {
	_, d := m.current()
	w, _ := d.(ai.Writer)
	return w
}

// HasDrafter reports whether reply drafts are configured.
func (m *Monitor) HasDrafter() bool {
	_, d := m.current()
	return d != nil
}

// Match decides which of a project's terms an item mentions and whether it is
// about the product itself or only about a competitor. ok is false when the
// item mentions none of them (sources return candidates, not certainties).
func Match(p store.Project, it source.Item) (terms []string, about string, ok bool) {
	text := it.Title + "\n" + it.Text + "\n" + it.URL
	seen := map[string]bool{}
	add := func(t string) {
		if !seen[strings.ToLower(t)] {
			seen[strings.ToLower(t)] = true
			terms = append(terms, t)
		}
	}
	product := false
	for _, t := range it.Terms {
		add(t)
		isRival := false
		for _, c := range p.Competitors {
			if strings.EqualFold(c, t) {
				isRival = true // a Google Alert set up for a competitor
			}
		}
		if !isRival {
			product = true
		}
	}
	for _, t := range append(append([]string{}, p.Domains...), p.Keywords...) {
		if source.Contains(text, t) {
			add(t)
			product = true
		}
	}
	for _, t := range p.Competitors {
		if source.Contains(text, t) {
			add(t)
		}
	}
	if strings.HasPrefix(it.Where, "your ") { // replies and mentions in the owner's own inboxes
		product = true
	}
	switch {
	case product:
		return terms, "product", true
	case len(terms) > 0:
		return terms, "competitor", true
	}
	return nil, "", false
}

// IsBot reports whether an author is an automated account, such as GitHub's
// renovate[bot] and dependabot[bot], or a Reddit AutoModerator.
func IsBot(author string) bool {
	a := strings.ToLower(author)
	return strings.HasSuffix(a, "[bot]") || strings.HasSuffix(a, "-bot") || strings.HasSuffix(a, "_bot") ||
		strings.HasSuffix(a, "devbot") || a == "automoderator" || a == "github-actions"
}

// CheckNow asks the running loop to check immediately.
func (m *Monitor) CheckNow() {
	m.init()
	select {
	case m.trigger <- struct{}{}:
	default: // a check is already queued
	}
}

func (m *Monitor) init() {
	m.once.Do(func() { m.trigger = make(chan struct{}, 1) })
}

// Run checks every project on a timer until ctx ends.
func (m *Monitor) Run(ctx context.Context) {
	m.init()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-m.trigger:
		}
		m.CheckAll(ctx)
		timer.Reset(m.Interval)
	}
}

// CheckAll checks every project against every source, then drafts replies.
func (m *Monitor) CheckAll(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.busy.Store(true)
	defer m.busy.Store(false)
	projects, err := m.Store.Projects(ctx)
	if err != nil {
		m.Log.Error("loading projects", "err", err)
		return
	}
	sources, _ := m.current()
	for _, p := range projects {
		if ctx.Err() != nil {
			return // shutting down
		}
		watch := source.Watch{Domains: p.Domains, Keywords: p.Keywords, Competitors: p.Competitors}
		if len(watch.Domains)+len(watch.Keywords)+len(watch.Competitors) == 0 {
			continue
		}
		for _, src := range sources {
			if ctx.Err() != nil {
				return
			}
			if !m.due(p.ID, src) {
				continue
			}
			check := store.Check{Source: src.Name(), At: time.Now()}
			items, err := src.Fetch(ctx, watch)
			if ctx.Err() != nil {
				return // shutting down mid-request; this check is simply not recorded
			}
			if err != nil {
				check.Error = err.Error()
				m.Log.Warn("source failed", "project", p.Name, "source", src.Name(), "err", err)
			}
			for _, it := range items {
				if IsBot(it.Author) {
					continue // dependency bots and CI accounts are not people talking about you
				}
				if !m.belongs(p.ID, it) {
					continue // a reply to an account used for another project
				}
				terms, about, ok := Match(p, it)
				if !ok {
					continue
				}
				check.Found++
				added, err := m.Store.AddMention(ctx, store.Mention{
					ProjectID: p.ID, Source: it.Source, ExternalID: it.ExternalID, URL: it.URL,
					Author: it.Author, Title: it.Title, Body: it.Text, Location: it.Where,
					PostedAt: it.Created, Terms: terms, About: about, Ours: p.IsOwn(it.Source, it.Author) || m.isConnected(it.Source, it.Author),
					ContextText: it.ContextText, ContextURL: it.ContextURL, ContextOwn: it.ContextOwn,
				})
				if err != nil {
					m.Log.Error("saving mention", "err", err)
					continue
				}
				if added {
					check.Added++
				}
			}
			if err := m.Store.RecordCheck(ctx, p.ID, check); err != nil {
				m.Log.Error("recording check", "err", err)
			}
			m.Log.Info("checked", "project", p.Name, "source", src.Name(), "matched", check.Found, "new", check.Added)
		}
	}
	m.draftPending(ctx, projects)
	m.searchDue(ctx, projects)
	m.analyticsDue(ctx, projects)
	if m.Notifier != nil {
		if err := m.Notifier.Run(ctx); err != nil {
			m.Log.Warn("sending notifications", "err", err)
		}
	}
}

// draftPending asks the AI provider about new mentions that have no decision yet.
func (m *Monitor) draftPending(ctx context.Context, projects []store.Project) {
	if !m.HasDrafter() {
		return
	}
	byID := map[int64]store.Project{}
	for _, p := range projects {
		byID[p.ID] = p
	}
	pending, err := m.Store.Undrafted(ctx, 20)
	if err != nil {
		m.Log.Error("loading undrafted mentions", "err", err)
		return
	}
	for _, mention := range pending {
		if err := m.DraftOne(ctx, byID[mention.ProjectID], mention); err != nil {
			m.Log.Warn("drafting failed", "mention", mention.ID, "err", err)
			return // provider down or out of credit: try again next check
		}
	}
}

// DraftOne asks for a reply decision for one mention and stores it.
func (m *Monitor) DraftOne(ctx context.Context, p store.Project, mention store.Mention) error {
	_, drafter := m.current()
	if drafter == nil {
		return errors.New("reply drafts are off; choose an AI provider in Settings")
	}
	replyToAuthor := ""
	if i := strings.Index(mention.ContextURL, "/profile/"); i >= 0 { // Bluesky links name the author
		replyToAuthor = strings.SplitN(mention.ContextURL[i+len("/profile/"):], "/", 2)[0]
	}
	ownThread := mention.ContextOwn || m.isConnected(mention.Source, replyToAuthor) || (replyToAuthor != "" && p.IsOwn(mention.Source, replyToAuthor))
	if mention.ContextText == "" && strings.HasPrefix(mention.Location, "your ") {
		ownThread = true // a mention or comment on the team's own account or content
	}
	linked := ""
	if p.ReadLinks && mention.ContextURL != "" {
		if text, err := ai.ReadPage(ctx, mention.ContextURL); err == nil {
			linked = text
		}
	}
	draft, err := drafter.Draft(ctx, ai.Request{
		Description: p.Description, Tone: p.Tone, ReplyLength: p.ReplyLength, AvoidWords: p.AvoidWords, Examples: p.Examples, LinkedText: linked,
		Product: p.Name, Domains: p.Domains, Source: mention.Source, Where: mention.Location,
		Author: mention.Author, Title: mention.Title, Text: mention.Body, About: mention.About, Terms: mention.Terms,
		ReplyTo: mention.ContextText, ReplyToAuthor: replyToAuthor, OwnThread: ownThread,
	})
	if err != nil {
		return err
	}
	return m.Store.SetDraft(ctx, mention.ID, draft.Decision, draft.Reply, draft.Reason)
}

// emails lists the service accounts behind a set of Google clients, for
// messages that tell the owner which address to add.
func emails[C interface{ Email() string }](clients []C) string {
	var out []string
	for _, c := range clients {
		out = append(out, c.Email())
	}
	if len(out) == 1 {
		return out[0]
	}
	return "one of " + strings.Join(out, ", ")
}

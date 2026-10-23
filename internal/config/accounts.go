package config

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sonic0002/eavesong/internal/ga"
	"github.com/sonic0002/eavesong/internal/gsc"
	"github.com/sonic0002/eavesong/internal/source"
	"github.com/sonic0002/eavesong/internal/store"
)

// Account is one connected account with its secret decrypted.
type Account struct {
	ID       int64
	Platform string
	Name     string  // what the account is called: a handle, a username, a label
	Secret   string  // the key, token, password or private feed link
	FromEnv  bool    // set by an environment variable; change it there
	Projects []int64 // for your own accounts: the projects it is used for; empty means all
}

// Key identifies one of your accounts on the items it brings in ("bluesky:acme.dev").
func (a Account) Key() string { return a.Platform + ":" + a.Name }

// Kinds of connection.
const (
	KindKey     = "key"     // an API key or token: lets Eavesong read a platform, shared by all projects
	KindAccount = "account" // one of your own accounts: its replies and mentions belong to chosen projects
)

// Field is one input of a platform's "connect" form. Every form posts at most
// a name and a secret.
type Field struct {
	Input       string // "name" or "secret"
	Label       string
	Placeholder string
	Kind        string // "text", "password" or "textarea"
	Optional    bool
}

// Platform describes a source that can use connected accounts.
type Platform struct {
	Key, Name  string
	Help       string   // what connecting adds
	Steps      []string // how to get the key
	Fields     []Field
	Button     string
	NamePrefix string // shown before account names, such as "@" or "u/"
	Free       bool   // works without any account; keys only raise limits
	Kind       string // KindKey or KindAccount
}

// AccountPlatforms lists, in display order, every platform with connected accounts.
var AccountPlatforms = []Platform{
	{Key: "x", Name: "X API", Kind: KindKey,
		Help:   "Lets Eavesong search recent posts on X, with your own X API token. X charges for every post read, so Eavesong checks at most every two hours and stops at the daily limit set above.",
		Steps:  []string{"Create a project and app at developer.x.com and add credit to the account.", "Copy the app's Bearer Token."},
		Fields: []Field{{"secret", "Bearer token", "AAAA…", "password", false}}, Button: "Save token"},
	{Key: "xaccount", Name: "X", NamePrefix: "@", Kind: KindAccount,
		Help:   "Collects posts that mention your X account. Uses the X API token under API keys below, and counts against the same daily limit.",
		Fields: []Field{{"name", "Username", "acme", "text", false}}, Button: "Add account"},
	{Key: "bluesky", Kind: KindAccount, Name: "Bluesky", NamePrefix: "@",
		Help:   "Bluesky only lets signed-in accounts search. Search uses one account; every account you connect also has its replies, mentions and quotes read.",
		Steps:  []string{"In Bluesky, open Settings, Privacy and security, App passwords, and create one. Never use your main password."},
		Fields: []Field{{"name", "Handle or DID", "you.bsky.social", "text", false}, {"secret", "App password", "xxxx-xxxx-xxxx-xxxx", "password", false}}, Button: "Connect"},
	{Key: "reddit", Kind: KindAccount, Name: "Reddit", NamePrefix: "u/",
		Help:   "Reads replies to your posts and comments. Reddit no longer approves API apps for this, so Eavesong uses each account's private inbox feed.",
		Steps:  []string{"While signed in to the account, open old.reddit.com/prefs/feeds.", "Copy the RSS link next to \"your inbox\". It works like a password."},
		Fields: []Field{{"secret", "Inbox feed link", "https://old.reddit.com/message/inbox/.rss?feed=…&user=…", "password", false}}, Button: "Connect"},
	{Key: "mastodon", Kind: KindAccount, Name: "Mastodon", NamePrefix: "@",
		Help:   "Searches public posts across the network and reads each account's mentions. Most servers only let signed-in accounts search.",
		Steps:  []string{"On your server, open Preferences, Development, New application.", "Tick only the read scope, save, and copy \"Your access token\"."},
		Fields: []Field{{"name", "Server", "https://mastodon.social", "text", false}, {"secret", "Access token", "", "password", false}}, Button: "Connect"},
	{Key: "facebook", Kind: KindAccount, Name: "Facebook Pages",
		Help:   "Reads comments on recent posts of Pages you manage. Meta does not let apps search public posts, so mentions elsewhere on Facebook cannot be found.",
		Steps:  []string{"Create an app at developers.facebook.com and get a Page access token for your Page (Graph API Explorer, pages_read_engagement and pages_read_user_content).", "Copy the Page ID from the Page's About section."},
		Fields: []Field{{"name", "Page ID", "123456789", "text", false}, {"secret", "Page access token", "", "password", false}}, Button: "Connect"},
	{Key: "devto", Kind: KindAccount, Name: "DEV", NamePrefix: "@",
		Help:   "Reads the comments on this account's published articles; DEV has no public search.",
		Steps:  []string{"Create an API key at dev.to/settings/extensions (\"DEV Community API Keys\")."},
		Fields: []Field{{"secret", "API key", "", "password", false}}, Button: "Connect"},
	{Key: "youtube", Kind: KindKey, Name: "YouTube",
		Help:   "Finds new videos about your product. A search uses 100 of the 10,000 free daily quota units, so Eavesong checks every six hours.",
		Steps:  []string{"In Google Cloud, enable the YouTube Data API v3 and create an API key."},
		Fields: []Field{{"secret", "API key", "", "password", false}, {"name", "Label", "optional", "text", true}}, Button: "Connect"},
	{Key: "stackoverflow", Kind: KindKey, Name: "Stack Overflow", Free: true,
		Help:   "Stack Overflow is searched without any key, every two hours. A free key raises the daily quota so it can be checked every time.",
		Steps:  []string{"Register an app at stackapps.com/apps/oauth/register and copy its Key (not the secret)."},
		Fields: []Field{{"secret", "Key", "", "password", false}}, Button: "Save key"},
	{Key: "googlealerts", Kind: KindKey, Name: "Google Alerts",
		Help:   "Catches blogs, news sites and forums that no other source covers, through Google Alerts delivered as RSS. Add one feed per alert.",
		Steps:  []string{"At google.com/alerts, create an alert for your product name.", "In its options, set \"Deliver to\" to RSS feed, then copy the feed link (the RSS icon)."},
		Fields: []Field{{"secret", "Alert feed link", "https://www.google.com/alerts/feeds/…", "text", false}}, Button: "Add alert"},
	{Key: "github", Kind: KindKey, Name: "GitHub", Free: true,
		Help:   "GitHub is searched without a token. Tokens raise the limit; with several, the next is used when one hits its limit.",
		Steps:  []string{"Create a fine-grained token at github.com/settings/tokens with no extra permissions."},
		Fields: []Field{{"secret", "Token", "", "password", false}, {"name", "Label", "optional", "text", true}}, Button: "Save token"},
	{Key: "searchconsole", Kind: KindKey, Name: "Search Console",
		Help:   "Shows the Google searches each project appears for, in the project's Search tab.",
		Steps:  []string{"In Google Cloud, enable the Search Console API, create a service account and download a JSON key.", "In Search Console, open Settings, Users and permissions, and add the service account's email as a Restricted user.", "Paste the whole key file. Eavesong lists the properties it can read."},
		Fields: []Field{{"secret", "Service account key (JSON)", `{"type": "service_account", ...}`, "textarea", false}}, Button: "Connect"},
	{Key: "googleanalytics", Kind: KindKey, Name: "Google Analytics",
		Help:   "Shows visitors, where they came from and which pages they read, in the project's Traffic tab, next to the mentions from each place. Google Analytics 4 only.",
		Steps:  []string{"In Google Cloud, enable the Google Analytics Admin API and the Google Analytics Data API. You can reuse the Search Console service account, or create one and download a JSON key.", "In Google Analytics, open Admin, Property access management, and add the service account's email as a Viewer.", "Paste the whole key file. Eavesong matches each project to the property that measures its domain."},
		Fields: []Field{{"secret", "Service account key (JSON)", `{"type": "service_account", ...}`, "textarea", false}}, Button: "Connect"},
}

// PlatformByKey finds a platform description.
func PlatformByKey(key string) (Platform, bool) {
	for _, p := range AccountPlatforms {
		if p.Key == key {
			return p, true
		}
	}
	return Platform{}, false
}

// Checked is what a verifier learned about new credentials.
type Checked struct {
	Name string // the account's real name, when the platform reports one
	Note string // shown after connecting, such as which properties a key can read
}

// Verifier checks new credentials with the platform before they are saved.
type Verifier interface {
	Check(ctx context.Context, platform, name, secret string) (Checked, error)
}

// Accounts returns every connected account, saved ones first, then any given
// by environment variables.
func (m Manager) Accounts(ctx context.Context) ([]Account, error) {
	rows, err := m.Store.Connections(ctx)
	if err != nil {
		return nil, err
	}
	assigned, err := m.Store.ConnectionProjects(ctx)
	if err != nil {
		return nil, err
	}
	var out []Account
	for _, c := range rows {
		secret, err := m.Box.Open(c.Secret)
		if err != nil {
			return nil, err
		}
		name := c.Name
		if c.Platform == "bluesky" && strings.HasPrefix(name, "did:") {
			name = strings.TrimSuffix(name, ".bsky.social") // saved by 0.1 with a wrong suffix
		}
		out = append(out, Account{ID: c.ID, Platform: c.Platform, Name: name, Secret: secret, Projects: assigned[c.ID]})
	}
	env := func(platform, name, secret string) {
		if secret != "" && name != "" {
			out = append(out, Account{Platform: platform, Name: name, Secret: secret, FromEnv: true})
		}
	}
	if h := os.Getenv("BLUESKY_HANDLE"); h != "" {
		env("bluesky", strings.TrimPrefix(h, "@"), os.Getenv("BLUESKY_APP_PASSWORD"))
	}
	if f := os.Getenv("REDDIT_INBOX_FEED"); f != "" {
		env("reddit", source.InboxUser(f), f)
	}
	env("devto", os.Getenv("DEVTO_USERNAME"), os.Getenv("DEVTO_API_KEY"))
	env("github", "from GITHUB_TOKEN", os.Getenv("GITHUB_TOKEN"))
	env("x", "from X_BEARER_TOKEN", os.Getenv("X_BEARER_TOKEN"))
	if u := os.Getenv("X_USERNAME"); u != "" {
		out = append(out, Account{Platform: "xaccount", Name: store.CleanHandle(u), FromEnv: true})
	}
	env("youtube", "from YOUTUBE_API_KEY", os.Getenv("YOUTUBE_API_KEY"))
	env("stackoverflow", "from STACKEXCHANGE_KEY", os.Getenv("STACKEXCHANGE_KEY"))
	return out, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// AddAccount checks and stores an account, encrypting its secret. Adding an
// account that already exists replaces its secret.
func (m Manager) AddAccount(ctx context.Context, platform, name, secret string) error {
	_, err := m.AddAccountWithNote(ctx, platform, name, secret)
	return err
}

// normalize checks the format of new credentials before asking the platform.
func normalize(platform, name, secret string) (string, string, error) {
	need := func(v, what string) error {
		if v == "" {
			return errors.New("enter " + what)
		}
		return nil
	}
	switch platform {
	case "bluesky":
		name = store.CleanHandle(name)
		if name == "" || secret == "" {
			return "", "", errors.New("a Bluesky account needs a handle and an app password")
		}
		if !strings.Contains(name, ".") && !strings.HasPrefix(name, "did:") {
			name += ".bsky.social" // "alice" means alice.bsky.social
		}
	case "reddit":
		u, err := url.Parse(secret)
		if err != nil || u.Query().Get("feed") == "" || !strings.HasSuffix(u.Hostname(), "reddit.com") {
			return "", "", errors.New("paste the RSS link next to \"your inbox\" from old.reddit.com/prefs/feeds")
		}
		u.Host = "www.reddit.com" // old.reddit.com refuses scripts; www serves the same feed
		secret = u.String()
		if name = source.InboxUser(secret); name == "" {
			return "", "", errors.New("that feed link has no user= part; copy the whole link")
		}
	case "mastodon":
		if !strings.Contains(name, "://") {
			name = "https://" + name
		}
		u, err := url.Parse(name)
		if err != nil || u.Host == "" {
			return "", "", errors.New("enter your server's address, such as https://mastodon.social")
		}
		name = "https://" + u.Host
		if err := need(secret, "the access token"); err != nil {
			return "", "", err
		}
	case "x":
		if err := need(secret, "the bearer token"); err != nil {
			return "", "", err
		}
		name = ""
	case "xaccount":
		if name = store.CleanHandle(name); name == "" {
			return "", "", errors.New("enter your X username")
		}
		secret = "" // mentions are read with the shared X API token
	case "facebook":
		if err := need(name, "the Page ID"); err != nil {
			return "", "", err
		}
		if err := need(secret, "the Page access token"); err != nil {
			return "", "", err
		}
	case "searchconsole", "googleanalytics":
		key, _, err := gsc.ParseKey(secret)
		if err != nil {
			return "", "", err
		}
		name = key.ClientEmail
	case "devto", "youtube", "stackoverflow", "github", "googlealerts":
		if err := need(secret, "the key"); err != nil {
			return "", "", err
		}
	default:
		return "", "", errors.New("unknown platform")
	}
	return name, secret, nil
}

// AddAccountWithNote is AddAccount that also returns a short note for the
// person, such as which Search Console properties a key can read.
func (m Manager) AddAccountWithNote(ctx context.Context, platform, name, secret string) (string, error) {
	name, secret, err := normalize(platform, strings.TrimSpace(name), strings.TrimSpace(secret))
	if err != nil {
		return "", err
	}
	note := ""
	if m.Verify != nil {
		checked, err := m.Verify.Check(ctx, platform, name, secret)
		if err != nil {
			return "", err
		}
		switch {
		case checked.Name == "":
		case platform == "facebook":
			name = checked.Name + " (" + name + ")" // keep the Page ID; Sources reads it back
		default:
			name = checked.Name // for Mastodon user@server, from which Sources takes the server
		}
		note = checked.Note
	}
	switch {
	case name != "":
	case platform == "github", platform == "youtube", platform == "stackoverflow":
		name = "key ending " + secret[max(0, len(secret)-4):]
	case platform == "x":
		name = "token ending " + secret[max(0, len(secret)-4):]
	}
	sealed, err := m.Box.Seal(secret)
	if err != nil {
		return "", err
	}
	return note, m.Store.SaveConnection(ctx, store.Connection{Platform: platform, Name: name, Secret: sealed})
}

func (m Manager) RemoveAccount(ctx context.Context, id int64) error {
	return m.Store.DeleteConnection(ctx, id)
}

// Migrate moves the single source credentials saved by Eavesong 0.1 into
// connected accounts.
func (m Manager) Migrate(ctx context.Context) error {
	saved, err := m.Store.Settings(ctx)
	if err != nil {
		return err
	}
	open := func(name string) string {
		v, _ := m.Box.Open(saved[name])
		return v
	}
	if h, p := saved["bluesky_handle"], open("bluesky_app_password"); h != "" && p != "" {
		if err := m.AddAccount(ctx, "bluesky", h, p); err != nil {
			return err
		}
	}
	if f := open("reddit_inbox_feed"); f != "" {
		_ = m.AddAccount(ctx, "reddit", "", f) // an unusable old feed is simply dropped
	}
	if t := open("github_token"); t != "" {
		if err := m.AddAccount(ctx, "github", "", t); err != nil {
			return err
		}
	}
	// Before accounts and keys were separate, an X token could carry a username.
	rows, err := m.Store.Connections(ctx)
	if err != nil {
		return err
	}
	for _, c := range rows {
		if c.Platform == "x" && c.Name != "" && !strings.HasPrefix(c.Name, "from ") && c.Name != "search only" && !strings.HasPrefix(c.Name, "token ending") {
			if err := m.Store.SaveConnection(ctx, store.Connection{Platform: "xaccount", Name: c.Name, Secret: ""}); err != nil {
				return err
			}
		}
	}
	return m.Store.SaveSettings(ctx, map[string]string{"bluesky_handle": "", "bluesky_app_password": "", "reddit_inbox_feed": "", "github_token": ""})
}

// Sources builds the connectors, giving each the accounts it can use.
// xBudget limits paid X reads; nil means no limit.
func Sources(accounts []Account, xBudget source.Budget) []source.Source {
	var (
		bsky                          []source.BlueskyAccount
		dev                           []source.DevToAccount
		masto                         []source.MastodonAccount
		xTokens, xUsers               []string
		pages                         []source.FacebookPage
		feeds, tokens, ytKeys, alerts []string
		soKey                         string
	)
	for _, a := range accounts {
		switch a.Platform {
		case "bluesky":
			bsky = append(bsky, source.BlueskyAccount{Handle: a.Name, AppPassword: a.Secret, Key: a.Key()})
		case "reddit":
			feeds = append(feeds, a.Secret)
		case "github":
			tokens = append(tokens, a.Secret)
		case "devto":
			dev = append(dev, source.DevToAccount{Username: a.Name, APIKey: a.Secret})
		case "mastodon":
			server := a.Name
			if i := strings.LastIndex(a.Name, "@"); i >= 0 {
				server = "https://" + a.Name[i+1:]
			}
			masto = append(masto, source.MastodonAccount{Server: server, Acct: a.Name, Token: a.Secret})
		case "x":
			xTokens = append(xTokens, a.Secret)
		case "xaccount":
			xUsers = append(xUsers, a.Name)
		case "facebook":
			id, label := a.Name, a.Name
			if open, close := strings.LastIndex(a.Name, " ("), strings.LastIndex(a.Name, ")"); open > 0 && close > open {
				label, id = a.Name[:open], a.Name[open+2:close]
			}
			pages = append(pages, source.FacebookPage{ID: id, Name: label, Token: a.Secret, Key: a.Key()})
		case "youtube":
			ytKeys = append(ytKeys, a.Secret)
		case "stackoverflow":
			soKey = a.Secret
		case "googlealerts":
			alerts = append(alerts, a.Secret)
		}
	}
	list := []source.Source{
		source.HackerNews{},
		source.Reddit{InboxFeeds: feeds},
		source.Lobsters{},
		source.GitHub{Tokens: tokens},
		source.StackOverflow{Key: soKey},
	}
	if len(bsky) > 0 {
		list = append(list, source.Bluesky{Accounts: bsky})
	}
	if len(dev) > 0 {
		list = append(list, source.DevTo{Accounts: dev})
	}
	if len(masto) > 0 {
		list = append(list, source.Mastodon{Accounts: masto})
	}
	if len(xTokens) > 0 {
		list = append(list, source.X{Tokens: xTokens, Usernames: xUsers, Budget: xBudget})
	}
	if len(pages) > 0 {
		list = append(list, source.Facebook{Pages: pages})
	}
	if len(ytKeys) > 0 {
		list = append(list, source.YouTube{Keys: ytKeys})
	}
	if len(alerts) > 0 {
		list = append(list, source.GoogleAlerts{Feeds: alerts})
	}
	return list
}

// LiveVerifier checks credentials with the real platforms.
type LiveVerifier struct{}

func (LiveVerifier) Check(ctx context.Context, platform, name, secret string) (Checked, error) {
	switch platform {
	case "bluesky":
		handle, err := source.VerifyBluesky(ctx, "", name, secret)
		return Checked{Name: handle}, err
	case "github":
		login, err := source.VerifyGitHub(ctx, "", secret)
		if name != "" {
			login = name
		}
		return Checked{Name: login}, err
	case "devto":
		user, err := source.VerifyDevTo(ctx, "", secret)
		return Checked{Name: user}, err
	case "searchconsole":
		client, err := gsc.New(secret)
		if err != nil {
			return Checked{}, err
		}
		sites, err := client.Sites(ctx)
		if err != nil {
			return Checked{}, err
		}
		if len(sites) == 0 {
			return Checked{}, errors.New("Google accepted the key, but " + client.Email() + " cannot read any Search Console property yet\n" +
				"Open **Search Console** and select your property\n" +
				"Go to **Settings**, then **Users and permissions**, and click **Add user**\n" +
				"Add `" + client.Email() + "` with **Restricted** permission\n" +
				"Come back here and connect the key again")
		}
		return Checked{Name: client.Email(), Note: "It can read " + strings.Join(sites, ", ") + "."}, nil
	case "googleanalytics":
		client, err := ga.New(secret)
		if err != nil {
			return Checked{}, err
		}
		props, err := client.Properties(ctx)
		if err != nil {
			return Checked{}, err
		}
		if len(props) == 0 {
			return Checked{}, errors.New("Google accepted the key, but " + client.Email() + " cannot read any Google Analytics property yet\n" +
				"Open **Google Analytics** and select your property\n" +
				"Go to **Admin**, then **Property access management**, and add a user\n" +
				"Add `" + client.Email() + "` with the **Viewer** role\n" +
				"Come back here and connect the key again")
		}
		var names []string
		for _, p := range props {
			n := p.Name
			if len(p.Hosts) > 0 {
				n += " (" + strings.Join(p.Hosts, ", ") + ")"
			}
			names = append(names, n)
		}
		return Checked{Name: client.Email(), Note: "It can read " + strings.Join(names, ", ") + "."}, nil
	case "mastodon":
		acct, err := source.VerifyMastodon(ctx, name, secret)
		return Checked{Name: acct}, err
	case "x":
		return Checked{}, source.VerifyX(ctx, "", secret, "")
	case "xaccount":
		return Checked{}, nil // checking a username would cost a paid read
	case "facebook":
		page, err := source.VerifyFacebookPage(ctx, "", name, secret)
		return Checked{Name: page}, err
	case "youtube":
		return Checked{}, source.VerifyYouTube(ctx, "", secret)
	case "stackoverflow":
		return Checked{}, source.VerifyStackExchange(ctx, "", secret)
	case "googlealerts":
		alert, err := source.VerifyGoogleAlert(ctx, secret)
		return Checked{Name: alert}, err
	}
	return Checked{}, nil
}

// SearchClients builds a Search Console client for every connected key.
func SearchClients(accounts []Account) []*gsc.Client {
	var out []*gsc.Client
	for _, a := range accounts {
		if a.Platform != "searchconsole" {
			continue
		}
		if c, err := gsc.New(a.Secret); err == nil {
			out = append(out, c)
		}
	}
	return out
}

// AnalyticsClients builds a Google Analytics client for every connected key.
func AnalyticsClients(accounts []Account) []*ga.Client {
	var out []*ga.Client
	for _, a := range accounts {
		if a.Platform != "googleanalytics" {
			continue
		}
		if c, err := ga.New(a.Secret); err == nil {
			out = append(out, c)
		}
	}
	return out
}

// XBudget caps paid X reads per UTC day, counting in the settings table.
type XBudget struct {
	Store *store.Store
	Cap   int
}

func (b XBudget) key() string { return "x_reads_" + time.Now().UTC().Format("2006-01-02") }

func (b XBudget) used(ctx context.Context) int {
	saved, err := b.Store.Settings(ctx)
	if err != nil {
		return b.Cap // fail closed: no paid reads when the count is unknown
	}
	n, _ := strconv.Atoi(saved[b.key()])
	return n
}

func (b XBudget) Left(ctx context.Context) int { return max(0, b.Cap-b.used(ctx)) }

func (b XBudget) Spend(ctx context.Context, n int) {
	_ = b.Store.SaveSettings(ctx, map[string]string{b.key(): fmt.Sprint(b.used(ctx) + n)})
}

// Used reports today's paid X reads, for the settings page.
func (b XBudget) Used(ctx context.Context) int { return b.used(ctx) }

// Scopes maps each of your accounts that is limited to some projects to those
// projects. Accounts missing from the map are used for every project.
func Scopes(accounts []Account) map[string][]int64 {
	out := map[string][]int64{}
	for _, a := range accounts {
		if len(a.Projects) > 0 {
			out[a.Key()] = a.Projects
		}
	}
	return out
}

// PlatformKind returns whether a platform takes shared keys or your own accounts.
func PlatformKind(platform string) string {
	if p, ok := PlatformByKey(platform); ok {
		return p.Kind
	}
	return KindKey
}

// OwnNames lists your connected accounts as "source:name" (lower case), so
// their own posts stay out of the inbox without listing them again.
func OwnNames(accounts []Account) map[string]bool {
	out := map[string]bool{}
	for _, a := range accounts {
		if PlatformKind(a.Platform) != KindAccount || a.Name == "" {
			continue
		}
		source := a.Platform
		if source == "xaccount" {
			source = "x"
		}
		name := strings.ToLower(a.Name)
		if source == "facebook" {
			if i := strings.LastIndex(name, " ("); i > 0 {
				name = name[:i] // the Page's name, as it appears on its comments
			}
		}
		out[source+":"+name] = true
		if source == "mastodon" {
			if i := strings.Index(name, "@"); i > 0 {
				out[source+":"+name[:i]] = true // local accounts appear without @server
			}
		}
	}
	return out
}

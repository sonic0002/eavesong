package config

import (
	"context"
	"errors"
	"github.com/sonic0002/eavesong/internal/ai"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sonic0002/eavesong/internal/secret"
	"github.com/sonic0002/eavesong/internal/source"
	"github.com/sonic0002/eavesong/internal/store"
)

func TestSaveLoadEncryptsSecrets(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	key, err := secret.LoadKey(filepath.Join(dir, "test.key"))
	if err != nil {
		t.Fatal(err)
	}
	box, _ := secret.New(key)
	m := Manager{Store: db, Box: box}
	ctx := context.Background()

	in := Settings{AIProvider: "anthropic", AIKey: "sk-ant-abc123456789"}
	if err := m.Save(ctx, in); err != nil {
		t.Fatal(err)
	}
	raw, _ := db.Settings(ctx)
	if strings.Contains(raw["ai_key"], "abc123") || !strings.HasPrefix(raw["ai_key"], "v1:") {
		t.Errorf("the API key must be stored encrypted, got %q", raw["ai_key"])
	}
	if raw["ai_provider"] != "anthropic" {
		t.Errorf("non-secret settings are stored as is, got %q", raw["ai_provider"])
	}
	out, err := m.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if out.AIKey != in.AIKey {
		t.Errorf("round trip lost a secret: %+v", out)
	}
	if d, name := out.Drafter(); d == nil || name != "Anthropic, "+DefaultModel {
		t.Errorf("Drafter() = %v, %q", d, name)
	}

	// A different key cannot read the stored secrets.
	other, _ := secret.LoadKey(filepath.Join(dir, "other.key"))
	box2, _ := secret.New(other)
	if _, err := (Manager{Store: db, Box: box2}).Load(ctx); err == nil {
		t.Error("loading with the wrong key must fail, not return garbage")
	}
}

func TestValidate(t *testing.T) {
	for _, c := range []struct {
		s  Settings
		ok bool
	}{
		{Settings{AIProvider: "none"}, true},
		{Settings{AIProvider: "anthropic"}, false},
		{Settings{AIProvider: "anthropic", AIKey: "k"}, true},
		{Settings{AIProvider: "gemini"}, false},
		{Settings{AIProvider: "gemini", AIKey: "k"}, true},
		{Settings{AIProvider: "openai", AIBaseURL: "http://localhost:11434/v1"}, false},
		{Settings{AIProvider: "openai", AIBaseURL: "http://localhost:11434/v1", AIModel: "llama3.1"}, true},
		{Settings{AIProvider: "other"}, false},
	} {
		if err := c.s.Validate(); (err == nil) != c.ok {
			t.Errorf("Validate(%+v) = %v", c.s, err)
		}
	}
}

func TestAccounts(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	key, _ := secret.LoadKey(filepath.Join(dir, "test.key"))
	box, _ := secret.New(key)
	m := Manager{Store: db, Box: box}
	ctx := context.Background()

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(m.AddAccount(ctx, "bluesky", "@acme", "aaaa-bbbb-cccc-dddd"))
	must(m.AddAccount(ctx, "bluesky", "acme-support.bsky.social", "eeee-ffff-gggg-hhhh"))
	must(m.AddAccount(ctx, "reddit", "", "https://old.reddit.com/message/inbox/.rss?feed=abc&user=acme_team"))
	must(m.AddAccount(ctx, "reddit", "", "https://www.reddit.com/message/inbox/.rss?feed=def&user=acme_dev"))
	must(m.AddAccount(ctx, "github", "", "ghp_one"))
	if err := m.AddAccount(ctx, "reddit", "", "https://example.com/feed"); err == nil {
		t.Error("a non-Reddit link must be refused")
	}

	accounts, err := m.Accounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 5 {
		t.Fatalf("want 5 accounts, got %d: %+v", len(accounts), accounts)
	}
	raw, _ := db.Connections(ctx)
	for _, c := range raw {
		if !strings.HasPrefix(c.Secret, "v1:") {
			t.Errorf("%s %s secret stored unencrypted", c.Platform, c.Name)
		}
	}
	names := map[string]bool{}
	for _, a := range accounts {
		names[a.Platform+":"+a.Name] = true
		if a.Platform == "reddit" && !strings.HasPrefix(a.Secret, "https://www.reddit.com/") {
			t.Errorf("old.reddit.com feeds must be switched to www: %s", a.Secret)
		}
	}
	for _, want := range []string{"bluesky:acme.bsky.social", "bluesky:acme-support.bsky.social", "reddit:acme_team", "reddit:acme_dev"} {
		if !names[want] {
			t.Errorf("missing account %s in %v", want, names)
		}
	}

	var bsky, reddit int
	for _, src := range Sources(accounts, nil) {
		switch s := src.(type) {
		case source.Bluesky:
			bsky = len(s.Accounts)
		case source.Reddit:
			reddit = len(s.InboxFeeds)
		}
	}
	if bsky != 2 || reddit != 2 {
		t.Errorf("sources got %d Bluesky accounts and %d Reddit feeds, want 2 and 2", bsky, reddit)
	}
}

func TestMigrateOldSettings(t *testing.T) {
	dir := t.TempDir()
	db, _ := store.Open(filepath.Join(dir, "test.db"))
	defer db.Close()
	key, _ := secret.LoadKey(filepath.Join(dir, "test.key"))
	box, _ := secret.New(key)
	m := Manager{Store: db, Box: box}
	ctx := context.Background()
	pw, _ := box.Seal("aaaa-bbbb-cccc-dddd")
	tok, _ := box.Seal("ghp_old")
	db.SaveSettings(ctx, map[string]string{"bluesky_handle": "acme.dev", "bluesky_app_password": pw, "github_token": tok})

	if err := m.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	accounts, _ := m.Accounts(ctx)
	if len(accounts) != 2 {
		t.Fatalf("want the Bluesky account and the GitHub token moved over, got %+v", accounts)
	}
	saved, _ := db.Settings(ctx)
	if saved["bluesky_handle"] != "" || saved["github_token"] != "" {
		t.Error("old settings must be removed after moving them")
	}
}

type fakeVerifier struct{}

func (fakeVerifier) Check(_ context.Context, platform, name, secret string) (Checked, error) {
	switch platform {
	case "bluesky":
		if secret != "good-pass" {
			return Checked{}, errors.New("Bluesky did not accept that handle and app password")
		}
		if strings.HasPrefix(name, "did:") {
			return Checked{Name: "importstatic.com"}, nil // Bluesky reports the real handle
		}
		return Checked{Name: name}, nil
	case "github":
		return Checked{Name: "octo-user"}, nil
	case "devto":
		return Checked{Name: "importstatic"}, nil
	case "facebook":
		return Checked{Name: "Acme"}, nil
	case "mastodon":
		return Checked{Name: "acme@mastodon.social"}, nil
	}
	return Checked{}, nil
}

func TestAddAccountVerifies(t *testing.T) {
	dir := t.TempDir()
	db, _ := store.Open(filepath.Join(dir, "test.db"))
	defer db.Close()
	key, _ := secret.LoadKey(filepath.Join(dir, "test.key"))
	box, _ := secret.New(key)
	m := Manager{Store: db, Box: box, Verify: fakeVerifier{}}
	ctx := context.Background()

	if err := m.AddAccount(ctx, "bluesky", "did:plc:vsw7e3pypk652mckroqrtopt", "good-pass"); err != nil {
		t.Fatal(err)
	}
	if err := m.AddAccount(ctx, "bluesky", "someone.bsky.social", "wrong"); err == nil {
		t.Error("a rejected app password must not be saved")
	}
	if err := m.AddAccount(ctx, "github", "", "ghp_x"); err != nil {
		t.Fatal(err)
	}
	accounts, _ := m.Accounts(ctx)
	got := map[string]bool{}
	for _, a := range accounts {
		got[a.Platform+":"+a.Name] = true
	}
	if !got["bluesky:importstatic.com"] || !got["github:octo-user"] || len(accounts) != 2 {
		t.Errorf("want the DID saved as its handle and the token labelled with its login, got %v", got)
	}
}

func TestNewPlatforms(t *testing.T) {
	dir := t.TempDir()
	db, _ := store.Open(filepath.Join(dir, "test.db"))
	defer db.Close()
	key, _ := secret.LoadKey(filepath.Join(dir, "test.key"))
	box, _ := secret.New(key)
	m := Manager{Store: db, Box: box, Verify: fakeVerifier{}}
	ctx := context.Background()
	for _, a := range [][3]string{
		{"mastodon", "mastodon.social", "tok"},
		{"x", "", "AAAA-bearer"},
		{"xaccount", "@acme", ""},
		{"facebook", "12345", "page-token"},
		{"youtube", "", "AIza-key"},
		{"stackoverflow", "", "so-key"},
		{"googlealerts", "", "https://www.google.com/alerts/feeds/1/2"},
	} {
		if err := m.AddAccount(ctx, a[0], a[1], a[2]); err != nil {
			t.Fatalf("%s: %v", a[0], err)
		}
	}
	accounts, _ := m.Accounts(ctx)
	byPlatform := map[string]Account{}
	for _, a := range accounts {
		byPlatform[a.Platform] = a
	}
	if byPlatform["facebook"].Name != "Acme (12345)" || byPlatform["mastodon"].Name != "acme@mastodon.social" || byPlatform["xaccount"].Name != "acme" {
		t.Errorf("names: %+v", byPlatform)
	}
	found := map[string]source.Source{}
	for _, s := range Sources(accounts, nil) {
		found[s.Name()] = s
	}
	if fb, ok := found["facebook"].(source.Facebook); !ok || fb.Pages[0].ID != "12345" || fb.Pages[0].Name != "Acme" {
		t.Errorf("facebook page = %+v", found["facebook"])
	}
	if ms, ok := found["mastodon"].(source.Mastodon); !ok || ms.Accounts[0].Server != "https://mastodon.social" {
		t.Errorf("mastodon = %+v", found["mastodon"])
	}
	if so, ok := found["stackoverflow"].(source.StackOverflow); !ok || so.Key != "so-key" {
		t.Errorf("stackoverflow = %+v", found["stackoverflow"])
	}
	for _, name := range []string{"x", "youtube", "googlealerts"} {
		if found[name] == nil {
			t.Errorf("missing source %s", name)
		}
	}
	if err := m.AddAccount(ctx, "reddit", "", "https://example.com/feed"); err == nil {
		t.Error("a non-Reddit link must be refused")
	}
}

func TestXBudget(t *testing.T) {
	dir := t.TempDir()
	db, _ := store.Open(filepath.Join(dir, "test.db"))
	defer db.Close()
	b := XBudget{Store: db, Cap: 50}
	ctx := context.Background()
	b.Spend(ctx, 30)
	b.Spend(ctx, 15)
	if left := b.Left(ctx); left != 5 {
		t.Errorf("Left = %d, want 5", left)
	}
}

func TestOwnNames(t *testing.T) {
	own := OwnNames([]Account{
		{Platform: "reddit", Name: "Efficient_File"},
		{Platform: "xaccount", Name: "ImportStatic"},
		{Platform: "mastodon", Name: "acme@mastodon.social"},
		{Platform: "facebook", Name: "Acme (12345)"},
		{Platform: "github", Name: "work token"}, // a key, not an account
	})
	for _, want := range []string{"reddit:efficient_file", "x:importstatic", "mastodon:acme@mastodon.social", "mastodon:acme", "facebook:acme"} {
		if !own[want] {
			t.Errorf("missing %s in %v", want, own)
		}
	}
	if own["github:work token"] {
		t.Error("API keys are not your accounts")
	}
}

func TestGeminiDrafter(t *testing.T) {
	d, name := (Settings{AIProvider: "gemini", AIKey: "gemini-key"}).Drafter()
	client, ok := d.(ai.OpenAICompatible)
	if !ok || client.APIKey != "gemini-key" || client.Model != DefaultGeminiModel || client.BaseURL != GeminiBaseURL || name != "Gemini, "+DefaultGeminiModel {
		t.Fatalf("Gemini drafter = %#v, %q", d, name)
	}
}

func TestGetenvUsesOnlyRequestedName(t *testing.T) {
	t.Setenv("UNRELATED_INTERVAL", "45m")
	t.Setenv("EAVESONG_INTERVAL", "")
	if got := Getenv("EAVESONG_INTERVAL"); got != "" {
		t.Fatalf("unexpected fallback value %q", got)
	}
	t.Setenv("EAVESONG_INTERVAL", " 15m ")
	if got := Getenv("EAVESONG_INTERVAL"); got != "15m" {
		t.Fatalf("Getenv() = %q, want 15m", got)
	}
}

package store

import "testing"

func TestCleanHandle(t *testing.T) {
	for in, want := range map[string]string{
		"@acme":                         "acme",
		"u/acme_team":                   "acme_team",
		"/u/acme_team":                  "acme_team",
		"https://x.com/ImportStatic":    "ImportStatic",
		"https://twitter.com/acme?s=20": "acme",
		"https://www.reddit.com/user/Efficient_File/":  "Efficient_File",
		"https://bsky.app/profile/importstatic.com":    "importstatic.com",
		"https://github.com/sonic0002":                 "sonic0002",
		"https://news.ycombinator.com/user?id=misonic": "misonic",
		"  acme.bsky.social ":                          "acme.bsky.social",
	} {
		if got := CleanHandle(in); got != want {
			t.Errorf("CleanHandle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsOwnPerPlatform(t *testing.T) {
	p := Project{OwnAccounts: []string{"reddit:Efficient_File", "x:ImportStatic", "other:importstatic.com", "legacy_name"}}
	cases := []struct {
		source, author string
		want           bool
	}{
		{"reddit", "efficient_file", true},  // case-insensitive
		{"github", "Efficient_File", false}, // a Reddit name does not hide GitHub posts
		{"bluesky", "importstatic.com", true},
		{"hackernews", "legacy_name", true}, // lines from older versions count everywhere
		{"reddit", "someone_else", false},
	}
	for _, c := range cases {
		if got := p.IsOwn(c.source, c.author); got != c.want {
			t.Errorf("IsOwn(%q, %q) = %v, want %v", c.source, c.author, got, c.want)
		}
	}
	if got := p.AccountsFor("x"); len(got) != 1 || got[0] != "ImportStatic" {
		t.Errorf("AccountsFor(x) = %v", got)
	}
}

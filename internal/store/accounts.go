package store

import (
	"regexp"
	"strings"
)

// Platforms people can list their own accounts for, in display order. X is
// listed although it is not a source yet, so accounts are ready when it is.
var Platforms = []struct{ Key, Name, Placeholder string }{
	{"x", "X", "@acme"},
	{"reddit", "Reddit", "u/acme_team"},
	{"bluesky", "Bluesky", "acme.bsky.social"},
	{"devto", "DEV", "acme"},
	{"mastodon", "Mastodon", "acme@mastodon.social"},
	{"stackoverflow", "Stack Overflow", "display name"},
	{"youtube", "YouTube", "channel name"},
	{"facebook", "Facebook", "Page or profile name"},
	{"hackernews", "Hacker News", "acme"},
	{"github", "GitHub", "acme-bot, acme"},
	{"lobsters", "Lobsters", "acme"},
	{"other", "Anywhere", "names used on every platform"},
}

// profileURL matches profile links people paste instead of a handle.
var profileURL = regexp.MustCompile(`(?i)^(?:https?://)?(?:www\.|old\.|news\.)?(?:x\.com/|twitter\.com/|reddit\.com/(?:user|u)/|bsky\.app/profile/|dev\.to/|github\.com/|lobste\.rs/(?:~|u/)|news\.ycombinator\.com/user\?id=)([^/?#\s]+)`)

// CleanHandle turns "@acme", "u/acme", "https://x.com/acme" and similar into "acme".
func CleanHandle(s string) string {
	s = strings.TrimSpace(s)
	if m := profileURL.FindStringSubmatch(s); m != nil {
		s = m[1]
	}
	s = strings.TrimPrefix(s, "@")
	s = strings.TrimPrefix(strings.TrimPrefix(s, "/u/"), "u/")
	return strings.TrimSpace(s)
}

// AccountsFor returns the handles saved for one platform. Accounts are stored
// as "platform:handle" lines; lines without a platform (from older versions)
// count as "other".
func (p Project) AccountsFor(platform string) []string {
	var out []string
	for _, line := range p.OwnAccounts {
		key, handle, ok := strings.Cut(line, ":")
		if !ok || !knownPlatform(key) {
			key, handle = "other", line
		}
		if handle = CleanHandle(handle); key == platform && handle != "" {
			out = append(out, handle)
		}
	}
	return out
}

// IsOwn reports whether an author on a source is one of the project's accounts.
func (p Project) IsOwn(source, author string) bool {
	for _, platform := range []string{source, "other"} {
		for _, handle := range p.AccountsFor(platform) {
			if strings.EqualFold(handle, author) {
				return true
			}
		}
	}
	return false
}

func knownPlatform(key string) bool {
	for _, pl := range Platforms {
		if pl.Key == key {
			return true
		}
	}
	return false
}

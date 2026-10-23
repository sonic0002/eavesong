package monitor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/sonic0002/eavesong/internal/ai"
	"github.com/sonic0002/eavesong/internal/source"
	"github.com/sonic0002/eavesong/internal/store"
)

var project = store.Project{
	Name: "Acme", Domains: []string{"acme.dev"}, Keywords: []string{"Acme"},
	Competitors: []string{"Rival"}, OwnAccounts: []string{"@acme_team"},
}

func TestMatch(t *testing.T) {
	cases := []struct {
		name      string
		item      source.Item
		wantAbout string
		wantOK    bool
	}{
		{"keyword", source.Item{Text: "Acme saved my week"}, "product", true},
		{"domain link", source.Item{Text: "read https://acme.dev/blog"}, "product", true},
		{"competitor only", source.Item{Text: "Rival is slow"}, "competitor", true},
		{"both", source.Item{Text: "Rival vs Acme"}, "product", true},
		{"preset term", source.Item{Text: "[link]", Terms: []string{"acme.dev"}}, "product", true},
		{"inbox reply", source.Item{Text: "thanks!", Where: "your inbox (u/acme_team)"}, "product", true},
		{"bluesky notification", source.Item{Text: "nice", Where: "your notifications (@acme.dev)"}, "product", true},
		{"nothing", source.Item{Text: "unrelated"}, "", false},
		{"alert for a competitor", source.Item{Text: "a review", Terms: []string{"rival"}}, "competitor", true},
	}
	for _, c := range cases {
		_, about, ok := Match(project, c.item)
		if about != c.wantAbout || ok != c.wantOK {
			t.Errorf("%s: got (%q, %v), want (%q, %v)", c.name, about, ok, c.wantAbout, c.wantOK)
		}
	}
}

type fakeSource struct{ items []source.Item }

func (fakeSource) Name() string { return "fake" }
func (f fakeSource) Fetch(context.Context, source.Watch) ([]source.Item, error) {
	return f.items, nil
}

type fakeDrafter struct{ err error }

func (f fakeDrafter) Draft(context.Context, ai.Request) (ai.Draft, error) {
	return ai.Draft{Decision: "suggest", Reply: "Thanks, glad it helped.", Reason: "they praised it"}, f.err
}

func TestCheckAllStoresDraftsAndSkipsDuplicates(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	id, err := db.SaveProject(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	src := fakeSource{items: []source.Item{
		{Source: "fake", ExternalID: "1", Author: "dana", Text: "Acme is great", Created: now},
		{Source: "fake", ExternalID: "2", Author: "acme_team", Text: "We shipped Acme 2", Created: now},
		{Source: "fake", ExternalID: "3", Author: "erin", Text: "off topic", Created: now},
		{Source: "fake", ExternalID: "4", Author: "renovate[bot]", Text: "Update Acme to v2", Created: now},
	}}
	m := &Monitor{Store: db, Sources: []source.Source{src}, Drafter: fakeDrafter{}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	m.CheckAll(ctx)
	m.CheckAll(ctx) // the second check must not add the same posts again

	inbox, err := db.Mentions(ctx, id, store.Filter{Status: "new"})
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 || inbox[0].Author != "dana" {
		t.Fatalf("inbox should hold only dana's post (own posts hidden, off-topic dropped), got %+v", inbox)
	}
	if inbox[0].Decision != "suggest" || inbox[0].Draft == "" {
		t.Errorf("expected a stored draft, got decision %q draft %q", inbox[0].Decision, inbox[0].Draft)
	}
	all, _ := db.Mentions(ctx, id, store.Filter{IncludeOurs: true})
	if len(all) != 2 {
		t.Errorf("with our own posts included want 2 mentions, got %d", len(all))
	}
}

func TestDraftFailureLeavesMentionUndrafted(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.SaveProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	src := fakeSource{items: []source.Item{{Source: "fake", ExternalID: "1", Author: "dana", Text: "Acme?", Created: time.Now()}}}
	m := &Monitor{Store: db, Sources: []source.Source{src}, Drafter: fakeDrafter{err: errors.New("out of credit")}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	m.CheckAll(ctx)
	pending, err := db.Undrafted(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Errorf("a failed draft should be retried on the next check; undrafted = %d", len(pending))
	}
}

func TestAccountRepliesOnlyReachAssignedProjects(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	a, _ := db.SaveProject(ctx, store.Project{Name: "Acme", Keywords: []string{"Acme"}})
	b, _ := db.SaveProject(ctx, store.Project{Name: "Beta", Keywords: []string{"Beta"}})
	now := time.Now()
	src := fakeSource{items: []source.Item{
		{Source: "fake", ExternalID: "r1", Author: "dana", Text: "thanks!", Where: "your notifications (@acme.dev)", Account: "bluesky:acme.dev", Created: now},
		{Source: "fake", ExternalID: "r2", Author: "erin", Text: "nice", Where: "your notifications (@shared.dev)", Account: "bluesky:shared.dev", Created: now},
	}}
	m := &Monitor{Store: db, Sources: []source.Source{src}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	m.ConfigureScopes(map[string][]int64{"bluesky:acme.dev": {a}}) // shared.dev stays on every project
	m.CheckAll(ctx)

	inA, _ := db.Mentions(ctx, a, store.Filter{})
	inB, _ := db.Mentions(ctx, b, store.Filter{})
	if len(inA) != 2 || len(inB) != 1 || inB[0].Author != "erin" {
		t.Errorf("Acme got %d, Beta got %d %+v; want Acme 2 and Beta only the shared account's reply", len(inA), len(inB), inB)
	}
}

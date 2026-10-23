package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestReplyMarksHandledAndUndoReturnsToReview(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	pid, err := s.SaveProject(ctx, Project{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.AddMention(ctx, Mention{ProjectID: pid, Source: "reddit", ExternalID: "post", URL: "https://example.com", PostedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	mentions, err := s.Mentions(ctx, pid, Filter{})
	if err != nil || len(mentions) != 1 {
		t.Fatalf("mention: %v, %d", err, len(mentions))
	}
	id := mentions[0].ID
	if err := s.RecordDraftUsage(ctx, id, "replied"); err != nil {
		t.Fatal(err)
	}
	m, err := s.Mention(ctx, id)
	if err != nil || m.Status != "done" || m.RepliedAt == "" {
		t.Fatalf("reply state: %+v, %v", m, err)
	}
	if err := s.RecordDraftUsage(ctx, id, "unreplied"); err != nil {
		t.Fatal(err)
	}
	m, err = s.Mention(ctx, id)
	if err != nil || m.Status != "new" || m.RepliedAt != "" {
		t.Fatalf("undo state: %+v, %v", m, err)
	}
}

package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	d, err := parse("```json\n{\"decision\": \"suggest\", \"reply\": \"Yes, on amd64 too.\", \"reason\": \"a question\"}\n```")
	if err != nil || d.Decision != "suggest" || d.Reply != "Yes, on amd64 too." {
		t.Fatalf("parse = %+v, %v", d, err)
	}
	d, err = parse(`{"decision":"skip","reply":"should be dropped","reason":"praise"}`)
	if err != nil || d.Reply != "" {
		t.Errorf("a skip must not carry a reply: %+v, %v", d, err)
	}
	if _, err := parse(`{"decision":"post it now"}`); err == nil {
		t.Error("unknown decisions must be rejected")
	}
	if _, err := parse("no json here"); err == nil {
		t.Error("a reply without JSON must be an error")
	}
}

func TestOpenAICompatible(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path != "/v1/chat/completions" || body.Model != "llama3" || len(body.Messages) != 2 || body.Messages[0].Role != "system" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"decision\":\"attention\",\"reply\":\"\",\"reason\":\"bug report\"}"}}]}`))
	}))
	defer srv.Close()

	d, err := OpenAICompatible{Model: "llama3", BaseURL: srv.URL + "/v1"}.Draft(context.Background(), Request{Product: "Acme", Text: "Acme crashes on start"})
	if err != nil || d.Decision != "attention" {
		t.Fatalf("Draft = %+v, %v", d, err)
	}
}

func TestPromptIncludesWhatTheReplyAnswers(t *testing.T) {
	p := prompt(Request{Product: "ImportStatic", Source: "bluesky", Where: "your notifications (@importstatic.com)", Author: "nark3d.bsky.social",
		Text: "/memory only tells you after the fact", ReplyTo: "To check, run /memory", ReplyToAuthor: "importstatic.com", OwnThread: true})
	if !strings.Contains(p, "This replies to the team's own post:") || !strings.Contains(p, "To check, run /memory") {
		t.Errorf("the replied-to post must reach the model:\n%s", p)
	}
	other := prompt(Request{Text: "x", ReplyTo: "y", ReplyToAuthor: "someone.bsky.social"})
	if !strings.Contains(other, "a post by someone.bsky.social") {
		t.Errorf("a reply to someone else must say so:\n%s", other)
	}
}

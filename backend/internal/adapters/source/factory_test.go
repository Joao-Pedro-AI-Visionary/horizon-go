package source

import (
	"encoding/json"
	"testing"

	"github.com/jpdev/horizon-go/internal/domain"
)

func TestBuild(t *testing.T) {
	ok := []domain.SourceConfig{
		{Kind: domain.KindHackerNews, Name: "hn", Config: json.RawMessage(`{"min_score":50}`)},
		{Kind: domain.KindHackerNews, Name: "hn-default"},
		{Kind: domain.KindReddit, Name: "r/golang", Config: json.RawMessage(`{"subreddit":"golang","sort":"hot"}`)},
		{Kind: domain.KindRSS, Name: "go-blog", Config: json.RawMessage(`{"url":"https://go.dev/blog/feed.atom"}`)},
	}
	for _, sc := range ok {
		s, err := Build(sc, nil, BaseURLs{})
		if err != nil {
			t.Fatalf("Build(%s): %v", sc.Name, err)
		}
		if s.Name() != sc.Name {
			t.Fatalf("name = %q", s.Name())
		}
	}
	bad := []domain.SourceConfig{
		{Kind: "twitter", Name: "x"},
		{Kind: domain.KindReddit, Name: "r/none", Config: json.RawMessage(`{}`)},
		{Kind: domain.KindRSS, Name: "rss-bad", Config: json.RawMessage(`{"url":"nope"}`)},
		{Kind: domain.KindHackerNews, Name: "hn-bad", Config: json.RawMessage(`{"min_score":"high"}`)},
	}
	for _, sc := range bad {
		if _, err := Build(sc, nil, BaseURLs{}); err == nil {
			t.Fatalf("Build(%s) must fail", sc.Name)
		}
	}
}

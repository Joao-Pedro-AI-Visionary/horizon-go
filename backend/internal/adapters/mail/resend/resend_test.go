package resend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSend(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/emails" || r.Header.Get("Authorization") != "Bearer re_test" {
			t.Errorf("bad request %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"id":"abc-123"}`))
	}))
	defer srv.Close()
	c := &Client{APIKey: "re_test", From: "Horizon <onboarding@resend.dev>", BaseURL: srv.URL}
	id, err := c.Send(context.Background(), "a@b.c", "s", "<p>h</p>", "t")
	if err != nil || id != "abc-123" {
		t.Fatalf("id %q err %v", id, err)
	}
	if got["from"] != "Horizon <onboarding@resend.dev>" || got["to"].([]any)[0] != "a@b.c" || got["subject"] != "s" {
		t.Fatalf("body %+v", got)
	}
}

func TestSendError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"name":"validation_error","message":"domain not verified"}`))
	}))
	defer srv.Close()
	_, err := (&Client{APIKey: "re_x", BaseURL: srv.URL}).Send(context.Background(), "a@b.c", "s", "h", "t")
	if err == nil || !strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), "re_x") {
		t.Fatalf("err %v", err)
	}
}

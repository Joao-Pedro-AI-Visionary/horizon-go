package digest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jpdev/horizon-go/internal/domain"
)

type recorder struct{ kinds, names, msgs []string }

func (r *recorder) RecordFailure(_ context.Context, kind, name, msg string) error {
	r.kinds, r.names, r.msgs = append(r.kinds, kind), append(r.names, name), append(r.msgs, msg)
	return nil
}

func TestGenerateRecordsFailuresButNotBadInput(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	fs := &fakeStore{st: domain.Settings{DigestMaxItems: 5, Timezone: "America/Sao_Paulo"}, candErr: errors.New("db down")}
	rec := &recorder{}
	svc := &Service{Store: fs, Now: func() time.Time { return now }, Failures: rec}

	if _, err := svc.Generate(context.Background(), ""); err == nil {
		t.Fatal("want error")
	}
	if len(rec.kinds) != 1 || rec.kinds[0] != domain.AlertDigest || rec.names[0] != "Digest" || !strings.Contains(rec.msgs[0], "db down") {
		t.Fatalf("recorded %+v", rec)
	}
	if _, err := svc.Generate(context.Background(), "nope"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("bad date: %v", err)
	}
	if len(rec.kinds) != 1 {
		t.Fatalf("bad input must not raise an alert: %+v", rec)
	}
	fs.candErr = nil
	if _, err := svc.Generate(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if len(rec.kinds) != 1 {
		t.Fatalf("success must not record: %+v", rec)
	}
}

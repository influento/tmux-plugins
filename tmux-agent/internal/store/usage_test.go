package store

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// A tally keeps its efforts, and writes the same without them as hours,
// the shape a tmux-agent before efforts reads. That one drops by_hour and
// version when it writes the tally back; the tally then loads as version
// 0 from hours, its totals whole, with every effort unknown.
func TestUsageOlderBinary(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	at := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	u := Usage{Agent: "fakeagent", Session: "s1", Version: 1}
	u.Add(at, "model-a", "high", Tokens{Output: 5})
	u.Add(at, "model-a", "low", Tokens{Output: 2})
	if err := s.SaveUsage(u); err != nil {
		t.Fatal(err)
	}
	got, _, err := s.LoadUsage("fakeagent", "s1")
	if err != nil || got.Version != 1 || got.Hours["2026-01-05T10"]["model-a"]["high"].Output != 5 || len(got.Hours["2026-01-05T10"]["model-a"]) != 2 {
		t.Fatalf("loaded = %+v, %v", got, err)
	}

	// The older binary: its struct knows hours and not by_hour or version.
	p, err := s.usagePath("fakeagent", "s1")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var old struct {
		Agent   string                       `json:"agent"`
		Session string                       `json:"session_id"`
		Hours   map[string]map[string]Tokens `json:"hours"`
	}
	if err := json.Unmarshal(data, &old); err != nil {
		t.Fatal(err)
	}
	if old.Hours["2026-01-05T10"]["model-a"].Output != 7 {
		t.Fatalf("hours as an older binary reads them = %+v", old.Hours)
	}
	old.Hours["2026-01-05T10"]["model-a"] = old.Hours["2026-01-05T10"]["model-a"].Plus(Tokens{Output: 1})
	if data, err = json.Marshal(old); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, _, err = s.LoadUsage("fakeagent", "s1")
	if err != nil || got.Version != 0 || got.Hours["2026-01-05T10"]["model-a"][""].Output != 8 || len(got.Hours["2026-01-05T10"]["model-a"]) != 1 {
		t.Errorf("after an older binary's write = %+v, %v", got, err)
	}
}

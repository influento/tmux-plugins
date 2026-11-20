package claude

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Claude Code's file for a process says when it went idle; busy, a
// dialog, another session or no file say nothing.
func TestIdleSince(t *testing.T) {
	home := t.TempDir()
	getenv := func(k string) string { return map[string]string{"HOME": home}[k] }
	dir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	const session = "11111111-1111-1111-1111-111111111111"
	at := time.UnixMilli(1794698366044)
	tests := []struct {
		name, body string
		ok         bool
	}{
		{"idle", `{"pid":42,"sessionId":"` + session + `","status":"idle","statusUpdatedAt":1794698366044}`, true},
		{"busy", `{"pid":42,"sessionId":"` + session + `","status":"busy","statusUpdatedAt":1794698366044}`, false},
		{"waiting", `{"pid":42,"sessionId":"` + session + `","status":"waiting","waitingFor":"dialog open","statusUpdatedAt":1794698366044}`, false},
		{"another session", `{"pid":42,"sessionId":"22222222-2222-2222-2222-222222222222","status":"idle","statusUpdatedAt":1794698366044}`, false},
		{"no time", `{"pid":42,"sessionId":"` + session + `","status":"idle"}`, false},
		{"half written", `{"pid":42,"sessionId":"` + session, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(dir, "42.json"), []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			got, ok := Claude{}.IdleSince(42, session, getenv)
			if ok != tt.ok || (ok && !got.Equal(at)) {
				t.Errorf("IdleSince = %v, %v; want ok %v at %v", got, ok, tt.ok, at)
			}
		})
	}
	if _, ok := (Claude{}).IdleSince(43, session, getenv); ok {
		t.Error("IdleSince of a process with no file: ok")
	}
	if got, want := (Claude{}).StatusFile(42, getenv), filepath.Join(dir, "42.json"); got != want {
		t.Errorf("StatusFile = %q, want %q", got, want)
	}
}

// A session's SendMessage address is the name in its process's file;
// another session, no name or no file give none.
func TestPeerAddress(t *testing.T) {
	home := t.TempDir()
	getenv := func(k string) string { return map[string]string{"HOME": home}[k] }
	dir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	const session = "11111111-1111-1111-1111-111111111111"
	tests := []struct {
		name, body string
		to         string
	}{
		{"named", `{"pid":42,"sessionId":"` + session + `","name":"proj-a-1f","status":"busy"}`, "proj-a-1f"},
		{"another session", `{"pid":42,"sessionId":"22222222-2222-2222-2222-222222222222","name":"proj-a-1f"}`, ""},
		{"no name", `{"pid":42,"sessionId":"` + session + `","status":"idle"}`, ""},
		{"half written", `{"pid":42,"sessionId":"` + session, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(dir, "42.json"), []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			via, to, ok := Claude{}.PeerAddress(42, session, getenv)
			if tt.to == "" {
				if ok {
					t.Errorf("PeerAddress = %q, %q; want none", via, to)
				}
				return
			}
			if !ok || via != "SendMessage" || to != tt.to {
				t.Errorf("PeerAddress = %q, %q, %v; want SendMessage, %q", via, to, ok, tt.to)
			}
		})
	}
	if _, _, ok := (Claude{}).PeerAddress(43, session, getenv); ok {
		t.Error("PeerAddress of a process with no file: ok")
	}
}

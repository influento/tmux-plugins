package store

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Usage is a session's token tally: what its main agent's model calls
// used, by hour and model, read from its transcript so far. It outlives
// the transcript. Subagents' tokens are in their own records.
type Usage struct {
	Agent      string `json:"agent"`
	Session    string `json:"session_id"`
	Transcript string `json:"transcript,omitempty"`
	Cwd        string `json:"cwd,omitempty"`      // the folder the session started in
	OneShot    bool   `json:"one_shot,omitempty"` // a run of one prompt (claude -p)
	// Parent is the session whose agent started this one, as its process
	// tree told when it ran; ParentAgent that session's agent.
	Parent      string `json:"parent,omitempty"`
	ParentAgent string `json:"parent_agent,omitempty"`
	Title       string `json:"title,omitempty"` // the session's title
	TitleSure   bool   `json:"title_sure,omitempty"`
	// Offset is how far the transcript is read; Recent the ids of the last
	// model calls read, which a later line can repeat.
	Offset int64    `json:"offset"`
	Recent []string `json:"recent,omitempty"`
	// Hours maps an hour (UTC, HourKey) to the tokens of each model.
	Hours   map[string]map[string]Tokens `json:"hours,omitempty"`
	Updated int64                        `json:"updated,omitempty"`
}

// HourLayout is the layout of an hour key: the UTC hour a call began.
const HourLayout = "2006-01-02T15"

// HourKey is the key of the hour t falls in.
func HourKey(t time.Time) string { return t.UTC().Format(HourLayout) }

// Add counts tokens of a model at time t.
func (u *Usage) Add(t time.Time, model string, tok Tokens) {
	if u.Hours == nil {
		u.Hours = map[string]map[string]Tokens{}
	}
	h := HourKey(t)
	if u.Hours[h] == nil {
		u.Hours[h] = map[string]Tokens{}
	}
	u.Hours[h][model] = u.Hours[h][model].Plus(tok)
}

// Plus is the sum of two counts.
func (t Tokens) Plus(o Tokens) Tokens {
	return Tokens{
		Input:      t.Input + o.Input,
		CacheWrite: t.CacheWrite + o.CacheWrite,
		CacheRead:  t.CacheRead + o.CacheRead,
		Output:     t.Output + o.Output,
	}
}

func (s *Store) usagePath(agent, session string) (string, error) {
	p, err := s.path(agent, session)
	if err != nil {
		return "", err
	}
	return filepath.Join(strings.TrimSuffix(p, ".json"), "usage.json"), nil
}

// LoadUsage reads a session's tally. ok is false when there is none.
func (s *Store) LoadUsage(agent, session string) (u Usage, ok bool, err error) {
	p, err := s.usagePath(agent, session)
	if err != nil {
		return Usage{}, false, err
	}
	ok, err = load(p, &u)
	if err != nil || !ok {
		return Usage{}, false, err
	}
	return u, true, nil
}

// SaveUsage writes a session's tally whole. The caller holds the lock.
func (s *Store) SaveUsage(u Usage) error {
	p, err := s.usagePath(u.Agent, u.Session)
	if err != nil {
		return err
	}
	u.Updated = time.Now().Unix()
	return writeJSON(p, u)
}

// AllUsage returns the tally of every session of an agent. A damaged
// tally is left out: the next pass reads its transcript again.
func (s *Store) AllUsage(agent string) ([]Usage, error) {
	if !ValidName(agent) {
		return nil, errors.New("store: invalid agent name " + agent)
	}
	dirs, err := os.ReadDir(filepath.Join(s.Dir, "sessions", agent))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Usage
	for _, d := range dirs {
		if !d.IsDir() || !ValidName(d.Name()) {
			continue
		}
		u, ok, err := s.LoadUsage(agent, d.Name())
		if _, bad := errors.AsType[*json.SyntaxError](err); bad {
			continue
		}
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, u)
		}
	}
	return out, nil
}

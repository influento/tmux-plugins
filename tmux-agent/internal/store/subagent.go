package store

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Subagent is what is kept about one subagent: an agent that a session's
// agent started to work beside it, in the same session and pane.
type Subagent struct {
	Agent       string `json:"agent"`
	Session     string `json:"session_id"` // the session that started it
	ID          string `json:"subagent_id"`
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
	Prompt      string `json:"prompt,omitempty"`
	Result      string `json:"result,omitempty"` // its final report
	Model       string `json:"model,omitempty"`
	Depth       int    `json:"depth,omitempty"` // 1 for one the session's agent started
	Transcript  string `json:"transcript,omitempty"`
	Started     int64  `json:"started,omitempty"`
	Ended       int64  `json:"ended,omitempty"`
	// Outcome is how it ended: done, stopped (by the user) or unknown;
	// empty while it runs.
	Outcome string  `json:"outcome,omitempty"`
	Tokens  *Tokens `json:"tokens,omitempty"`
	// Complete marks a record filled from the agent's files after the
	// subagent ended: a pass over the files reads it no more.
	Complete bool  `json:"complete,omitempty"`
	Updated  int64 `json:"updated,omitempty"`
}

// Tokens are the tokens a subagent's model calls used, by kind.
type Tokens struct {
	Input      int64 `json:"input"`
	CacheWrite int64 `json:"cache_write"`
	CacheRead  int64 `json:"cache_read"`
	Output     int64 `json:"output"`
}

// Total is all the tokens together.
func (t Tokens) Total() int64 { return t.Input + t.CacheWrite + t.CacheRead + t.Output }

func (s *Store) subagentDir(agent, session string) (string, error) {
	p, err := s.path(agent, session)
	if err != nil {
		return "", err
	}
	return filepath.Join(strings.TrimSuffix(p, ".json"), "subagents"), nil
}

func (s *Store) subagentPath(agent, session, id string) (string, error) {
	if !ValidName(id) {
		return "", errors.New("store: invalid subagent id " + id)
	}
	dir, err := s.subagentDir(agent, session)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, id+".json"), nil
}

// LoadSubagent reads a subagent's record. ok is false when there is none.
func (s *Store) LoadSubagent(agent, session, id string) (sub Subagent, ok bool, err error) {
	p, err := s.subagentPath(agent, session, id)
	if err != nil {
		return Subagent{}, false, err
	}
	ok, err = load(p, &sub)
	if err != nil || !ok {
		return Subagent{}, false, err
	}
	return sub, true, nil
}

// UpdateSubagent reads a subagent's record, or starts one, applies fn and
// writes it back. The caller holds the lock.
func (s *Store) UpdateSubagent(agent, session, id string, fn func(*Subagent)) error {
	sub, _, err := s.LoadSubagent(agent, session, id)
	if err != nil {
		if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
			return err
		}
		sub = Subagent{} // a damaged record is replaced
	}
	sub.Agent, sub.Session, sub.ID = agent, session, id
	fn(&sub)
	sub.Updated = time.Now().Unix()
	p, err := s.subagentPath(agent, session, id)
	if err != nil {
		return err
	}
	return writeJSON(p, sub)
}

// Subagents returns the records of a session's subagents, by id.
func (s *Store) Subagents(agent, session string) ([]Subagent, error) {
	dir, err := s.subagentDir(agent, session)
	if err != nil {
		return nil, err
	}
	names, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	slices.Sort(names)
	var out []Subagent
	for _, n := range names {
		var sub Subagent
		ok, err := load(n, &sub)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, sub)
		}
	}
	return out, nil
}

// AllSubagents returns every subagent record of an agent, session by
// session.
func (s *Store) AllSubagents(agent string) ([]Subagent, error) {
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
	var out []Subagent
	for _, d := range dirs {
		if !d.IsDir() || !ValidName(d.Name()) {
			continue
		}
		subs, err := s.Subagents(agent, d.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, subs...)
	}
	return out, nil
}

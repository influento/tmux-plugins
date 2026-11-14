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
// used, by hour, model and effort, read from its transcript so far. It
// outlives the transcript. Subagents' tokens are in their own records.
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
	// Hours maps an hour (UTC, HourKey) to each model's tokens, by the
	// effort it ran at ("" when the agent did not say).
	Hours map[string]map[string]map[string]Tokens `json:"by_hour,omitempty"`
	// Version is the reader's version that read the tally; a pass reads an
	// older one's transcript again from its start.
	Version int   `json:"version,omitempty"`
	Updated int64 `json:"updated,omitempty"`
}

// usageFile is a tally as written: Hours, and the same without efforts
// as hours, the shape a tmux-agent before efforts reads and writes. Such
// a one drops by_hour and version when it writes a tally back, but keeps
// hours whole, so the tally loads as version 0 with every effort unknown,
// and nothing is lost when its transcript is gone.
type usageFile struct {
	usageFields
	Legacy map[string]map[string]Tokens `json:"hours,omitempty"`
}

type usageFields Usage

// MarshalJSON writes the tally with hours beside by_hour.
func (u Usage) MarshalJSON() ([]byte, error) {
	f := usageFile{usageFields: usageFields(u)}
	for h, models := range u.Hours {
		for m, efforts := range models {
			for _, tok := range efforts {
				if f.Legacy == nil {
					f.Legacy = map[string]map[string]Tokens{}
				}
				if f.Legacy[h] == nil {
					f.Legacy[h] = map[string]Tokens{}
				}
				f.Legacy[h][m] = f.Legacy[h][m].Plus(tok)
			}
		}
	}
	return json.Marshal(f)
}

// UnmarshalJSON reads a tally; one of version 0 from its hours.
func (u *Usage) UnmarshalJSON(data []byte) error {
	var f usageFile
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	*u = Usage(f.usageFields)
	if u.Version == 0 {
		u.Hours = nil
		for h, models := range f.Legacy {
			for m, tok := range models {
				t, err := time.Parse(HourLayout, h)
				if err != nil {
					continue
				}
				u.Add(t, m, "", tok)
			}
		}
	}
	return nil
}

// HourLayout is the layout of an hour key: the UTC hour a call began.
const HourLayout = "2006-01-02T15"

// HourKey is the key of the hour t falls in.
func HourKey(t time.Time) string { return t.UTC().Format(HourLayout) }

// Add counts tokens of a model at an effort at time t.
func (u *Usage) Add(t time.Time, model, effort string, tok Tokens) {
	if u.Hours == nil {
		u.Hours = map[string]map[string]map[string]Tokens{}
	}
	h := HourKey(t)
	if u.Hours[h] == nil {
		u.Hours[h] = map[string]map[string]Tokens{}
	}
	if u.Hours[h][model] == nil {
		u.Hours[h][model] = map[string]Tokens{}
	}
	u.Hours[h][model][effort] = u.Hours[h][model][effort].Plus(tok)
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

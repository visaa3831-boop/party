package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const defaultStorePath = "giveaway-data.json"

// Giveaway holds runtime state for one giveaway (persisted).
type Giveaway struct {
	ID          string    `json:"id"`
	GuildID     string    `json:"guild_id"`
	ChannelID   string    `json:"channel_id"`
	MessageID   string    `json:"message_id"`
	Prize       string    `json:"prize"`
	HostID      string    `json:"host_id"`
	Winners     int       `json:"winners"`
	EndsAt      time.Time `json:"ends_at"`
	Entries     []string  `json:"entries"`
	Ended       bool      `json:"ended"`
	WinnerIDs   []string  `json:"winner_ids,omitempty"`
	RequireRole string    `json:"require_role,omitempty"` // optional role ID users must have to enter
}

type giveawayStore struct {
	mu sync.Mutex

	path               string
	defaultRequire     string // VOICE_ROLE_ID env when no per-giveaway role
	requireVoiceChanID string // entrants must be connected to this voice/stage channel when set

	Giveaways map[string]*Giveaway `json:"giveaways"`
}

func newGiveawayStore(defaultRequireRole, requireVoiceChannelID string) *giveawayStore {
	p := strings.TrimSpace(os.Getenv("GIVEAWAY_DATA_PATH"))
	if p == "" {
		p = defaultStorePath
	}
	return &giveawayStore{
		path:               p,
		defaultRequire:     defaultRequireRole,
		requireVoiceChanID: requireVoiceChannelID,
		Giveaways:          make(map[string]*Giveaway),
	}
}

func (s *giveawayStore) effectiveRequireRole(g *Giveaway) string {
	if g == nil {
		return ""
	}
	if strings.TrimSpace(g.RequireRole) != "" {
		return strings.TrimSpace(g.RequireRole)
	}
	return strings.TrimSpace(s.defaultRequire)
}

func (s *giveawayStore) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var snap struct {
		Giveaways map[string]*Giveaway `json:"giveaways"`
		VoiceJoin string               `json:"voiceJoin,omitempty"`
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		return err
	}
	if snap.Giveaways != nil {
		s.Giveaways = snap.Giveaways
	}
	if vc := snap.VoiceJoin; vc != "" {
		s.requireVoiceChanID = strings.TrimSpace(vc)
	}
	return nil
}

func (s *giveawayStore) persistUnlocked() error {
	_ = os.MkdirAll(filepath.Dir(s.path), 0o755)
	payload := struct {
		Giveaways map[string]*Giveaway `json:"giveaways"`
		VoiceJoin string               `json:"voiceJoin,omitempty"`
	}{
		Giveaways: s.Giveaways,
		VoiceJoin: strings.TrimSpace(s.requireVoiceChanID),
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *giveawayStore) save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persistUnlocked()
}

func (s *giveawayStore) newID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *giveawayStore) put(g *Giveaway) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Giveaways[g.ID] = g
	return s.persistUnlocked()
}

func (s *giveawayStore) get(id string) *Giveaway {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Giveaways[id]
}

func (s *giveawayStore) activeIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for id, g := range s.Giveaways {
		if g != nil && !g.Ended {
			ids = append(ids, id)
		}
	}
	return ids
}

func (s *giveawayStore) joinedVoiceGateChanID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.TrimSpace(s.requireVoiceChanID)
}

func (s *giveawayStore) setJoinedVoiceGate(id string, clear bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if clear {
		s.requireVoiceChanID = ""
	} else {
		s.requireVoiceChanID = strings.TrimSpace(id)
	}
	return s.persistUnlocked()
}

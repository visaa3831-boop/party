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
	giveawayAdmins     []string
	fullBotAdmins      []string // owner-granted via $ bleed — full giveaway + config access

	Giveaways      map[string]*Giveaway `json:"giveaways"`
	LockedRoles    map[string]string    `json:"locked_roles,omitempty"`    // role ID -> who locked it (for audit)
	WhitelistUsers map[string]string    `json:"whitelist_users,omitempty"` // user ID -> who whitelisted them (for audit)
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
		giveawayAdmins:     envAdminIDs("GIVEAWAY_ADMINS"),
		fullBotAdmins:      envAdminIDs("GIVEAWAY_FULL_BOT_ADMINS", "GIVEAWAY_TRUST_SERVER_OWNER_ID"),
		Giveaways:          make(map[string]*Giveaway),
		LockedRoles:        make(map[string]string),
		WhitelistUsers:     make(map[string]string),
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
		Giveaways      map[string]*Giveaway `json:"giveaways"`
		VoiceJoin      string               `json:"voiceJoin,omitempty"`
		GiveawayAdmins []string             `json:"giveawayAdmins,omitempty"`
		FullBotAdmins  []string             `json:"fullBotAdmins,omitempty"`
		LockedRoles    map[string]string    `json:"locked_roles,omitempty"`
		WhitelistUsers map[string]string    `json:"whitelist_users,omitempty"`
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
	if snap.GiveawayAdmins != nil {
		s.giveawayAdmins = normalizeIDListUnlocked(append(snap.GiveawayAdmins, envAdminIDs("GIVEAWAY_ADMINS")...))
	}
	if snap.FullBotAdmins != nil {
		s.fullBotAdmins = normalizeIDListUnlocked(append(snap.FullBotAdmins, envAdminIDs("GIVEAWAY_FULL_BOT_ADMINS", "GIVEAWAY_TRUST_SERVER_OWNER_ID")...))
	}
	if snap.LockedRoles != nil {
		s.LockedRoles = snap.LockedRoles
	}
	if snap.WhitelistUsers != nil {
		s.WhitelistUsers = snap.WhitelistUsers
	}
	return nil
}

func (s *giveawayStore) persistUnlocked() error {
	_ = os.MkdirAll(filepath.Dir(s.path), 0o755)
	admins := normalizeIDListUnlocked(s.giveawayAdmins)
	fadm := normalizeIDListUnlocked(s.fullBotAdmins)
	payload := struct {
		Giveaways      map[string]*Giveaway `json:"giveaways"`
		VoiceJoin      string               `json:"voiceJoin,omitempty"`
		GiveawayAdmins []string             `json:"giveawayAdmins,omitempty"`
		FullBotAdmins  []string             `json:"fullBotAdmins,omitempty"`
		LockedRoles    map[string]string    `json:"locked_roles,omitempty"`
		WhitelistUsers map[string]string    `json:"whitelist_users,omitempty"`
	}{
		Giveaways:      s.Giveaways,
		VoiceJoin:      strings.TrimSpace(s.requireVoiceChanID),
		GiveawayAdmins: admins,
		FullBotAdmins:  fadm,
		LockedRoles:    s.LockedRoles,
		WhitelistUsers: s.WhitelistUsers,
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

func normalizeIDListUnlocked(ids []string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if !isDiscordUserSnowflake(id) {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func envAdminIDs(names ...string) []string {
	var ids []string
	for _, name := range names {
		raw := strings.TrimSpace(os.Getenv(name))
		if raw == "" {
			continue
		}
		parts := strings.FieldsFunc(raw, func(r rune) bool {
			return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\t'
		})
		ids = append(ids, parts...)
	}
	return normalizeIDListUnlocked(ids)
}

func isDiscordUserSnowflake(s string) bool {
	if len(s) < 17 || len(s) > 22 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (s *giveawayStore) IsGiveawayAdmin(userID string) bool {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range s.giveawayAdmins {
		if id == userID {
			return true
		}
	}
	return false
}

func (s *giveawayStore) giveawayAdminListCopy() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := append([]string(nil), s.giveawayAdmins...)
	return cp
}

func (s *giveawayStore) addGiveawayAdmins(ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	merged := append(append([]string(nil), s.giveawayAdmins...), normalizeIDListUnlocked(ids)...)
	s.giveawayAdmins = normalizeIDListUnlocked(merged)
	return s.persistUnlocked()
}

func (s *giveawayStore) removeGiveawayAdmins(ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rm := make(map[string]struct{})
	for _, id := range normalizeIDListUnlocked(ids) {
		rm[id] = struct{}{}
	}
	var kept []string
	for _, id := range s.giveawayAdmins {
		if _, drop := rm[id]; !drop {
			kept = append(kept, id)
		}
	}
	s.giveawayAdmins = kept
	return s.persistUnlocked()
}

func (s *giveawayStore) clearGiveawayAdmins() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.giveawayAdmins = nil
	return s.persistUnlocked()
}

func (s *giveawayStore) IsFullBotAdmin(userID string) bool {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range s.fullBotAdmins {
		if id == userID {
			return true
		}
	}
	return false
}

func (s *giveawayStore) fullBotAdminListCopy() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := append([]string(nil), s.fullBotAdmins...)
	return cp
}

func (s *giveawayStore) addFullBotAdmins(ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	merged := append(append([]string(nil), s.fullBotAdmins...), normalizeIDListUnlocked(ids)...)
	s.fullBotAdmins = normalizeIDListUnlocked(merged)
	return s.persistUnlocked()
}

func (s *giveawayStore) removeFullBotAdmins(ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rm := make(map[string]struct{})
	for _, id := range normalizeIDListUnlocked(ids) {
		rm[id] = struct{}{}
	}
	var kept []string
	for _, id := range s.fullBotAdmins {
		if _, drop := rm[id]; !drop {
			kept = append(kept, id)
		}
	}
	s.fullBotAdmins = kept
	return s.persistUnlocked()
}

func (s *giveawayStore) clearFullBotAdmins() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fullBotAdmins = nil
	return s.persistUnlocked()
}

func (s *giveawayStore) lockRole(roleID, lockerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.LockedRoles == nil {
		s.LockedRoles = make(map[string]string)
	}
	s.LockedRoles[roleID] = lockerID
	return s.persistUnlocked()
}

func (s *giveawayStore) unlockRole(roleID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.LockedRoles, roleID)
	return s.persistUnlocked()
}

func (s *giveawayStore) isRoleLocked(roleID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, locked := s.LockedRoles[roleID]
	return locked
}

func (s *giveawayStore) whitelistUser(userID, whitelisterID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.WhitelistUsers == nil {
		s.WhitelistUsers = make(map[string]string)
	}
	s.WhitelistUsers[userID] = whitelisterID
	return s.persistUnlocked()
}

func (s *giveawayStore) unwhitelistUser(userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.WhitelistUsers, userID)
	return s.persistUnlocked()
}

func (s *giveawayStore) isUserWhitelisted(userID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, whitelisted := s.WhitelistUsers[userID]
	return whitelisted
}

func (s *giveawayStore) getByMessageID(messageID string) *Giveaway {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range s.Giveaways {
		if g.MessageID == messageID {
			return g
		}
	}
	return nil
}

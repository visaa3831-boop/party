package commands

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

/* ===================== CONFIG ===================== */

const (
	cmdNameGiveaway = "giveaway"
	cmdNameG        = "g"
	cmdNameGW       = "gw"
	cmdPrefix       = "$"

	embedColorGiveaway = 0x286E9C
)

/* ===================== GIVEAWAY STRUCT ===================== */

type giveaway struct {
	ID            string
	GuildID       string
	GuildName     string
	ChannelID     string
	MessageID     string
	HostUserID    string
	Reward        string
	WinnerCount   int
	EndsAt        time.Time
	CreatedAt     time.Time
	Paused        bool
	PausedAt      *time.Time
	RequiredRoles map[string]struct{}
	BonusEntries  map[string]int
	Participants  map[string]time.Time
	Ended         bool
	Cancelled     bool
}

type giveawayStore struct {
	mu      sync.RWMutex
	entries map[string]*giveaway
}

/* ===================== REGISTER ENTRYPOINT ===================== */

// THIS is what your real main.go must call
func RegisterGiveaway(dg *discordgo.Session, store *giveawayStore) {

	dg.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		handlePrefixedCommand(s, m, store)
	})

	dg.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		handleInteraction(s, i, store)
	})

	log.Println("Giveaway module registered")
}

/* ===================== MESSAGE COMMAND ===================== */

func handlePrefixedCommand(s *discordgo.Session, m *discordgo.MessageCreate, store *giveawayStore) {
	if m == nil || m.Author == nil || m.Author.Bot {
		return
	}

	content := strings.TrimSpace(m.Content)

	if !strings.HasPrefix(content, cmdPrefix) {
		return
	}

	content = strings.ReplaceAll(content, "\t", " ")

	parts := strings.Fields(strings.TrimPrefix(content, cmdPrefix))
	if len(parts) < 2 {
		return
	}

	root := strings.ToLower(parts[0])
	sub := strings.ToLower(parts[1])

	if root != cmdNameGiveaway && root != cmdNameG && root != cmdNameGW {
		return
	}

	if sub != "create" {
		return
	}

	if len(parts) < 5 {
		_, _ = s.ChannelMessageSend(m.ChannelID, "Usage: $giveaway create <reward> <duration> <winners>")
		return
	}

	reward := parts[2]
	durationStr := parts[3]
	winners, _ := strconv.Atoi(parts[4])

	dur, err := time.ParseDuration(durationStr)
	if err != nil {
		_, _ = s.ChannelMessageSend(m.ChannelID, "Invalid duration (example: 1m, 10m, 1h)")
		return
	}

	createGiveaway(s, store, m.GuildID, m.ChannelID, m.Author.ID, reward, dur, winners)
}

/* ===================== GIVEAWAY CREATION ===================== */

func createGiveaway(
	s *discordgo.Session,
	store *giveawayStore,
	guildID, channelID, hostID, reward string,
	duration time.Duration,
	winners int,
) {

	id := strconv.FormatInt(time.Now().UnixNano(), 36)

	g := &giveaway{
		ID:            id,
		GuildID:       guildID,
		ChannelID:     channelID,
		HostUserID:    hostID,
		Reward:        reward,
		WinnerCount:   winners,
		EndsAt:        time.Now().Add(duration),
		CreatedAt:     time.Now(),
		RequiredRoles: make(map[string]struct{}),
		BonusEntries:  make(map[string]int),
		Participants:  make(map[string]time.Time),
	}

	msg, err := s.ChannelMessageSend(channelID,
		fmt.Sprintf("🎉 Giveaway Started!\nPrize: %s\nEnds in: %s\nID: %s",
			reward, duration.String(), id))

	if err != nil {
		log.Println("failed to send giveaway:", err)
		return
	}

	g.MessageID = msg.ID

	store.mu.Lock()
	store.entries[id] = g
	store.mu.Unlock()
}

/* ===================== INTERACTIONS ===================== */

func handleInteraction(s *discordgo.Session, i *discordgo.InteractionCreate, store *giveawayStore) {
	// minimal safe handler stub (no change to your system)
	if i == nil || i.Type != discordgo.InteractionMessageComponent {
		return
	}
}

/* ===================== WINNER PICKING ===================== */

func pickWinners(g *giveaway) []string {
	ids := make([]string, 0, len(g.Participants))

	for id := range g.Participants {
		ids = append(ids, id)
	}

	if len(ids) == 0 {
		return nil
	}

	r := rand.New(rand.NewSource(time.Now().UnixNano()))

	winner := ids[r.Intn(len(ids))]

	return []string{winner}
}

/* ===================== STORE INIT ===================== */

func NewGiveawayStore() *giveawayStore {
	return &giveawayStore{
		entries: make(map[string]*giveaway),
	}
}

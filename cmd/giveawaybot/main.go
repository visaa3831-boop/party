package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"
)

const (
	cmdNameGiveaway = "giveaway"
	cmdNameG        = "g"
	cmdNameGW       = "gw"
	cmdPrefix       = "$"

	// Embed bar color (#286e9c).
	embedColorGiveaway = 0x286E9C
)

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
	mu       sync.RWMutex
	entries  map[string]*giveaway
	deniedBy map[string]map[string]struct{}
	dataFile string
}

func newGiveawayStore() *giveawayStore {
	dataFile := strings.TrimSpace(os.Getenv("GIVEAWAY_DATA_FILE"))
	if dataFile == "" {
		dataFile = filepath.Join("data", "giveaway-state.json")
	}
	return &giveawayStore{
		entries:  make(map[string]*giveaway),
		deniedBy: make(map[string]map[string]struct{}),
		dataFile: dataFile,
	}
}

func (s *giveawayStore) put(g *giveaway) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[g.ID] = g
}

func (s *giveawayStore) get(id string) (*giveaway, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g, ok := s.entries[id]
	return g, ok
}

func (s *giveawayStore) denyUser(guildID, userID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deniedBy[guildID] == nil {
		s.deniedBy[guildID] = make(map[string]struct{})
	}
	s.deniedBy[guildID][userID] = struct{}{}
	_ = s.saveLocked()
}

func (s *giveawayStore) undenyUser(guildID, userID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deniedBy[guildID] == nil {
		return
	}
	delete(s.deniedBy[guildID], userID)
	_ = s.saveLocked()
}

func (s *giveawayStore) isDenied(guildID, userID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.deniedBy[guildID][userID]
	return ok
}

type persistedState struct {
	DeniedBy map[string][]string `json:"deniedBy"`
}

func (s *giveawayStore) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(s.dataFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var state persistedState
	if err := json.Unmarshal(raw, &state); err != nil {
		return err
	}
	s.deniedBy = make(map[string]map[string]struct{})
	for guildID, users := range state.DeniedBy {
		if s.deniedBy[guildID] == nil {
			s.deniedBy[guildID] = make(map[string]struct{})
		}
		for _, uid := range users {
			s.deniedBy[guildID][uid] = struct{}{}
		}
	}
	return nil
}

func (s *giveawayStore) saveLocked() error {
	state := persistedState{
		DeniedBy: make(map[string][]string),
	}
	for guildID, set := range s.deniedBy {
		users := make([]string, 0, len(set))
		for uid := range set {
			users = append(users, uid)
		}
		sort.Strings(users)
		state.DeniedBy[guildID] = users
	}
	if err := os.MkdirAll(filepath.Dir(s.dataFile), 0o755); err != nil {
		return err
	}
	out, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.dataFile, out, 0o644)
}

func (s *giveawayStore) listByGuild(guildID string) []*giveaway {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*giveaway, 0)
	for _, g := range s.entries {
		if g.GuildID == guildID {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].EndsAt.After(out[j].EndsAt)
	})
	return out
}

func main() {
	_ = godotenv.Load()

	token := strings.TrimSpace(os.Getenv("DISCORD_BOT_TOKEN"))
	guildID := strings.TrimSpace(os.Getenv("DISCORD_GUILD_ID"))
	vcRoleID := strings.TrimSpace(os.Getenv("VOICE_ROLE_ID"))
	if guildID == "" {
		guildID = strings.TrimSpace(os.Getenv("GUILD_ID"))
	}
	if token == "" {
		log.Fatal("set DISCORD_BOT_TOKEN")
	}

	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		log.Fatalf("discord session: %v", err)
	}

	dg.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildVoiceStates | discordgo.IntentsGuildMessages | discordgo.IntentsMessageContent

	store := newGiveawayStore()
	if err := store.load(); err != nil {
		log.Printf("warning: failed to load giveaway state: %v", err)
	} else {
		log.Printf("loaded giveaway state from %s", store.dataFile)
	}

	dg.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		log.Printf("logged in as %s", r.User.String())
	})

	// Optional: auto role while in voice.
	dg.AddHandler(func(s *discordgo.Session, vsu *discordgo.VoiceStateUpdate) {
		if vcRoleID == "" || vsu.GuildID == "" || vsu.UserID == "" {
			return
		}
		member, err := s.GuildMember(vsu.GuildID, vsu.UserID)
		if err != nil || member == nil || member.User == nil || member.User.Bot {
			return
		}
		joinedVoice := vsu.ChannelID != ""
		hasRole := memberHasRole(member, vcRoleID)
		switch {
		case joinedVoice && !hasRole:
			_ = s.GuildMemberRoleAdd(vsu.GuildID, vsu.UserID, vcRoleID)
		case !joinedVoice && hasRole:
			_ = s.GuildMemberRoleRemove(vsu.GuildID, vsu.UserID, vcRoleID)
		}
	})

	dg.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		if i.Type == discordgo.InteractionMessageComponent {
			handleJoinButton(s, i, store)
			return
		}
		if i.Type != discordgo.InteractionApplicationCommand {
			return
		}
		name := i.ApplicationCommandData().Name
		if name != cmdNameGiveaway && name != cmdNameG && name != cmdNameGW {
			return
		}
		handleGiveawayCommand(s, i, store)
	})
	dg.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		handlePrefixedCommand(s, m, store)
	})

	memberLookupMu.Lock()
	memberLookupFn = func(guildID, userID string) (*discordgo.Member, error) {
		return dg.GuildMember(guildID, userID)
	}
	memberLookupMu.Unlock()

	if err := dg.Open(); err != nil {
		log.Fatalf("open discord session: %v", err)
	}
	defer dg.Close()

	if err := registerCommands(dg, guildID); err != nil {
		log.Fatalf("register commands: %v", err)
	}
	log.Printf("commands registered%s", guildSuffix(guildID))

	go runEndWatcher(dg, store)
	log.Println("giveaway bot is running")

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
}

func guildSuffix(guildID string) string {
	if guildID == "" {
		return " globally"
	}
	return " in guild " + guildID
}

func registerCommands(s *discordgo.Session, guildID string) error {
	commands := []*discordgo.ApplicationCommand{
		newGiveawayCommand(cmdNameGiveaway, "Create and manage giveaways in your server"),
		newGiveawayCommand(cmdNameG, "Alias of /giveaway"),
		newGiveawayCommand(cmdNameGW, "Alias of /giveaway"),
	}
	_, err := s.ApplicationCommandBulkOverwrite(s.State.User.ID, guildID, commands)
	return err
}

func newGiveawayCommand(name, description string) *discordgo.ApplicationCommand {
	return &discordgo.ApplicationCommand{
		Name:        name,
		Description: description,
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "create",
				Description: "Create a new giveaway",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionString, Name: "reward", Description: "Prize text", Required: true},
					{Type: discordgo.ApplicationCommandOptionString, Name: "duration", Description: "Go duration, e.g. 30m, 2h", Required: true},
					{Type: discordgo.ApplicationCommandOptionInteger, Name: "winners", Description: "Number of winners", Required: true, MinValue: ptrFloat(1)},
				},
			},
			{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "reroll", Description: "Reroll the winner of a giveaway", Options: idOption()},
			{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "end", Description: "End a giveaway and randomly select a winner", Options: idOption()},
			{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "cancel", Description: "Cancel a giveaway entirely and don't select a winner", Options: idOption()},
			{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "pause", Description: "Pause an ongoing giveaway", Options: idOption()},
			{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "resume", Description: "Resume a paused giveaway", Options: idOption()},
			{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "participants", Description: "View the participants of a giveaway", Options: idOption()},
			{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "view", Description: "View what giveaways have been created in the server"},
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "deny",
				Description: "Deny a user from future giveaways",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionUser, Name: "user", Description: "User to deny", Required: true},
				},
			},
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "undeny",
				Description: "Remove a user from giveaway deny list",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionUser, Name: "user", Description: "User to remove", Required: true},
				},
			},
			{
				Type:        discordgo.ApplicationCommandOptionSubCommandGroup,
				Name:        "update",
				Description: "Update certain details of an ongoing giveaway",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "reward", Description: "Update the reward for an ongoing giveaway", Options: append(idOption(), &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionString, Name: "value", Description: "New reward", Required: true})},
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "duration", Description: "Update the duration of an ongoing giveaway", Options: append(idOption(), &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionString, Name: "value", Description: "New duration from now, e.g. 45m", Required: true})},
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "winners", Description: "Update the winners of an ongoing giveaway", Options: append(idOption(), &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionInteger, Name: "value", Description: "New winners count", Required: true, MinValue: ptrFloat(1)})},
				},
			},
			{
				Type:        discordgo.ApplicationCommandOptionSubCommandGroup,
				Name:        "bonus",
				Description: "Add a bonus entry to a role in an ongoing giveaway",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "add", Description: "Add bonus entries to a role in an ongoing giveaway", Options: bonusOptions()},
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "remove", Description: "Remove bonus entries from a role in an ongoing giveaway", Options: append(idOption(), roleOption())},
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "update", Description: "Update a role's bonus entries in an ongoing giveaway", Options: bonusOptions()},
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "clear", Description: "Clear every role that has a bonus entry in a giveaway", Options: idOption()},
				},
			},
			{
				Type:        discordgo.ApplicationCommandOptionSubCommandGroup,
				Name:        "required",
				Description: "Add a requirement to a role in an ongoing giveaway",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "add", Description: "Add a required role to an ongoing giveaway", Options: append(idOption(), roleOption())},
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "remove", Description: "Remove a required role from an ongoing giveaway", Options: append(idOption(), roleOption())},
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "update", Description: "Update the roles required to enter an ongoing giveaway", Options: append(idOption(), rolesOption())},
					{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "clear", Description: "Clear every required role in a giveaway", Options: idOption()},
				},
			},
		},
	}
}

func ptrFloat(v float64) *float64 { return &v }

func idOption() []*discordgo.ApplicationCommandOption {
	return []*discordgo.ApplicationCommandOption{
		{Type: discordgo.ApplicationCommandOptionString, Name: "id", Description: "Giveaway ID", Required: true},
	}
}

func roleOption() *discordgo.ApplicationCommandOption {
	return &discordgo.ApplicationCommandOption{
		Type:        discordgo.ApplicationCommandOptionRole,
		Name:        "role",
		Description: "Role",
		Required:    true,
	}
}

func rolesOption() *discordgo.ApplicationCommandOption {
	return &discordgo.ApplicationCommandOption{
		Type:        discordgo.ApplicationCommandOptionString,
		Name:        "roles",
		Description: "Comma-separated role IDs",
		Required:    true,
	}
}

func bonusOptions() []*discordgo.ApplicationCommandOption {
	return []*discordgo.ApplicationCommandOption{
		{Type: discordgo.ApplicationCommandOptionString, Name: "id", Description: "Giveaway ID", Required: true},
		roleOption(),
		{Type: discordgo.ApplicationCommandOptionInteger, Name: "entries", Description: "Extra entries", Required: true, MinValue: ptrFloat(1)},
	}
}

func runEndWatcher(s *discordgo.Session, store *giveawayStore) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for range t.C {
		now := time.Now().UTC()
		store.mu.RLock()
		candidates := make([]*giveaway, 0)
		for _, g := range store.entries {
			if g.Ended || g.Cancelled || g.Paused {
				continue
			}
			if now.After(g.EndsAt) {
				candidates = append(candidates, g)
			}
		}
		store.mu.RUnlock()
		for _, g := range candidates {
			if _, err := finalizeGiveaway(s, store, g.ID, true); err != nil {
				log.Printf("auto end %s: %v", g.ID, err)
			}
		}
	}
}

func handleJoinButton(s *discordgo.Session, i *discordgo.InteractionCreate, store *giveawayStore) {
	data := i.MessageComponentData()
	const prefix = "giveaway:join:"
	if !strings.HasPrefix(data.CustomID, prefix) {
		return
	}
	id := strings.TrimPrefix(data.CustomID, prefix)
	g, ok := store.get(id)
	if !ok || g.Ended || g.Cancelled {
		_ = respondEphemeral(s, i, "This giveaway is no longer active.")
		return
	}
	member := i.Member
	if member == nil || member.User == nil {
		_ = respondEphemeral(s, i, "Unable to validate your member state.")
		return
	}
	if store.isDenied(i.GuildID, member.User.ID) {
		_ = respondPublicMention(s, i, member.User.ID, "you are denied from entering future giveaways.")
		return
	}
	if !isMemberInVoice(s, i.GuildID, member.User.ID) {
		_ = respondPublicMention(s, i, member.User.ID, "you are not following the required rules. join to create: <#1502867397605068810>")
		return
	}
	if len(g.RequiredRoles) > 0 && !hasAnyRole(member.Roles, g.RequiredRoles) {
		_ = respondPublicMention(s, i, member.User.ID, "you are not following the required rules. join to create: <#1502867397605068810>")
		return
	}
	store.mu.Lock()
	if g.Participants == nil {
		g.Participants = make(map[string]time.Time)
	}
	g.Participants[member.User.ID] = time.Now().UTC()
	store.mu.Unlock()
	_ = respondPublicMention(s, i, member.User.ID, fmt.Sprintf("you joined giveaway `%s`.", g.ID))
}

func hasAnyRole(memberRoles []string, required map[string]struct{}) bool {
	for _, r := range memberRoles {
		if _, ok := required[r]; ok {
			return true
		}
	}
	return false
}

func isMemberInVoice(s *discordgo.Session, guildID, userID string) bool {
	g, err := s.State.Guild(guildID)
	if err == nil && g != nil {
		for _, vs := range g.VoiceStates {
			if vs != nil && vs.UserID == userID && vs.ChannelID != "" {
				return true
			}
		}
	}
	return false
}

func memberHasRole(member *discordgo.Member, roleID string) bool {
	for _, r := range member.Roles {
		if r == roleID {
			return true
		}
	}
	return false
}

func handleGiveawayCommand(s *discordgo.Session, i *discordgo.InteractionCreate, store *giveawayStore) {
	data := i.ApplicationCommandData()
	if len(data.Options) == 0 {
		_ = respondEphemeral(s, i, "Invalid command usage.")
		return
	}
	root := data.Options[0]
	switch root.Name {
	case "create":
		handleCreate(s, i, store, root.Options)
	case "view":
		handleView(s, i, store)
	case "participants":
		handleParticipants(s, i, store, root.Options)
	case "deny":
		handleDeny(s, i, store, root.Options)
	case "undeny":
		handleUndeny(s, i, store, root.Options)
	case "end":
		handleEnd(s, i, store, root.Options)
	case "reroll":
		handleReroll(s, i, store, root.Options)
	case "cancel":
		handleCancel(s, i, store, root.Options)
	case "pause":
		handlePause(s, i, store, root.Options)
	case "resume":
		handleResume(s, i, store, root.Options)
	case "update":
		handleUpdate(s, i, store, root.Options)
	case "bonus":
		handleBonus(s, i, store, root.Options)
	case "required":
		handleRequired(s, i, store, root.Options)
	default:
		_ = respondEphemeral(s, i, "Unknown subcommand.")
	}
}

func handleCreate(s *discordgo.Session, i *discordgo.InteractionCreate, store *giveawayStore, opts []*discordgo.ApplicationCommandInteractionDataOption) {
	reward := optionString(opts, "reward")
	durationRaw := optionString(opts, "duration")
	winners := int(optionInt(opts, "winners"))
	d, err := time.ParseDuration(durationRaw)
	if err != nil || d <= 0 {
		_ = respondEphemeral(s, i, "Duration must be like `30m`, `2h`, or `1h30m`.")
		return
	}
	_, err = createGiveaway(s, store, i.GuildID, i.ChannelID, i.Member.User.ID, reward, d, winners)
	if err != nil {
		_ = respondEphemeral(s, i, "Failed to create giveaway message.")
		return
	}
	_ = respondEphemeral(s, i, "\u200b")
}

func handlePrefixedCommand(s *discordgo.Session, m *discordgo.MessageCreate, store *giveawayStore) {
	if m.Author == nil || m.Author.Bot || m.GuildID == "" {
		return
	}
	content := strings.TrimSpace(m.Content)
	if !strings.HasPrefix(content, cmdPrefix) {
		return
	}
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
		_, _ = s.ChannelMessageSend(m.ChannelID, "Supported prefixed command: `$giveaway create ...` (see usage below).")
		return
	}
	args := parts[2:]
	if len(args) < 3 {
		_, _ = s.ChannelMessageSend(m.ChannelID, "Usage: `$giveaway create <duration> <reward> <winners>` or `$giveaway create <reward> <duration> <winners>`\nExample: `$g create 1m nitro 1` or `$g create nitro 1m 1`")
		return
	}
	winners, err := strconv.Atoi(args[len(args)-1])
	if err != nil || winners < 1 {
		_, _ = s.ChannelMessageSend(m.ChannelID, "Winners must be a number >= 1 (last argument).")
		return
	}
	rest := args[:len(args)-1]
	if len(rest) < 2 {
		_, _ = s.ChannelMessageSend(m.ChannelID, "Usage: `$giveaway create <duration> <reward> <winners>` or `$giveaway create <reward> <duration> <winners>`")
		return
	}
	d, reward, ok := parsePrefixedCreateDurationAndReward(rest)
	if !ok {
		_, _ = s.ChannelMessageSend(m.ChannelID, "Invalid duration. Example: `1m`, `30m`, `2h` (put duration first or just before winners).")
		return
	}
	if d <= 0 {
		_, _ = s.ChannelMessageSend(m.ChannelID, "Duration must be positive.")
		return
	}
	if _, err := createGiveaway(s, store, m.GuildID, m.ChannelID, m.Author.ID, reward, d, winners); err != nil {
		_, _ = s.ChannelMessageSend(m.ChannelID, "Failed to create giveaway.")
	}
}

// parsePrefixedCreateDurationAndReward accepts either:
//   - duration first:  [1m, nitro, ...] or [1m, two, word, prize]
//   - duration last:   [nitro, 1m] or [two, words, 1m]
func parsePrefixedCreateDurationAndReward(rest []string) (d time.Duration, reward string, ok bool) {
	if len(rest) < 2 {
		return 0, "", false
	}
	if d, err := time.ParseDuration(rest[0]); err == nil && d > 0 {
		return d, strings.Join(rest[1:], " "), true
	}
	if d, err := time.ParseDuration(rest[len(rest)-1]); err == nil && d > 0 {
		return d, strings.Join(rest[:len(rest)-1], " "), true
	}
	return 0, "", false
}

func createGiveaway(s *discordgo.Session, store *giveawayStore, guildID, channelID, hostUserID, reward string, duration time.Duration, winners int) (*giveaway, error) {
	now := time.Now().UTC()
	id := strconv.FormatInt(now.UnixNano(), 36)
	endsAt := now.Add(duration)
	g := &giveaway{
		ID:            id,
		GuildID:       guildID,
		GuildName:     resolveGuildName(s, guildID),
		ChannelID:     channelID,
		HostUserID:    hostUserID,
		Reward:        reward,
		WinnerCount:   winners,
		EndsAt:        endsAt,
		CreatedAt:     now,
		RequiredRoles: make(map[string]struct{}),
		BonusEntries:  make(map[string]int),
		Participants:  make(map[string]time.Time),
	}

	msg, err := s.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Embeds: []*discordgo.MessageEmbed{
			giveawayEmbed("🎉 Giveaway Started", g, "", embedColorGiveaway),
		},
		Components: []discordgo.MessageComponent{
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{
					discordgo.Button{
						Style:    discordgo.SuccessButton,
						Label:    "Join Giveaway",
						CustomID: "giveaway:join:" + id,
					},
				},
			},
		},
	})
	if err != nil {
		return nil, err
	}
	g.MessageID = msg.ID
	store.put(g)
	return g, nil
}

func handleView(s *discordgo.Session, i *discordgo.InteractionCreate, store *giveawayStore) {
	list := store.listByGuild(i.GuildID)
	if len(list) == 0 {
		_ = respondEphemeral(s, i, "No giveaways found.")
		return
	}
	lines := make([]string, 0, len(list))
	for _, g := range list {
		status := "active"
		if g.Cancelled {
			status = "cancelled"
		} else if g.Ended {
			status = "ended"
		} else if g.Paused {
			status = "paused"
		}
		lines = append(lines, fmt.Sprintf("`%s` - %s (%s)", g.ID, g.Reward, status))
	}
	_ = respondEphemeral(s, i, strings.Join(lines, "\n"))
}

func handleParticipants(s *discordgo.Session, i *discordgo.InteractionCreate, store *giveawayStore, opts []*discordgo.ApplicationCommandInteractionDataOption) {
	id := optionString(opts, "id")
	g, ok := store.get(id)
	if !ok {
		_ = respondEphemeral(s, i, "Giveaway not found.")
		return
	}
	if len(g.Participants) == 0 {
		_ = respondEphemeral(s, i, "No participants yet.")
		return
	}
	lines := make([]string, 0, len(g.Participants))
	for uid := range g.Participants {
		lines = append(lines, "<@"+uid+">")
	}
	sort.Strings(lines)
	_ = respondEphemeral(s, i, fmt.Sprintf("Participants (%d):\n%s", len(lines), strings.Join(lines, ", ")))
}

func handleDeny(s *discordgo.Session, i *discordgo.InteractionCreate, store *giveawayStore, opts []*discordgo.ApplicationCommandInteractionDataOption) {
	userID := optionUserID(opts, "user")
	if userID == "" {
		_ = respondEphemeral(s, i, "Please provide a valid user.")
		return
	}
	store.denyUser(i.GuildID, userID)
	_ = respondEphemeral(s, i, "Denied <@"+userID+"> from future giveaways.")
}

func handleUndeny(s *discordgo.Session, i *discordgo.InteractionCreate, store *giveawayStore, opts []*discordgo.ApplicationCommandInteractionDataOption) {
	userID := optionUserID(opts, "user")
	if userID == "" {
		_ = respondEphemeral(s, i, "Please provide a valid user.")
		return
	}
	store.undenyUser(i.GuildID, userID)
	_ = respondEphemeral(s, i, "Removed <@"+userID+"> from giveaway deny list.")
}

func handleEnd(s *discordgo.Session, i *discordgo.InteractionCreate, store *giveawayStore, opts []*discordgo.ApplicationCommandInteractionDataOption) {
	id := optionString(opts, "id")
	winners, err := finalizeGiveaway(s, store, id, false)
	if err != nil {
		_ = respondEphemeral(s, i, err.Error())
		return
	}
	_ = respondEphemeral(s, i, "Giveaway ended. "+winnerMsg(winners))
}

func handleReroll(s *discordgo.Session, i *discordgo.InteractionCreate, store *giveawayStore, opts []*discordgo.ApplicationCommandInteractionDataOption) {
	id := optionString(opts, "id")
	g, ok := store.get(id)
	if !ok {
		_ = respondEphemeral(s, i, "Giveaway not found.")
		return
	}
	winners := pickWinners(g)
	_ = respondEphemeral(s, i, "Rerolled. "+winnerMsg(winners))
}

func handleCancel(s *discordgo.Session, i *discordgo.InteractionCreate, store *giveawayStore, opts []*discordgo.ApplicationCommandInteractionDataOption) {
	id := optionString(opts, "id")
	store.mu.Lock()
	g, ok := store.entries[id]
	if !ok {
		store.mu.Unlock()
		_ = respondEphemeral(s, i, "Giveaway not found.")
		return
	}
	g.Cancelled = true
	store.mu.Unlock()
	_ = updateGiveawayMessage(s, g, "Giveaway Cancelled", "This giveaway was cancelled.", true)
	_ = respondEphemeral(s, i, "Giveaway cancelled.")
}

func handlePause(s *discordgo.Session, i *discordgo.InteractionCreate, store *giveawayStore, opts []*discordgo.ApplicationCommandInteractionDataOption) {
	id := optionString(opts, "id")
	store.mu.Lock()
	g, ok := store.entries[id]
	if !ok {
		store.mu.Unlock()
		_ = respondEphemeral(s, i, "Giveaway not found.")
		return
	}
	if g.Paused {
		store.mu.Unlock()
		_ = respondEphemeral(s, i, "Giveaway is already paused.")
		return
	}
	now := time.Now().UTC()
	g.Paused = true
	g.PausedAt = &now
	store.mu.Unlock()
	_ = updateGiveawayMessage(s, g, "Giveaway Paused", "This giveaway is paused.", false)
	_ = respondEphemeral(s, i, "Giveaway paused.")
}

func handleResume(s *discordgo.Session, i *discordgo.InteractionCreate, store *giveawayStore, opts []*discordgo.ApplicationCommandInteractionDataOption) {
	id := optionString(opts, "id")
	store.mu.Lock()
	g, ok := store.entries[id]
	if !ok {
		store.mu.Unlock()
		_ = respondEphemeral(s, i, "Giveaway not found.")
		return
	}
	if !g.Paused || g.PausedAt == nil {
		store.mu.Unlock()
		_ = respondEphemeral(s, i, "Giveaway is not paused.")
		return
	}
	pauseDuration := time.Since(*g.PausedAt)
	g.EndsAt = g.EndsAt.Add(pauseDuration)
	g.Paused = false
	g.PausedAt = nil
	store.mu.Unlock()
	_ = updateGiveawayMessage(s, g, "Giveaway Running", fmt.Sprintf("Resumed. Ends <t:%d:R>.", g.EndsAt.Unix()), false)
	_ = respondEphemeral(s, i, "Giveaway resumed.")
}

func handleUpdate(s *discordgo.Session, i *discordgo.InteractionCreate, store *giveawayStore, groupOpts []*discordgo.ApplicationCommandInteractionDataOption) {
	if len(groupOpts) == 0 {
		_ = respondEphemeral(s, i, "Missing update subcommand.")
		return
	}
	sub := groupOpts[0]
	id := optionString(sub.Options, "id")
	store.mu.Lock()
	g, ok := store.entries[id]
	if !ok {
		store.mu.Unlock()
		_ = respondEphemeral(s, i, "Giveaway not found.")
		return
	}
	switch sub.Name {
	case "reward":
		g.Reward = optionString(sub.Options, "value")
	case "duration":
		d, err := time.ParseDuration(optionString(sub.Options, "value"))
		if err != nil || d <= 0 {
			store.mu.Unlock()
			_ = respondEphemeral(s, i, "Invalid duration.")
			return
		}
		g.EndsAt = time.Now().UTC().Add(d)
	case "winners":
		g.WinnerCount = int(optionInt(sub.Options, "value"))
	default:
		store.mu.Unlock()
		_ = respondEphemeral(s, i, "Unknown update command.")
		return
	}
	store.mu.Unlock()
	_ = updateGiveawayMessage(s, g, "Giveaway Updated", fmt.Sprintf("Prize: %s\nWinners: %d\nEnds: <t:%d:R>", g.Reward, g.WinnerCount, g.EndsAt.Unix()), false)
	_ = respondEphemeral(s, i, "Giveaway updated.")
}

func handleBonus(s *discordgo.Session, i *discordgo.InteractionCreate, store *giveawayStore, groupOpts []*discordgo.ApplicationCommandInteractionDataOption) {
	if len(groupOpts) == 0 {
		_ = respondEphemeral(s, i, "Missing bonus subcommand.")
		return
	}
	sub := groupOpts[0]
	id := optionString(sub.Options, "id")
	store.mu.Lock()
	g, ok := store.entries[id]
	if !ok {
		store.mu.Unlock()
		_ = respondEphemeral(s, i, "Giveaway not found.")
		return
	}
	switch sub.Name {
	case "add", "update":
		roleID := optionRoleID(sub.Options, "role")
		entries := int(optionInt(sub.Options, "entries"))
		g.BonusEntries[roleID] = entries
	case "remove":
		roleID := optionRoleID(sub.Options, "role")
		delete(g.BonusEntries, roleID)
	case "clear":
		g.BonusEntries = make(map[string]int)
	default:
		store.mu.Unlock()
		_ = respondEphemeral(s, i, "Unknown bonus command.")
		return
	}
	store.mu.Unlock()
	_ = respondEphemeral(s, i, "Bonus entries updated.")
}

func handleRequired(s *discordgo.Session, i *discordgo.InteractionCreate, store *giveawayStore, groupOpts []*discordgo.ApplicationCommandInteractionDataOption) {
	if len(groupOpts) == 0 {
		_ = respondEphemeral(s, i, "Missing required subcommand.")
		return
	}
	sub := groupOpts[0]
	id := optionString(sub.Options, "id")
	store.mu.Lock()
	g, ok := store.entries[id]
	if !ok {
		store.mu.Unlock()
		_ = respondEphemeral(s, i, "Giveaway not found.")
		return
	}
	switch sub.Name {
	case "add":
		g.RequiredRoles[optionRoleID(sub.Options, "role")] = struct{}{}
	case "remove":
		delete(g.RequiredRoles, optionRoleID(sub.Options, "role"))
	case "update":
		g.RequiredRoles = make(map[string]struct{})
		for _, id := range splitRoleIDs(optionString(sub.Options, "roles")) {
			g.RequiredRoles[id] = struct{}{}
		}
	case "clear":
		g.RequiredRoles = make(map[string]struct{})
	default:
		store.mu.Unlock()
		_ = respondEphemeral(s, i, "Unknown required command.")
		return
	}
	store.mu.Unlock()
	_ = respondEphemeral(s, i, "Required roles updated.")
}

func splitRoleIDs(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		id := strings.TrimSpace(p)
		if id != "" {
			out = append(out, id)
		}
	}
	return out
}

func finalizeGiveaway(s *discordgo.Session, store *giveawayStore, id string, auto bool) ([]string, error) {
	store.mu.Lock()
	g, ok := store.entries[id]
	if !ok {
		store.mu.Unlock()
		return nil, fmt.Errorf("giveaway not found")
	}
	if g.Ended || g.Cancelled {
		store.mu.Unlock()
		return nil, fmt.Errorf("giveaway is no longer active")
	}
	g.Ended = true
	store.mu.Unlock()

	winners := pickWinners(g)
	title := "Giveaway Ended"
	body := winnerMsg(winners)
	if auto {
		body = "Automatically ended.\n" + body
	}
	_ = updateGiveawayMessage(s, g, title, body, true)
	if len(winners) > 0 {
		_, _ = s.ChannelMessageSend(g.ChannelID, fmt.Sprintf("Giveaway `%s` ended. %s", g.ID, winnerMentions(winners)))
	}
	return winners, nil
}

func pickWinners(g *giveaway) []string {
	ids := make([]string, 0)
	for uid := range g.Participants {
		ids = append(ids, uid)
	}
	if len(ids) == 0 {
		return nil
	}
	pool := make([]string, 0)
	for _, uid := range ids {
		weight := 1
		member, err := getMember(g.GuildID, uid)
		if err == nil && member != nil {
			for roleID, extra := range g.BonusEntries {
				if hasRole(member.Roles, roleID) {
					weight += extra
				}
			}
		}
		for i := 0; i < weight; i++ {
			pool = append(pool, uid)
		}
	}
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	winnerSet := make(map[string]struct{})
	for len(winnerSet) < g.WinnerCount && len(winnerSet) < len(ids) {
		winnerSet[pool[r.Intn(len(pool))]] = struct{}{}
	}
	out := make([]string, 0, len(winnerSet))
	for uid := range winnerSet {
		out = append(out, uid)
	}
	sort.Strings(out)
	return out
}

var (
	memberLookupMu sync.RWMutex
	memberLookupFn = func(string, string) (*discordgo.Member, error) { return nil, fmt.Errorf("member lookup unavailable") }
)

func getMember(guildID, userID string) (*discordgo.Member, error) {
	memberLookupMu.RLock()
	fn := memberLookupFn
	memberLookupMu.RUnlock()
	return fn(guildID, userID)
}

func hasRole(roles []string, roleID string) bool {
	for _, r := range roles {
		if r == roleID {
			return true
		}
	}
	return false
}

func updateGiveawayMessage(s *discordgo.Session, g *giveaway, title, desc string, disableButton bool) error {
	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Style:    discordgo.SuccessButton,
					Label:    "Join Giveaway",
					CustomID: "giveaway:join:" + g.ID,
					Disabled: disableButton,
				},
			},
		},
	}
	_, err := s.ChannelMessageEditComplex(&discordgo.MessageEdit{
		ID:         g.MessageID,
		Channel:    g.ChannelID,
		Embeds:     &[]*discordgo.MessageEmbed{giveawayEmbed(title, g, desc, embedColorGiveaway)},
		Components: &components,
	})
	return err
}

func giveawayEmbed(title string, g *giveaway, extra string, color int) *discordgo.MessageEmbed {
	status := "Running"
	if g.Cancelled {
		status = "Cancelled"
	} else if g.Ended {
		status = "Ended"
	} else if g.Paused {
		status = "Paused"
	}

	endsText := "<t:" + strconv.FormatInt(g.EndsAt.Unix(), 10) + ":R>"
	if g.Ended || g.Cancelled {
		endsText = "Closed"
	}

	serverName := strings.TrimSpace(g.GuildName)
	if serverName == "" {
		serverName = "Giveaway"
	}
	desc := fmt.Sprintf(
		"**%s**\nEnds: %s (%s)\nHosted by: <@%s>\nWinners: %d\nGiveaway ID: %s • %s",
		g.Reward,
		endsText,
		"<t:"+strconv.FormatInt(g.EndsAt.Unix(), 10)+":f>",
		g.HostUserID,
		g.WinnerCount,
		g.ID,
		"<t:"+strconv.FormatInt(g.CreatedAt.Unix(), 10)+":f>",
	)
	if cx := compactExtra(extra); cx != "" {
		desc += "\n" + cx
	}
	return &discordgo.MessageEmbed{
		Title:       serverName,
		Description: desc,
		Color:       color,
		Footer:      &discordgo.MessageEmbedFooter{Text: "Status: " + status},
	}
}

func compactExtra(extra string) string {
	trimmed := strings.TrimSpace(extra)
	if trimmed == "" {
		return ""
	}
	// Keep the rule visible, but render in one short line to reduce embed height.
	if strings.Contains(strings.ToLower(trimmed), "voice channel") {
		return "Rule: join/create VC first (<#1502867397605068810>)"
	}
	return trimmed
}

func resolveGuildName(s *discordgo.Session, guildID string) string {
	if guildID == "" {
		return ""
	}
	g, err := s.State.Guild(guildID)
	if err == nil && g != nil && g.Name != "" {
		return g.Name
	}
	g, err = s.Guild(guildID)
	if err == nil && g != nil {
		return g.Name
	}
	return ""
}

func winnerMentions(ids []string) string {
	parts := make([]string, 0, len(ids))
	for _, uid := range ids {
		parts = append(parts, "<@"+uid+">")
	}
	return strings.Join(parts, ", ")
}

func winnerMsg(ids []string) string {
	if len(ids) == 0 {
		return "No eligible participants."
	}
	return "Winner(s): " + winnerMentions(ids)
}

func respondEphemeral(s *discordgo.Session, i *discordgo.InteractionCreate, content string) error {
	return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: content,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

func respondPublicMention(s *discordgo.Session, i *discordgo.InteractionCreate, userID, content string) error {
	_, _ = s.ChannelMessageSendComplex(i.ChannelID, &discordgo.MessageSend{
		Content: "<@" + userID + "> " + content,
		AllowedMentions: &discordgo.MessageAllowedMentions{
			Users: []string{userID},
		},
	})
	return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredMessageUpdate,
	})
}

func optionString(opts []*discordgo.ApplicationCommandInteractionDataOption, name string) string {
	for _, o := range opts {
		if o.Name == name {
			return o.StringValue()
		}
	}
	return ""
}

func optionInt(opts []*discordgo.ApplicationCommandInteractionDataOption, name string) int64 {
	for _, o := range opts {
		if o.Name == name {
			return o.IntValue()
		}
	}
	return 0
}

func optionRoleID(opts []*discordgo.ApplicationCommandInteractionDataOption, name string) string {
	for _, o := range opts {
		if o.Name == name && o.Type == discordgo.ApplicationCommandOptionRole {
			return o.Value.(string)
		}
	}
	return ""
}

func optionUserID(opts []*discordgo.ApplicationCommandInteractionDataOption, name string) string {
	for _, o := range opts {
		if o.Name == name && o.Type == discordgo.ApplicationCommandOptionUser {
			return o.Value.(string)
		}
	}
	return ""
}

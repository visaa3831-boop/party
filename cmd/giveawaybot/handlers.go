package main

import (
	"fmt"
	"log"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

const joinPrefix = "gw:join:"

func prefix() string {
	p := strings.TrimSpace(os.Getenv("GIVEAWAY_PREFIX"))
	if p == "" {
		return "!gw"
	}
	return p
}

func canManageGiveaways(s *discordgo.Session, guildID, channelID, userID string) bool {
	if guildID == "" || channelID == "" {
		return false
	}
	perms, err := s.UserChannelPermissions(channelID, userID)
	if err != nil {
		return false
	}
	return perms&discordgo.PermissionAdministrator != 0 || perms&discordgo.PermissionManageGuild != 0
}

func registerCommands(s *discordgo.Session, guildID string) error {
	if s.State == nil || s.State.User == nil {
		return fmt.Errorf("session not ready")
	}
	cmd := &discordgo.ApplicationCommand{
		Name:        "giveaway",
		Description: "Create and manage giveaways",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "start",
				Description: "Post a giveaway with a Join button",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionChannel,
						Name:        "channel",
						Description: "Channel to post the giveaway in",
						Required:    true,
						ChannelTypes: []discordgo.ChannelType{
							discordgo.ChannelTypeGuildText,
							discordgo.ChannelTypeGuildNews,
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionInteger,
						Name:        "minutes",
						Description: "Duration in minutes (1–10080)",
						Required:    true,
						MinValue:    floatPtr(1),
						MaxValue:    10080,
					},
					{
						Type:        discordgo.ApplicationCommandOptionInteger,
						Name:        "winners",
						Description: "How many winners (1–25)",
						Required:    true,
						MinValue:    floatPtr(1),
						MaxValue:    25,
					},
					{
						Type:        discordgo.ApplicationCommandOptionString,
						Name:        "prize",
						Description: "What you are giving away",
						Required:    true,
					},
					{
						Type:        discordgo.ApplicationCommandOptionRole,
						Name:        "require_role",
						Description: "Optional: users must have this role to enter",
						Required:    false,
					},
				},
			},
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "end",
				Description: "End a giveaway early (same rules as automatic end)",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionString,
						Name:        "id",
						Description: "Giveaway ID (shown in the embed footer)",
						Required:    true,
					},
				},
			},
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "reroll",
				Description: "Pick new winner(s) from the same entries",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionString,
						Name:        "id",
						Description: "Giveaway ID",
						Required:    true,
					},
				},
			},
		},
	}
	_, err := s.ApplicationCommandCreate(s.State.User.ID, guildID, cmd)
	return err
}

func floatPtr(f float64) *float64 { return &f }

func followupErr(s *discordgo.Session, i *discordgo.InteractionCreate, msg string) {
	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: msg,
		Flags:   discordgo.MessageFlagsEphemeral,
	})
}

func followupOK(s *discordgo.Session, i *discordgo.InteractionCreate, msg string) {
	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: msg,
		Flags:   discordgo.MessageFlagsEphemeral,
	})
}

func handleGiveawayCommand(s *discordgo.Session, i *discordgo.InteractionCreate, store *giveawayStore) {
	if i.Member == nil || i.Member.User == nil {
		respondEphemeral(s, i, "Use this command in a server.")
		return
	}
	data := i.ApplicationCommandData()
	if data.Name != "giveaway" || len(data.Options) == 0 {
		return
	}
	sub := data.Options[0].Name
	opts := subcommandOptions(&data)

	userID := i.Member.User.ID
	chID := i.ChannelID

	switch sub {
	case "start":
		if !canManageGiveaways(s, i.GuildID, chID, userID) {
			respondEphemeral(s, i, "You need **Manage Server** (or Administrator) to start giveaways.")
			return
		}
		channelID := optionChannelID(opts, "channel")
		minutes := optionInt(opts, "minutes")
		winners := optionInt(opts, "winners")
		prize := strings.TrimSpace(optionString(opts, "prize"))
		reqRole := optionRoleID(opts, "require_role")
		if channelID == "" || minutes < 1 || winners < 1 || prize == "" {
			respondEphemeral(s, i, "Invalid options.")
			return
		}
		if err := respondDefer(s, i); err != nil {
			return
		}
		if err := postGiveaway(s, store, i.GuildID, channelID, userID, minutes, winners, prize, reqRole); err != nil {
			log.Printf("giveaway start: %v", err)
			followupErr(s, i, "Could not start giveaway: "+err.Error())
			return
		}
		followupOK(s, i, "Giveaway posted.")

	case "end":
		if !canManageGiveaways(s, i.GuildID, chID, userID) {
			respondEphemeral(s, i, "You need **Manage Server** (or Administrator).")
			return
		}
		id := strings.TrimSpace(optionString(opts, "id"))
		g := store.get(id)
		if g == nil || g.GuildID != i.GuildID {
			respondEphemeral(s, i, "Unknown giveaway ID for this server.")
			return
		}
		if g.Ended {
			respondEphemeral(s, i, "That giveaway already ended.")
			return
		}
		if err := respondDefer(s, i); err != nil {
			return
		}
		finalizeGiveaway(s, store, id)
		followupOK(s, i, "Giveaway ended.")

	case "reroll":
		if !canManageGiveaways(s, i.GuildID, chID, userID) {
			respondEphemeral(s, i, "You need **Manage Server** (or Administrator).")
			return
		}
		id := strings.TrimSpace(optionString(opts, "id"))
		g := store.get(id)
		if g == nil || g.GuildID != i.GuildID {
			respondEphemeral(s, i, "Unknown giveaway ID for this server.")
			return
		}
		if !g.Ended {
			respondEphemeral(s, i, "End the giveaway before rerolling.")
			return
		}
		if len(g.Entries) == 0 {
			respondEphemeral(s, i, "No entries to reroll.")
			return
		}
		if err := respondDefer(s, i); err != nil {
			return
		}
		winners := pickWinners(g.Entries, g.Winners)
		g.WinnerIDs = winners
		if err := store.put(g); err != nil {
			followupErr(s, i, "Save failed: "+err.Error())
			return
		}
		if err := editGiveawayMessage(s, store, g, true); err != nil {
			log.Printf("reroll edit: %v", err)
			followupErr(s, i, "Reroll failed: "+err.Error())
			return
		}
		followupOK(s, i, fmt.Sprintf("Rerolled. Winners: %s", formatMentions(winners)))

	default:
		respondEphemeral(s, i, "Unknown subcommand.")
	}
}

func respondEphemeral(s *discordgo.Session, i *discordgo.InteractionCreate, msg string) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: msg,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

func respondDefer(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{},
	})
}

func stripCommandPrefix(content, pr string) (rest string, ok bool) {
	content = strings.TrimSpace(content)
	pr = strings.TrimSpace(pr)
	if len(content) < len(pr) {
		return "", false
	}
	if strings.EqualFold(content[:len(pr)], pr) {
		return strings.TrimSpace(content[len(pr):]), true
	}
	return "", false
}

func handlePrefixedCommand(s *discordgo.Session, m *discordgo.MessageCreate, store *giveawayStore) {
	if m.Author == nil || m.Author.Bot || m.GuildID == "" {
		return
	}
	pr := prefix()
	rest, ok := stripCommandPrefix(m.Content, pr)
	if !ok {
		return
	}
	if rest == "" {
		_, _ = s.ChannelMessageSend(m.ChannelID, helpText(pr))
		return
	}

	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return
	}
	sub := strings.ToLower(fields[0])

	switch sub {
	case "help":
		_, _ = s.ChannelMessageSend(m.ChannelID, helpText(pr))
	case "start":
		if !canManageGiveaways(s, m.GuildID, m.ChannelID, m.Author.ID) {
			_, _ = s.ChannelMessageSend(m.ChannelID, "You need **Manage Server** (or Administrator) to start giveaways.")
			return
		}
		if len(fields) < 4 {
			_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Usage: `%s start <minutes> <winners> <prize…>`", pr))
			return
		}
		minutes, err1 := strconv.Atoi(fields[1])
		winners, err2 := strconv.Atoi(fields[2])
		if err1 != nil || err2 != nil || minutes < 1 || winners < 1 {
			_, _ = s.ChannelMessageSend(m.ChannelID, "Minutes and winners must be positive numbers.")
			return
		}
		prize := strings.TrimSpace(strings.Join(fields[3:], " "))
		if prize == "" {
			return
		}
		reqRole := strings.TrimSpace(store.defaultRequire)
		if err := postGiveaway(s, store, m.GuildID, m.ChannelID, m.Author.ID, minutes, winners, prize, reqRole); err != nil {
			log.Printf("giveaway start: %v", err)
			_, _ = s.ChannelMessageSend(m.ChannelID, "Could not start: "+err.Error())
			return
		}
		_, _ = s.ChannelMessageSend(m.ChannelID, "Giveaway posted.")
	case "end":
		if !canManageGiveaways(s, m.GuildID, m.ChannelID, m.Author.ID) {
			_, _ = s.ChannelMessageSend(m.ChannelID, "You need **Manage Server** (or Administrator).")
			return
		}
		if len(fields) < 2 {
			_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Usage: `%s end <giveaway_id>`", pr))
			return
		}
		id := strings.TrimSpace(fields[1])
		g := store.get(id)
		if g == nil || g.GuildID != m.GuildID {
			_, _ = s.ChannelMessageSend(m.ChannelID, "Unknown giveaway ID.")
			return
		}
		if g.Ended {
			_, _ = s.ChannelMessageSend(m.ChannelID, "Already ended.")
			return
		}
		finalizeGiveaway(s, store, id)
		_, _ = s.ChannelMessageSend(m.ChannelID, "Giveaway ended.")
	case "reroll":
		if !canManageGiveaways(s, m.GuildID, m.ChannelID, m.Author.ID) {
			_, _ = s.ChannelMessageSend(m.ChannelID, "You need **Manage Server** (or Administrator).")
			return
		}
		if len(fields) < 2 {
			_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Usage: `%s reroll <giveaway_id>`", pr))
			return
		}
		id := strings.TrimSpace(fields[1])
		g := store.get(id)
		if g == nil || g.GuildID != m.GuildID {
			_, _ = s.ChannelMessageSend(m.ChannelID, "Unknown giveaway ID.")
			return
		}
		if !g.Ended {
			_, _ = s.ChannelMessageSend(m.ChannelID, "End the giveaway before rerolling.")
			return
		}
		if len(g.Entries) == 0 {
			_, _ = s.ChannelMessageSend(m.ChannelID, "No entries.")
			return
		}
		winners := pickWinners(g.Entries, g.Winners)
		g.WinnerIDs = winners
		if err := store.put(g); err != nil {
			_, _ = s.ChannelMessageSend(m.ChannelID, "Save failed: "+err.Error())
			return
		}
		if err := editGiveawayMessage(s, store, g, true); err != nil {
			_, _ = s.ChannelMessageSend(m.ChannelID, "Reroll failed: "+err.Error())
			return
		}
		_, _ = s.ChannelMessageSend(m.ChannelID, "Rerolled: "+formatMentions(winners))
	default:
		_, _ = s.ChannelMessageSend(m.ChannelID, helpText(pr))
	}
}

func helpText(pr string) string {
	return fmt.Sprintf(
		"**Giveaway bot**\n"+
			"• Slash: `/giveaway start`, `/giveaway end`, `/giveaway reroll`\n"+
			"• Prefix: `%s start <minutes> <winners> <prize>` · `%s end <id>` · `%s reroll <id>`\n"+
			"The giveaway ID is in the embed footer.",
		pr, pr, pr,
	)
}

func postGiveaway(s *discordgo.Session, store *giveawayStore, guildID, channelID, hostID string, minutes, winners int, prize, requireRole string) error {
	id, err := store.newID()
	if err != nil {
		return err
	}
	if winners > 25 {
		winners = 25
	}
	g := &Giveaway{
		ID:          id,
		GuildID:     guildID,
		ChannelID:   channelID,
		Prize:       prize,
		HostID:      hostID,
		Winners:     winners,
		EndsAt:      time.Now().Add(time.Duration(minutes) * time.Minute),
		Entries:     nil,
		Ended:       false,
		RequireRole: requireRole,
	}
	msg, err := s.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Embed:      giveawayEmbed(store, g, false),
		Components: giveawayButtons(g, false),
	})
	if err != nil {
		return err
	}
	g.MessageID = msg.ID
	return store.put(g)
}

func giveawayEmbed(store *giveawayStore, g *Giveaway, ended bool) *discordgo.MessageEmbed {
	req := store.effectiveRequireRole(g)
	var desc string
	if ended {
		desc = fmt.Sprintf("**Prize:** %s\n**Winners:** %d\n**Ended** <t:%d:R>\n",
			g.Prize, g.Winners, g.EndsAt.Unix())
		if len(g.WinnerIDs) > 0 {
			desc += "\n**Winner(s):** " + formatMentions(g.WinnerIDs)
		} else {
			desc += "\n**Winner(s):** (no entries)"
		}
	} else {
		desc = fmt.Sprintf("**Prize:** %s\n**Winners:** %d\n**Ends:** <t:%d:R>\n\nClick **Join** to enter.",
			g.Prize, g.Winners, g.EndsAt.Unix())
		if req != "" {
			desc += fmt.Sprintf("\n*Requires role <@&%s>*", req)
		}
	}

	foot := fmt.Sprintf("ID: %s · hosted by %s", g.ID, mention(g.HostID))
	return &discordgo.MessageEmbed{
		Title:       "Giveaway",
		Description: desc,
		Color:       0x5865F2,
		Footer:      &discordgo.MessageEmbedFooter{Text: foot},
	}
}

func giveawayButtons(g *Giveaway, disabled bool) []discordgo.MessageComponent {
	return []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Label:    "Join",
					Style:    discordgo.SuccessButton,
					CustomID: joinPrefix + g.ID,
					Disabled: disabled,
				},
			},
		},
	}
}

func editGiveawayMessage(s *discordgo.Session, store *giveawayStore, g *Giveaway, ended bool) error {
	row := giveawayButtons(g, ended)
	edit := discordgo.NewMessageEdit(g.ChannelID, g.MessageID)
	edit.SetEmbed(giveawayEmbed(store, g, ended))
	edit.Components = &row
	_, err := s.ChannelMessageEditComplex(edit)
	return err
}

func mention(id string) string {
	return "<@" + id + ">"
}

func formatMentions(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	var b strings.Builder
	for i, id := range ids {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(mention(id))
	}
	return b.String()
}

func pickWinners(entries []string, n int) []string {
	if n <= 0 || len(entries) == 0 {
		return nil
	}
	pool := append([]string(nil), entries...)
	rand.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	if n > len(pool) {
		n = len(pool)
	}
	return pool[:n]
}

func memberHasRole(s *discordgo.Session, guildID, userID, roleID string) bool {
	if strings.TrimSpace(roleID) == "" {
		return true
	}
	m, err := s.GuildMember(guildID, userID)
	if err != nil || m == nil {
		return false
	}
	for _, r := range m.Roles {
		if r == roleID {
			return true
		}
	}
	return false
}

func handleJoinButton(s *discordgo.Session, i *discordgo.InteractionCreate, store *giveawayStore) {
	if i.Member == nil || i.Member.User == nil || i.Message == nil {
		return
	}
	data := i.MessageComponentData()
	if !strings.HasPrefix(data.CustomID, joinPrefix) {
		return
	}
	id := strings.TrimPrefix(data.CustomID, joinPrefix)
	g := store.get(id)
	if g == nil {
		respondEphemeral(s, i, "Giveaway not found.")
		return
	}
	if g.Ended || time.Now().After(g.EndsAt) {
		respondEphemeral(s, i, "This giveaway has ended.")
		return
	}
	userID := i.Member.User.ID
	req := store.effectiveRequireRole(g)
	if !memberHasRole(s, g.GuildID, userID, req) {
		respondEphemeral(s, i, "You don't have the required role to enter.")
		return
	}
	for _, e := range g.Entries {
		if e == userID {
			respondEphemeral(s, i, "You're already entered.")
			return
		}
	}
	g.Entries = append(g.Entries, userID)
	if err := store.put(g); err != nil {
		respondEphemeral(s, i, "Could not save entry. Try again.")
		return
	}
	respondEphemeral(s, i, "You're in! Good luck.")
}

func finalizeGiveaway(s *discordgo.Session, store *giveawayStore, id string) {
	store.mu.Lock()
	g := store.Giveaways[id]
	if g == nil || g.Ended {
		store.mu.Unlock()
		return
	}
	g.Ended = true
	g.WinnerIDs = pickWinners(g.Entries, g.Winners)
	store.mu.Unlock()

	if err := editGiveawayMessage(s, store, g, true); err != nil {
		log.Printf("finalize edit message: %v", err)
	}
	if err := store.put(g); err != nil {
		log.Printf("finalize save: %v", err)
	}
}

func runEndWatcher(s *discordgo.Session, store *giveawayStore) {
	t := time.NewTicker(12 * time.Second)
	defer t.Stop()
	for range t.C {
		ids := store.activeIDs()
		now := time.Now()
		for _, id := range ids {
			g := store.get(id)
			if g == nil || g.Ended {
				continue
			}
			if now.Before(g.EndsAt) {
				continue
			}
			finalizeGiveaway(s, store, id)
		}
	}
}

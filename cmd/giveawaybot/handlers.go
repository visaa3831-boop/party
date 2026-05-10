package main

import (
	"fmt"
	"log"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"
)

const joinPrefix = "gw:join:"

func messagePrefixes() []string {
	if p := strings.TrimSpace(os.Getenv("GIVEAWAY_PREFIX")); p != "" {
		return []string{p}
	}
	return []string{"$g", "!gw"}
}

func prefixesHelpLine() string {
	ps := messagePrefixes()
	if len(ps) == 1 {
		return ps[0]
	}
	return strings.Join(ps, " · ")
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
	appID := s.State.User.ID

	giveaway := &discordgo.ApplicationCommand{
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

	short := &discordgo.ApplicationCommand{
		Name:        "g",
		Description: "Quick giveaways (/g create)",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "create",
				Description: "Post a giveaway in this channel",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionChannel,
						Name:        "channel",
						Description: "Channel (defaults to here if omitted)",
						Required:    false,
						ChannelTypes: []discordgo.ChannelType{
							discordgo.ChannelTypeGuildText,
							discordgo.ChannelTypeGuildNews,
						},
					},
					{
						Type:        discordgo.ApplicationCommandOptionString,
						Name:        "prize",
						Description: "What you are giving away",
						Required:    true,
					},
					{
						Type:        discordgo.ApplicationCommandOptionString,
						Name:        "duration",
						Description: `How long until it ends (e.g. 1m, 90m, 2h, 1d) or plain minutes like "120"`,
						Required:    true,
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
				Description: "End a giveaway early",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionString,
						Name:        "id",
						Description: "Giveaway ID (embed footer)",
						Required:    true,
					},
				},
			},
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "reroll",
				Description: "Reroll winners",
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

	_, err := s.ApplicationCommandBulkOverwrite(appID, guildID, []*discordgo.ApplicationCommand{giveaway, short})
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

// Discord requires an acknowledgement within ~3 seconds. Always defer immediately;
// previously we called UserChannelPermissions *before* defer (slow) or ignored `/g`.
func handleGiveawaySlash(s *discordgo.Session, i *discordgo.InteractionCreate, store *giveawayStore) {
	if i.Member == nil || i.Member.User == nil {
		respondEphemeral(s, i, "Use this command in a server.")
		return
	}

	data := i.ApplicationCommandData()
	root := strings.ToLower(strings.TrimSpace(data.Name))
	sub := ""
	if len(data.Options) > 0 {
		sub = strings.ToLower(strings.TrimSpace(data.Options[0].Name))
	}

	valid := root == "giveaway" || root == "g"
	if !valid {
		return
	}
	comboOk := root == "giveaway" && (sub == "start" || sub == "end" || sub == "reroll") ||
		root == "g" && (sub == "create" || sub == "end" || sub == "reroll")
	if !comboOk || sub == "" {
		respondEphemeral(s, i, "Unknown command.")
		return
	}

	if err := respondDefer(s, i); err != nil {
		log.Printf("slash defer failed: %v", err)
		return
	}

	opts := subcommandOptions(&data)

	userID := i.Member.User.ID
	chID := i.ChannelID
	guildID := i.GuildID

	switch {

	case root == "giveaway" && sub == "start":
		if !canManageGiveaways(s, guildID, chID, userID) {
			followupErr(s, i, "You need **Manage Server** (or Administrator) to start giveaways.")
			return
		}
		channelID := optionChannelID(opts, "channel")
		minutes := optionInt(opts, "minutes")
		winners := optionInt(opts, "winners")
		prize := strings.TrimSpace(optionString(opts, "prize"))
		reqRole := optionRoleID(opts, "require_role")
		dur := time.Duration(minutes) * time.Minute
		if channelID == "" || minutes < 1 || winners < 1 || prize == "" {
			followupErr(s, i, "Invalid options.")
			return
		}
		if err := postGiveaway(s, store, guildID, channelID, userID, dur, winners, prize, reqRole); err != nil {
			log.Printf("giveaway start: %v", err)
			followupErr(s, i, "Could not start giveaway: "+err.Error())
			return
		}
		followupOK(s, i, "Giveaway posted.")

	case root == "g" && sub == "create":
		if !canManageGiveaways(s, guildID, chID, userID) {
			followupErr(s, i, "You need **Manage Server** (or Administrator) to start giveaways.")
			return
		}
		channelID := strings.TrimSpace(optionString(opts, "channel"))
		if channelID == "" {
			channelID = chID
		}
		prize := strings.TrimSpace(optionString(opts, "prize"))
		durationStr := strings.TrimSpace(optionString(opts, "duration"))
		winners := optionInt(opts, "winners")
		reqRole := optionRoleID(opts, "require_role")
		if winners < 1 || prize == "" || durationStr == "" {
			followupErr(s, i, "Invalid options.")
			return
		}
		dur, err := parseFlexibleDuration(durationStr)
		if err != nil {
			followupErr(s, i, err.Error())
			return
		}
		if err := postGiveaway(s, store, guildID, channelID, userID, dur, winners, prize, reqRole); err != nil {
			log.Printf("g create: %v", err)
			followupErr(s, i, "Could not start giveaway: "+err.Error())
			return
		}
		followupOK(s, i, "Giveaway posted.")

	case sub == "end":
		if !canManageGiveaways(s, guildID, chID, userID) {
			followupErr(s, i, "You need **Manage Server** (or Administrator).")
			return
		}
		id := strings.TrimSpace(optionString(opts, "id"))
		g := store.get(id)
		if g == nil || g.GuildID != guildID {
			followupErr(s, i, "Unknown giveaway ID for this server.")
			return
		}
		if g.Ended {
			followupErr(s, i, "That giveaway already ended.")
			return
		}
		finalizeGiveaway(s, store, id)
		followupOK(s, i, "Giveaway ended.")

	case sub == "reroll":
		if !canManageGiveaways(s, guildID, chID, userID) {
			followupErr(s, i, "You need **Manage Server** (or Administrator).")
			return
		}
		id := strings.TrimSpace(optionString(opts, "id"))
		g := store.get(id)
		if g == nil || g.GuildID != guildID {
			followupErr(s, i, "Unknown giveaway ID for this server.")
			return
		}
		if !g.Ended {
			followupErr(s, i, "End the giveaway before rerolling.")
			return
		}
		if len(g.Entries) == 0 {
			followupErr(s, i, "No entries to reroll.")
			return
		}
		wlist := pickWinners(g.Entries, g.Winners)
		g.WinnerIDs = wlist
		if err := store.put(g); err != nil {
			followupErr(s, i, "Save failed: "+err.Error())
			return
		}
		if err := editGiveawayMessage(s, store, g, true); err != nil {
			log.Printf("reroll edit: %v", err)
			followupErr(s, i, "Reroll failed: "+err.Error())
			return
		}
		followupOK(s, i, fmt.Sprintf("Rerolled. Winners: %s", formatMentions(wlist)))

	default:
		followupErr(s, i, "Unknown subcommand.")
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
	if len(content) < len(pr) || pr == "" {
		return "", false
	}
	next, _ := utf8.DecodeRuneInString(content[len(pr):])
	isBoundary := len(content) == len(pr) || unicode.IsSpace(next)
	if strings.EqualFold(content[:len(pr)], pr) && isBoundary {
		return strings.TrimSpace(content[len(pr):]), true
	}
	return "", false
}

func handlePrefixedCommand(s *discordgo.Session, m *discordgo.MessageCreate, store *giveawayStore) {
	if m.Author == nil || m.Author.Bot || m.GuildID == "" {
		return
	}
	var rest string
	var matched bool
	for _, cand := range messagePrefixes() {
		if r, ok := stripCommandPrefix(m.Content, cand); ok {
			rest, matched = r, true
			break
		}
	}
	if !matched {
		return
	}

	if rest == "" {
		_, _ = s.ChannelMessageSend(m.ChannelID, helpText(prefixesHelpLine()))
		return
	}

	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return
	}
	sub := strings.ToLower(fields[0])

	switch sub {
	case "help":
		_, _ = s.ChannelMessageSend(m.ChannelID, helpText(prefixesHelpLine()))
	case "start":
		if !canManageGiveaways(s, m.GuildID, m.ChannelID, m.Author.ID) {
			_, _ = s.ChannelMessageSend(m.ChannelID, "You need **Manage Server** (or Administrator) to start giveaways.")
			return
		}
		if len(fields) < 4 {
			ph := prefixesHelpLine()
			_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Usage: `%s start <minutes> <winners> <prize…>` · `%s create <prize…> <duration> <winners>`", ph, ph))
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
		dur := time.Duration(minutes) * time.Minute
		if err := postGiveaway(s, store, m.GuildID, m.ChannelID, m.Author.ID, dur, winners, prize, reqRole); err != nil {
			log.Printf("giveaway start: %v", err)
			_, _ = s.ChannelMessageSend(m.ChannelID, "Could not start: "+err.Error())
			return
		}
		_, _ = s.ChannelMessageSend(m.ChannelID, "Giveaway posted.")
	case "create":
		if !canManageGiveaways(s, m.GuildID, m.ChannelID, m.Author.ID) {
			_, _ = s.ChannelMessageSend(m.ChannelID, "You need **Manage Server** (or Administrator) to start giveaways.")
			return
		}
		ph := prefixesHelpLine()
		if len(fields) < 4 {
			_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Usage: `%s create <prize…> <duration> <winners>` · example: `%s create nitro 1m 1`", ph, ph))
			return
		}
		winStr := fields[len(fields)-1]
		durationStr := fields[len(fields)-2]
		winners, errWin := strconv.Atoi(winStr)
		if errWin != nil || winners < 1 {
			_, _ = s.ChannelMessageSend(m.ChannelID, "Winner count must be a positive integer.")
			return
		}
		prizeParts := fields[1 : len(fields)-2]
		prize := strings.TrimSpace(strings.Join(prizeParts, " "))
		if prize == "" {
			_, _ = s.ChannelMessageSend(m.ChannelID, "Missing prize.")
			return
		}
		duration, errD := parseFlexibleDuration(durationStr)
		if errD != nil {
			_, _ = s.ChannelMessageSend(m.ChannelID, errD.Error())
			return
		}
		reqRole := strings.TrimSpace(store.defaultRequire)
		if err := postGiveaway(s, store, m.GuildID, m.ChannelID, m.Author.ID, duration, winners, prize, reqRole); err != nil {
			log.Printf("giveaway create: %v", err)
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
			ph := prefixesHelpLine()
			_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Usage: `%s end <giveaway_id>`", ph))
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
			ph := prefixesHelpLine()
			_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Usage: `%s reroll <giveaway_id>`", ph))
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
		_, _ = s.ChannelMessageSend(m.ChannelID, helpText(prefixesHelpLine()))
	}
}

func helpText(prefixHints string) string {
	return fmt.Sprintf(
		"**Giveaway bot**\n"+
			"• Slash: `/giveaway start` · `/giveaway end` · `/giveaway reroll`\n"+
			"• Also: **`/g create`** … `/g end`, `/g reroll`\n"+
			"• Prefix (**%s**): `%s create <prize…> <duration> <winners>` (e.g. `nitro 1m 1`)\n"+
			"`%s start <minutes> <winners> <prize>` · `%s end <id>` · `%s reroll <id>`\n"+
			"Giveaway IDs are shown in embed footers.",
		prefixHints, prefixHints, prefixHints, prefixHints, prefixHints,
	)
}

const maxGiveawayDuration = 7 * 24 * time.Hour

func postGiveaway(s *discordgo.Session, store *giveawayStore, guildID, channelID, hostID string, duration time.Duration, winners int, prize, requireRole string) error {
	if duration <= 0 {
		return fmt.Errorf("duration must be positive")
	}
	if duration > maxGiveawayDuration {
		duration = maxGiveawayDuration
	}
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
		EndsAt:      time.Now().Add(duration),
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
	if err := respondDefer(s, i); err != nil {
		log.Printf("join button defer: %v", err)
		return
	}

	id := strings.TrimPrefix(data.CustomID, joinPrefix)
	g := store.get(id)
	if g == nil {
		followupErr(s, i, "Giveaway not found.")
		return
	}
	if g.Ended || time.Now().After(g.EndsAt) {
		followupErr(s, i, "This giveaway has ended.")
		return
	}
	userID := i.Member.User.ID
	req := store.effectiveRequireRole(g)
	if !memberHasRole(s, g.GuildID, userID, req) {
		followupErr(s, i, "You don't have the required role to enter.")
		return
	}
	for _, e := range g.Entries {
		if e == userID {
			followupOK(s, i, "You're already entered.")
			return
		}
	}
	g.Entries = append(g.Entries, userID)
	if err := store.put(g); err != nil {
		followupErr(s, i, "Could not save entry. Try again.")
		return
	}
	followupOK(s, i, "You're in! Good luck.")
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

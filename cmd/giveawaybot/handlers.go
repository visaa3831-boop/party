package main

import (
	"fmt"
	"log"
	"math/rand"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"
)

const joinPrefix = "gw:join:"

var (
	debounceMap = make(map[string]*time.Timer)
	debounceMu  sync.Mutex
)

// debounceUpdate delays embed update by 100ms to batch multiple rapid changes
func debounceUpdate(s *discordgo.Session, store *giveawayStore, g *Giveaway) {
	debounceMu.Lock()
	defer debounceMu.Unlock()

	// Cancel existing timer if any
	if timer, exists := debounceMap[g.ID]; exists {
		timer.Stop()
	}

	// Set new timer
	debounceMap[g.ID] = time.AfterFunc(100*time.Millisecond, func() {
		debounceMu.Lock()
		delete(debounceMap, g.ID)
		debounceMu.Unlock()

		if err := editGiveawayMessage(s, store, g, false); err != nil {
			log.Printf("Failed to update giveaway embed (debounced): %v", err)
		}
	})
}

var (
	rxDiscordUserMention = regexp.MustCompile(`<@!?(\d{17,22})>`)
	rxDiscordRoleMention = regexp.MustCompile(`<@&(\d{17,22})>`)
	rxBareSnowflake      = regexp.MustCompile(`\b\d{17,22}\b`)
)

func uniqPrefixes(list []string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(list))
	for _, x := range list {
		x = strings.TrimSpace(x)
		if x == "" {
			continue
		}
		if _, ok := seen[x]; ok {
			continue
		}
		seen[x] = struct{}{}
		out = append(out, x)
	}
	return out
}

// messagePrefixes: longest-first defaults (`$gw` wins over `$`).
// If GIVEAWAY_PREFIX is set (e.g. on Railway), we prepend it but still register defaults so
// `$`, `$gw`, `$gwvc` commands like `$bleed` work even alongside a display-only custom starter.
func messagePrefixes() []string {
	def := []string{"$gwvc", "$gw", "$"}
	custom := strings.TrimSpace(os.Getenv("GIVEAWAY_PREFIX"))
	if custom == "" {
		return def
	}
	return uniqPrefixes(append([]string{custom}, def...))
}

func ieq(tok, want string) bool {
	return strings.EqualFold(strings.TrimSpace(tok), want)
}

func joinVoiceGateDisableWord(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "none", "off", "-", "0", "disable", "clear":
		return true
	default:
		return false
	}
}

func stripLeadingFold(s, lit string) (rest string, ok bool) {
	if len(s) < len(lit) {
		return "", false
	}
	if !strings.EqualFold(s[:len(lit)], lit) {
		return "", false
	}
	return strings.TrimSpace(s[len(lit):]), true
}

// squashPieces joins argv chunks so users can type IDs without spaces: "1502 867 ..."
func squashPieces(parts []string) string {
	return strings.ReplaceAll(strings.Join(parts, ""), " ", "")
}

func snowflakeDigits(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	if len(s) < 17 {
		return "", false
	}
	return s, true
}

func parseChannelSnowflakeToken(tok string) (id string, disable bool, ok bool) {
	tok = strings.TrimSpace(tok)
	if strings.HasPrefix(tok, "<#") && strings.HasSuffix(tok, ">") && len(tok) > 3 {
		tok = strings.TrimSpace(tok[3 : len(tok)-1])
	}
	tok = strings.ReplaceAll(tok, " ", "")
	tok = strings.TrimPrefix(tok, "#")
	if joinVoiceGateDisableWord(tok) {
		return "", true, true
	}
	id, valid := snowflakeDigits(tok)
	if !valid {
		return "", false, false
	}
	return id, false, true
}

// parseJoinVoicePrefixed parses "$gw vc ...", "$ gw vc ...", "$gwvc<id>", "$ vc ..." payloads (prefix already stripped).
func parseJoinVoicePrefixed(rest string) (matched, clear bool, id string, needsUsage bool) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return false, false, "", false
	}
	fields := strings.Fields(rest)

	if len(fields) >= 3 && ieq(fields[0], "gw") && ieq(fields[1], "vc") {
		tail := squashPieces(fields[2:])
		if tail == "" {
			return true, false, "", true
		}
		cid, dis, tokOK := parseChannelSnowflakeToken(tail)
		if !tokOK {
			return true, false, "", true
		}
		if dis {
			return true, true, "", false
		}
		return true, false, cid, false
	}

	if len(fields) >= 2 && ieq(fields[0], "vc") {
		tail := squashPieces(fields[1:])
		if tail == "" {
			return true, false, "", true
		}
		cid, dis, tokOK := parseChannelSnowflakeToken(tail)
		if !tokOK {
			return true, false, "", true
		}
		if dis {
			return true, true, "", false
		}
		return true, false, cid, false
	}

	if len(fields) == 2 && ieq(fields[0], "gwvc") {
		tail := fields[1]
		cid, dis, tokOK := parseChannelSnowflakeToken(tail)
		if !tokOK {
			return true, false, "", true
		}
		if dis {
			return true, true, "", false
		}
		return true, false, cid, false
	}

	if len(fields) == 1 {
		f := fields[0]
		tail, ok := stripLeadingFold(f, "gwvc")
		if !ok || tail == "" {
			return false, false, "", false
		}
		cid, dis, tokOK := parseChannelSnowflakeToken(tail)
		if !tokOK {
			return true, false, "", true
		}
		if dis {
			return true, true, "", false
		}
		return true, false, cid, false
	}

	return false, false, "", false
}

func optionSlashRestrictRole(opts []*discordgo.ApplicationCommandInteractionDataOption) string {
	if v := optionRoleID(opts, "role"); v != "" {
		return v
	}
	return optionRoleID(opts, "require_role") // Legacy registered command name
}

func prefixesHelpLine() string {
	ps := messagePrefixes()
	if len(ps) == 1 {
		return ps[0]
	}
	return strings.Join(ps, " · ")
}

func examplePrefix() string {
	for _, p := range messagePrefixes() {
		if p == "$" {
			return "$"
		}
	}
	ps := messagePrefixes()
	if len(ps) > 0 {
		return ps[0]
	}
	return "$"
}

func discordUserCanManageGuild(s *discordgo.Session, guildID, channelID, userID string) bool {
	if guildID == "" || channelID == "" {
		return false
	}
	// Owner must bypass even when Gateway cache lacks owner_id — see discordGuildOwner.
	if discordGuildOwner(s, guildID, userID) {
		return true
	}
	perms, err := s.UserChannelPermissions(channelID, userID)
	if err != nil {
		return false
	}
	return perms&discordgo.PermissionAdministrator != 0 || perms&discordgo.PermissionManageGuild != 0
}

// trustedGuildOwnerFromEnv gates $ bleed when Discord's guild.owner_id lookups fail.
// If DISCORD_GUILD_ID/GUILD_ID is set, it must match this guild; if unset, owner ID alone is accepted.
func trustedGuildOwnerFromEnv(serverGuildID, userID string) bool {
	owner := strings.TrimSpace(os.Getenv("GIVEAWAY_TRUST_SERVER_OWNER_ID"))
	wantGuild := strings.TrimSpace(os.Getenv("DISCORD_GUILD_ID"))
	if wantGuild == "" {
		wantGuild = strings.TrimSpace(os.Getenv("GUILD_ID"))
	}
	if owner == "" {
		return false
	}
	if owner != userID {
		return false
	}
	return wantGuild == "" || wantGuild == serverGuildID
}

// discordGuildOwner uses GET /guilds/:id first so OwnerID is always present — partial READY cache often omits it,
// which breaks UserChannelPermissions' owner shortcut and blocked real owners before.
func discordGuildOwner(s *discordgo.Session, guildID, userID string) bool {
	if guildID == "" || userID == "" {
		return false
	}
	if trustedGuildOwnerFromEnv(guildID, userID) {
		return true
	}
	g, err := s.Guild(guildID)
	if err == nil && g != nil && g.OwnerID != "" {
		return g.OwnerID == userID
	}
	if s.State != nil {
		g2, err2 := s.State.Guild(guildID)
		if err2 == nil && g2 != nil && g2.OwnerID != "" && g2.OwnerID == userID {
			log.Printf("discordGuildOwner: REST guild fetch failed guildID=%s err=%v — using Gateway cache owner match", guildID, err)
			return true
		}
	}
	if err != nil {
		log.Printf("discordGuildOwner: Guild(%s): %v", guildID, err)
	}
	return false
}

// Full bot ops: Discord manage (includes owner via discordGuildOwner) OR users granted via $ bleed roster.
func userIsFullBotOperator(s *discordgo.Session, store *giveawayStore, guildID, channelID, userID string) bool {
	if discordUserCanManageGuild(s, guildID, channelID, userID) {
		return true
	}
	return store.IsFullBotAdmin(userID)
}

func userCanManageGiveaways(s *discordgo.Session, store *giveawayStore, guildID, channelID, userID string) bool {
	if userIsFullBotOperator(s, store, guildID, channelID, userID) {
		return true
	}
	return store.IsGiveawayAdmin(userID)
}

func errMsgNeedGiveawayPerms() string {
	return "You need **Manage Server**, **full bot admin** (owner `bleed`), or **giveaway admin** to do that."
}

func discordUserSnowflakesFrom(text string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, sm := range rxDiscordUserMention.FindAllStringSubmatch(text, -1) {
		id := sm[1]
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	stripped := rxDiscordUserMention.ReplaceAllString(text, " ")
	for _, id := range rxBareSnowflake.FindAllString(stripped, -1) {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func discordRoleSnowflakesFrom(text string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, sm := range rxDiscordRoleMention.FindAllStringSubmatch(text, -1) {
		id := sm[1]
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func consumeFirstTokenInsensitive(tail, tok string) (rest string, ok bool) {
	tail = strings.TrimSpace(tail)
	tok = strings.TrimSpace(tok)
	if tail == "" || tok == "" {
		return "", false
	}
	if len(tail) < len(tok) {
		return "", false
	}
	if !strings.EqualFold(tail[:len(tok)], tok) {
		return "", false
	}
	if len(tail) == len(tok) {
		return "", true
	}
	next, _ := utf8.DecodeRuneInString(tail[len(tok):])
	// Slash-style commands often omit a space before a mention (`bleed<@id>`).
	if !unicode.IsSpace(next) && next != '<' {
		return "", false
	}
	return strings.TrimSpace(tail[len(tok):]), true
}

func classifyBleedTail(rest string) (tail string, ok bool) {
	r := strings.TrimSpace(rest)
	if t, ok := consumeFirstTokensJoined(r, "gw", "bleed"); ok {
		return t, true
	}
	return consumeFirstTokenInsensitive(r, "bleed")
}

func classifyGiveawayAdminTail(rest string) (tail string, ok bool) {
	r := strings.TrimSpace(rest)
	if t, ok := consumeFirstTokensJoined(r, "gw", "admin"); ok {
		return t, true
	}
	return consumeFirstTokenInsensitive(r, "admin")
}

func consumeFirstTokensJoined(tail string, first, second string) (after string, ok bool) {
	rest, ok1 := consumeFirstTokenInsensitive(tail, first)
	if !ok1 {
		return "", false
	}
	return consumeFirstTokenInsensitive(rest, second)
}

func handleGiveawayAdminPrefixed(s *discordgo.Session, m *discordgo.MessageCreate, store *giveawayStore, tail string) {
	tail = strings.TrimSpace(tail)
	px := examplePrefix()

	if !userIsFullBotOperator(s, store, m.GuildID, m.ChannelID, m.Author.ID) {
		_, _ = s.ChannelMessageSend(m.ChannelID, "Changing giveaway admins requires **Manage Server**, **full bot admin** (`bleed`), or Discord admin perms.")
		return
	}
	if tail == "" {
		_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf(
			"`%s gw admin @user` (or raw id)\n"+
				"`%s gw admin remove @user` · `%s gw admin list` · `%s gw admin clear`\n"+
				"You can start with **`%s admin …`** instead of **`%s gw admin …`** if you use the **`%s`** starter.",
			px, px, px, px, px, px, px))
		return
	}

	fields := strings.Fields(tail)
	if len(fields) == 0 {
		return
	}
	sw := strings.ToLower(fields[0])

	switch sw {
	case "list":
		ids := store.giveawayAdminListCopy()
		if len(ids) == 0 {
			_, _ = s.ChannelMessageSend(m.ChannelID, "No saved giveaway admins yet.")
			return
		}
		var b strings.Builder
		b.WriteString("Giveaway admins: ")
		for i, id := range ids {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString("<@" + id + ">")
		}
		_, _ = s.ChannelMessageSend(m.ChannelID, b.String())
	case "clear":
		if err := store.clearGiveawayAdmins(); err != nil {
			_, _ = s.ChannelMessageSend(m.ChannelID, "Save failed: "+err.Error())
			return
		}
		_, _ = s.ChannelMessageSend(m.ChannelID, "Cleared giveaway admin list.")

	case "remove", "del":
		remainder, tokOK := consumeFirstTokenInsensitive(tail, fields[0])
		if !tokOK || remainder == "" {
			_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Usage: `%s gw admin remove @user` (or numeric id)", px))
			return
		}
		ids := discordUserSnowflakesFrom(remainder)
		if len(ids) == 0 {
			_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("No user mention or id found after **remove**. Example: `%s gw admin remove @someone`.", px))
			return
		}
		if err := store.removeGiveawayAdmins(ids); err != nil {
			_, _ = s.ChannelMessageSend(m.ChannelID, "Save failed: "+err.Error())
			return
		}
		_, _ = s.ChannelMessageSend(m.ChannelID, "Removed giveaway admin(s).")

	default:
		ids := discordUserSnowflakesFrom(tail)
		if len(ids) == 0 {
			_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Mention someone (`@user`) or paste their Discord user id. Example: `%s gw admin @someone`", px))
			return
		}
		if err := store.addGiveawayAdmins(ids); err != nil {
			_, _ = s.ChannelMessageSend(m.ChannelID, "Save failed: "+err.Error())
			return
		}
		var b strings.Builder
		b.WriteString("Saved giveaway admins: ")
		for i, id := range ids {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString("<@" + id + ">")
		}
		_, _ = s.ChannelMessageSend(m.ChannelID, b.String()+"\n(Restart-safe — stored in the bot data JSON.)")
	}
}

func handleBleedPrefixed(s *discordgo.Session, m *discordgo.MessageCreate, store *giveawayStore, tail string) {
	tail = strings.TrimSpace(tail)
	px := examplePrefix()

	if !discordGuildOwner(s, m.GuildID, m.Author.ID) {
		_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf(
			"**Only the Discord server owner** can use **`bleed`**.\nYour user id: `%s`\nIf this is the owner account, set `GIVEAWAY_TRUST_SERVER_OWNER_ID=%s` in Railway and redeploy.",
			m.Author.ID, m.Author.ID))
		return
	}

	if tail == "" {
		_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf(
			"`%s bleed @user` (or numeric id)\n"+
				"`%s gw bleed …` · `%s bleed remove @user` · `%s bleed list` · `%s bleed clear`\n"+
				"Saved as **full bot admin** — same power as Manage Server on this bot; persists across restarts.",
			px, px, px, px, px))
		return
	}

	fields := strings.Fields(tail)
	if len(fields) == 0 {
		return
	}
	sw := strings.ToLower(fields[0])

	switch sw {
	case "list":
		ids := store.fullBotAdminListCopy()
		if len(ids) == 0 {
			_, _ = s.ChannelMessageSend(m.ChannelID, "No **full bot admins** saved (owner-only list).")
			return
		}
		var b strings.Builder
		b.WriteString("Full bot admins: ")
		for i, id := range ids {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString("<@" + id + ">")
		}
		_, _ = s.ChannelMessageSend(m.ChannelID, b.String())
	case "clear":
		if err := store.clearFullBotAdmins(); err != nil {
			_, _ = s.ChannelMessageSend(m.ChannelID, "Save failed: "+err.Error())
			return
		}
		_, _ = s.ChannelMessageSend(m.ChannelID, "Cleared full bot admins.")
	case "remove", "del":
		remainder, tokOK := consumeFirstTokenInsensitive(tail, fields[0])
		if !tokOK || remainder == "" {
			_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Usage: `%s bleed remove @user` (or id)", px))
			return
		}
		ids := discordUserSnowflakesFrom(remainder)
		if len(ids) == 0 {
			_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("No user found after **remove**. Example: `%s bleed remove @someone`.", px))
			return
		}
		if err := store.removeFullBotAdmins(ids); err != nil {
			_, _ = s.ChannelMessageSend(m.ChannelID, "Save failed: "+err.Error())
			return
		}
		_, _ = s.ChannelMessageSend(m.ChannelID, "Removed full bot admin(s).")
	default:
		ids := discordUserSnowflakesFrom(tail)
		if len(ids) == 0 {
			_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Mention a user (`@who`) or paste their id. Example: `%s bleed @someone`", px))
			return
		}
		if err := store.addFullBotAdmins(ids); err != nil {
			_, _ = s.ChannelMessageSend(m.ChannelID, "Save failed: "+err.Error())
			return
		}
		var b strings.Builder
		b.WriteString("Saved **full bot admin(s)**: ")
		for i, id := range ids {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString("<@" + id + ">")
		}
		_, _ = s.ChannelMessageSend(m.ChannelID, b.String()+"\n(Stored in `giveaway-data.json`; survives bot restarts.)")
	}
}

func handleMassDM(s *discordgo.Session, m *discordgo.MessageCreate, args []string) {
	var filterRoleID string
	msgParts := args
	if len(args) > 1 {
		if roles := discordRoleSnowflakesFrom(args[0]); len(roles) > 0 {
			filterRoleID = roles[0]
			msgParts = args[1:]
		}
	}
	message := strings.TrimSpace(strings.Join(msgParts, " "))
	if message == "" {
		_, _ = s.ChannelMessageSendEmbed(m.ChannelID, &discordgo.MessageEmbed{
			Title:       "❌ Error",
			Description: "Message cannot be empty.",
			Color:       0xFF0000,
		})
		return
	}

	targetDesc := "all members"
	if filterRoleID != "" {
		targetDesc = fmt.Sprintf("members with <@&%s>", filterRoleID)
	}
	_, _ = s.ChannelMessageSendEmbed(m.ChannelID, &discordgo.MessageEmbed{
		Title:       "📨 Mass DM Started",
		Description: fmt.Sprintf("Sending to %s…", targetDesc),
		Color:       0x5865F2,
	})

	var members []*discordgo.Member
	var after string
	for {
		page, err := s.GuildMembers(m.GuildID, after, 1000)
		if err != nil {
			log.Printf("massDM: GuildMembers: %v", err)
			_, _ = s.ChannelMessageSendEmbed(m.ChannelID, &discordgo.MessageEmbed{
				Title:       "❌ Error",
				Description: "Failed to fetch member list: " + err.Error(),
				Color:       0xFF0000,
			})
			return
		}
		members = append(members, page...)
		if len(page) < 1000 {
			break
		}
		after = page[len(page)-1].User.ID
	}

	var targets []*discordgo.Member
	for _, mem := range members {
		if mem.User == nil || mem.User.Bot {
			continue
		}
		if filterRoleID != "" {
			hasRole := false
			for _, r := range mem.Roles {
				if r == filterRoleID {
					hasRole = true
					break
				}
			}
			if !hasRole {
				continue
			}
		}
		targets = append(targets, mem)
	}

	sent, failed := 0, 0
	for _, mem := range targets {
		ch, err := s.UserChannelCreate(mem.User.ID)
		if err != nil {
			failed++
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if _, err := s.ChannelMessageSend(ch.ID, message); err != nil {
			failed++
		} else {
			sent++
		}
		time.Sleep(100 * time.Millisecond)
	}

	_, _ = s.ChannelMessageSendEmbed(m.ChannelID, &discordgo.MessageEmbed{
		Title:       "✅ Mass DM Complete",
		Description: fmt.Sprintf("**%d sent**, **%d failed** (closed DMs / bots)", sent, failed),
		Color:       0x57F287,
	})
}

// guildID empty means global slash commands (all servers); non-empty scopes to one guild (shows up immediately).
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
						Name:        "role",
						Description: "Optional role users must have to enter",
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
					// Required options first (Discord API rule); optional channel/role last.
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
						Type:        discordgo.ApplicationCommandOptionRole,
						Name:        "role",
						Description: "Optional role users must have to enter",
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

func followupPublic(s *discordgo.Session, i *discordgo.InteractionCreate, msg string) {
	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: msg,
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
		if !userCanManageGiveaways(s, store, guildID, chID, userID) {
			followupErr(s, i, errMsgNeedGiveawayPerms())
			return
		}
		channelID := optionChannelID(opts, "channel")
		minutes := optionInt(opts, "minutes")
		winners := optionInt(opts, "winners")
		prize := strings.TrimSpace(optionString(opts, "prize"))
		reqRole := optionSlashRestrictRole(opts)
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
		if !userCanManageGiveaways(s, store, guildID, chID, userID) {
			followupErr(s, i, errMsgNeedGiveawayPerms())
			return
		}
		channelID := strings.TrimSpace(optionString(opts, "channel"))
		if channelID == "" {
			channelID = chID
		}
		prize := strings.TrimSpace(optionString(opts, "prize"))
		durationStr := strings.TrimSpace(optionString(opts, "duration"))
		winners := optionInt(opts, "winners")
		reqRole := optionSlashRestrictRole(opts)
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
		if !userCanManageGiveaways(s, store, guildID, chID, userID) {
			followupErr(s, i, errMsgNeedGiveawayPerms())
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
		if !userCanManageGiveaways(s, store, guildID, chID, userID) {
			followupErr(s, i, errMsgNeedGiveawayPerms())
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
	if pr == "" {
		return "", false
	}
	switch pr {
	case "$gwvc":
		return stripLeadingFold(content, "$gwvc")
	case "$gw":
		return stripLeadingFold(content, "$gw")
	case "$":
		if strings.HasPrefix(content, "$") {
			return strings.TrimSpace(content[len("$"):]), true
		}
		return "", false
	}

	if len(content) < len(pr) {
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

	if tailBl, bleedOK := classifyBleedTail(rest); bleedOK {
		handleBleedPrefixed(s, m, store, tailBl)
		return
	}

	if tailADM, admOK := classifyGiveawayAdminTail(rest); admOK {
		handleGiveawayAdminPrefixed(s, m, store, tailADM)
		return
	}

	if mv, clr, vch, bad := parseJoinVoicePrefixed(rest); mv {
		ph := prefixesHelpLine()
		if !userIsFullBotOperator(s, store, m.GuildID, m.ChannelID, m.Author.ID) {
			_, _ = s.ChannelMessageSend(m.ChannelID, "You need **Manage Server**, **full bot admin** (`bleed`), or **Administrator** to change the VC join gate.")
			return
		}
		if bad {
			_, _ = s.ChannelMessageSend(m.ChannelID,
				fmt.Sprintf("Usage: `%s gw vc` + VC ping (<#…>) **or** raw channel id · `%s gwvc<id>` · `%s gw vc none` to disable.", ph, ph, ph))
			return
		}
		if clr {
			if err := store.setJoinedVoiceGate("", true); err != nil {
				_, _ = s.ChannelMessageSend(m.ChannelID, "Save failed: "+err.Error())
				return
			}
			_, _ = s.ChannelMessageSend(m.ChannelID, "VC join gate disabled — anyone eligible by role can join giveaways.")
			return
		}
		if err := store.setJoinedVoiceGate(vch, false); err != nil {
			_, _ = s.ChannelMessageSend(m.ChannelID, "Save failed: "+err.Error())
			return
		}
		_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("VC join gate set to <#%s>.", vch))
		return
	}

	if rest == "" {
		_, _ = s.ChannelMessageSend(m.ChannelID, helpText())
		return
	}

	fields := strings.Fields(rest)
	// Allow `$ gw reroll …` / `$ gw end …` when using the `$` starter (optional `gw` word after prefix).
	if len(fields) >= 1 && ieq(fields[0], "gw") {
		fields = fields[1:]
	}
	if len(fields) == 0 {
		// e.g. only `$ gw` after prefix — avoid dumping the whole guide here.
		_, _ = s.ChannelMessageSend(m.ChannelID, unknownGiveawayPrefixMsg())
		return
	}
	sub := strings.ToLower(fields[0])

	switch sub {
	case "help":
		_, _ = s.ChannelMessageSend(m.ChannelID, helpText())
	case "start":
		if !userCanManageGiveaways(s, store, m.GuildID, m.ChannelID, m.Author.ID) {
			_, _ = s.ChannelMessageSend(m.ChannelID, errMsgNeedGiveawayPerms())
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
	case "create":
		if !userCanManageGiveaways(s, store, m.GuildID, m.ChannelID, m.Author.ID) {
			_, _ = s.ChannelMessageSend(m.ChannelID, errMsgNeedGiveawayPerms())
			return
		}
		ph := prefixesHelpLine()
		if len(fields) < 4 {
			_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Usage: `%s create <prize…> <duration> <winners> [host_user_id]` · example: `%s create nitro 1m 1 @user`", ph, ph))
			return
		}
		hostID := m.Author.ID
		last := fields[len(fields)-1]
		valueEnd := len(fields)
		if ids := discordUserSnowflakesFrom(last); len(ids) > 0 {
			hostID = ids[0]
			valueEnd--
		} else if isDiscordUserSnowflake(last) {
			hostID = last
			valueEnd--
		}
		if valueEnd < 4 {
			_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Usage: `%s create <prize…> <duration> <winners> [host_user_id]` · example: `%s create nitro 1m 1 @user`", ph, ph))
			return
		}
		winStr := fields[valueEnd-1]
		durationStr := fields[valueEnd-2]
		winners, errWin := strconv.Atoi(winStr)
		if errWin != nil || winners < 1 {
			_, _ = s.ChannelMessageSend(m.ChannelID, "Winner count must be a positive integer.")
			return
		}
		prizeParts := fields[1 : valueEnd-2]
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
		if err := postGiveaway(s, store, m.GuildID, m.ChannelID, hostID, duration, winners, prize, reqRole); err != nil {
			log.Printf("giveaway create: %v", err)
			_, _ = s.ChannelMessageSend(m.ChannelID, "Could not start: "+err.Error())
			return
		}
	case "end":
		if !userCanManageGiveaways(s, store, m.GuildID, m.ChannelID, m.Author.ID) {
			_, _ = s.ChannelMessageSend(m.ChannelID, errMsgNeedGiveawayPerms())
			return
		}
		if len(fields) < 2 {
			ph := prefixesHelpLine()
			_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Usage: `%s end <id>`", ph))
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
		if !userCanManageGiveaways(s, store, m.GuildID, m.ChannelID, m.Author.ID) {
			_, _ = s.ChannelMessageSend(m.ChannelID, errMsgNeedGiveawayPerms())
			return
		}
		if len(fields) < 2 {
			ph := prefixesHelpLine()
			_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Usage: `%s reroll <id>` or `%s gw reroll <id>`", ph, ph))
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
	case "bleed":
		if tailMore, ok := consumeFirstTokenInsensitive(rest, fields[0]); ok {
			handleBleedPrefixed(s, m, store, tailMore)
			return
		}
		_, _ = s.ChannelMessageSend(m.ChannelID, unknownGiveawayPrefixMsg())
	case "admin":
		if tailMore, ok := consumeFirstTokenInsensitive(rest, fields[0]); ok {
			handleGiveawayAdminPrefixed(s, m, store, tailMore)
			return
		}
		_, _ = s.ChannelMessageSend(m.ChannelID, unknownGiveawayPrefixMsg())
	case "l":
		if !store.IsFullBotAdmin(m.Author.ID) {
			_, _ = s.ChannelMessageSend(m.ChannelID, "Only full bot admins can lock roles.")
			return
		}
		if len(fields) < 2 {
			_, _ = s.ChannelMessageSend(m.ChannelID, "Usage: `$l @role` to lock, `$l @role unlock` to unlock.")
			return
		}
		roleMentions := discordRoleSnowflakesFrom(fields[1])
		if len(roleMentions) == 0 {
			_, _ = s.ChannelMessageSend(m.ChannelID, "Invalid role mention.")
			return
		}
		roleID := roleMentions[0]
		action := "lock"
		if len(fields) >= 3 && strings.ToLower(fields[2]) == "unlock" {
			action = "unlock"
		}
		if action == "lock" {
			if err := store.lockRole(roleID, m.Author.ID); err != nil {
				_, _ = s.ChannelMessageSend(m.ChannelID, "Failed to lock role: "+err.Error())
				return
			}
			_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Locked role <@&%s>. Only full bot admins can give this role.", roleID))
		} else {
			if err := store.unlockRole(roleID); err != nil {
				_, _ = s.ChannelMessageSend(m.ChannelID, "Failed to unlock role: "+err.Error())
				return
			}
			_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Unlocked role <@&%s>.", roleID))
		}
	case "w":
		if !store.IsFullBotAdmin(m.Author.ID) {
			_, _ = s.ChannelMessageSend(m.ChannelID, "Only full bot admins can whitelist users.")
			return
		}
		if len(fields) < 2 {
			_, _ = s.ChannelMessageSend(m.ChannelID, "Usage: `$w @user` to whitelist, `$w @user unwhitelist` to remove.")
			return
		}
		userMentions := discordUserSnowflakesFrom(fields[1])
		if len(userMentions) == 0 {
			_, _ = s.ChannelMessageSend(m.ChannelID, "Invalid user mention.")
			return
		}
		userID := userMentions[0]
		action := "whitelist"
		if len(fields) >= 3 && strings.ToLower(fields[2]) == "unwhitelist" {
			action = "unwhitelist"
		}
		if action == "whitelist" {
			if err := store.whitelistUser(userID, m.Author.ID); err != nil {
				_, _ = s.ChannelMessageSend(m.ChannelID, "Failed to whitelist user: "+err.Error())
				return
			}
			_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Whitelisted <@%s>. They can now assign locked roles.", userID))
		} else {
			if err := store.unwhitelistUser(userID); err != nil {
				_, _ = s.ChannelMessageSend(m.ChannelID, "Failed to unwhitelist user: "+err.Error())
				return
			}
			_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Unwhitelisted <@%s>.", userID))
		}
	case "shop":
		shopEmbed := &discordgo.MessageEmbed{
			Title:       "🛒 Shop",
			Description: "DM the server owner to purchase items.\n\nPrices vary by item.",
			Color:       0x5865F2,
		}
		_, _ = s.ChannelMessageSendEmbed(m.ChannelID, shopEmbed)
	case "dm":
		if !userIsFullBotOperator(s, store, m.GuildID, m.ChannelID, m.Author.ID) {
			_, _ = s.ChannelMessageSend(m.ChannelID, "You need **Manage Server**, **full bot admin** (`bleed`), or **Administrator** to use mass DM.")
			return
		}
		if len(fields) < 2 {
			ph := prefixesHelpLine()
			_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf(
				"`%s dm <message>` — DM all server members\n"+
					"`%s dm @role <message>` — DM all members with a specific role", ph, ph))
			return
		}
		go handleMassDM(s, m, fields[1:])
	case "entries":
		if len(fields) < 2 {
			_, _ = s.ChannelMessageSend(m.ChannelID, "Usage: `$entries <giveaway_id>`")
			return
		}
		giveawayID := fields[1]
		g := store.get(giveawayID)
		if g == nil {
			_, _ = s.ChannelMessageSend(m.ChannelID, "Giveaway not found.")
			return
		}
		var entriesList string
		if len(g.Entries) == 0 {
			entriesList = "No entries yet."
		} else {
			for _, entry := range g.Entries {
				entriesList += fmt.Sprintf("<@%s>\n", entry)
			}
		}
		entriesEmbed := &discordgo.MessageEmbed{
			Title:       fmt.Sprintf("Giveaway Entries - %s", g.ID),
			Description: fmt.Sprintf("**Prize:** %s\n**Participants:** %d", g.Prize, len(g.Entries)),
			Color:       0x5865F2,
			Fields: []*discordgo.MessageEmbedField{
				{
					Name:  "Entries",
					Value: entriesList,
				},
			},
		}
		_, _ = s.ChannelMessageSendEmbed(m.ChannelID, entriesEmbed)
	default:
		_, _ = s.ChannelMessageSend(m.ChannelID, unknownGiveawayPrefixMsg())
	}
}

func unknownGiveawayPrefixMsg() string {
	px := examplePrefix()
	return fmt.Sprintf("Unknown giveaway-bot command — use **`%s help`** or send **`%s`** alone for the full guide.", px, px)
}

func helpText() string {
	px := examplePrefix()
	return "**Giveaway bot**\n" +
		"• Slash: `/giveaway start` … optional role filter **`role`**\n" +
		"• Slash: `/g create` · `/giveaway end|reroll` · `/g end|reroll`\n" +
		"• Text prefixes (**" + prefixesHelpLine() + "**), e.g. with **`" + px + "`**:\n" +
		"`" + px + " gw vc` + VC ping `<#…>` **or** raw id · `" + px + " gwvc<id>` (digits can be spaced) · `" + px + " gw vc none` · `" + px + " create nitro 1m 1 @user` · `" + px + " gw reroll <id>`\n" +
		"• **Server owner only:** `" + px + " bleed @user` — full bot admin (VC + giveaway admins + giveaways). `remove` / `list` / `clear`\n" +
		"• Full bot ops / admins: `" + px + " gw admin …` — **giveaway-only** admins (smaller powers)\n" +
		"IDs are on embed footers."
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
		Embed: giveawayEmbed(store, g, false),
	})
	if err != nil {
		return err
	}
	g.MessageID = msg.ID
	if err := store.put(g); err != nil {
		return err
	}
	// Add 🎉 reaction for joining
	if err := s.MessageReactionAdd(channelID, msg.ID, "🎉"); err != nil {
		log.Printf("Failed to add 🎉 reaction to giveaway %s: %v", g.ID, err)
	}
	return nil
}

func giveawayEmbed(store *giveawayStore, g *Giveaway, ended bool) *discordgo.MessageEmbed {
	req := store.effectiveRequireRole(g)
	var desc string
	if ended {
		desc = fmt.Sprintf("**Prize:** %s\n**Hosted By:** %s\n**Participants:** %d\n**Winners:** %d\n**Ended:** <t:%d:R>\n",
			g.Prize, mention(g.HostID), len(g.Entries), g.Winners, g.EndsAt.Unix())
		if len(g.WinnerIDs) > 0 {
			desc += "\n**Winner(s):** " + formatMentions(g.WinnerIDs)
		} else {
			desc += "\n**Winner(s):** (no entries)"
		}
	} else {
		desc = fmt.Sprintf("**Prize:** %s\n**Hosted By:** %s\n**Participants:** %d\n**Winners:** %d\n**Ends:** <t:%d:R>\n\nReact with 🎉 to enter.",
			g.Prize, mention(g.HostID), len(g.Entries), g.Winners, g.EndsAt.Unix())
		if req != "" {
			desc += fmt.Sprintf("\n*Requires role <@&%s>*", req)
		}
		if rid := store.joinedVoiceGateChanID(); rid != "" {
			desc += fmt.Sprintf("\n*You must be in voice <#%s> to enter.*", rid)
		}
	}

	foot := fmt.Sprintf("ID: %s", g.ID)
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

// Requires the user's current Gateway voice/stage channel to be in ANY voice channel when requiredChanID is set.
func memberInRequiredVoiceChannel(s *discordgo.Session, guildID, userID, requiredChanID string) bool {
	requiredChanID = strings.TrimSpace(requiredChanID)
	if requiredChanID == "" {
		return true
	}
	vs, err := s.State.VoiceState(guildID, userID)
	if err != nil {
		return false
	}
	// Accept any voice channel when requirement is set
	return vs.ChannelID != ""
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
	if rid := store.joinedVoiceGateChanID(); rid != "" &&
		!memberInRequiredVoiceChannel(s, g.GuildID, userID, rid) {
		followupPublic(s, i, fmt.Sprintf(
			"<@%s> you are not following the requirements. join a vc <#%s>",
			userID, rid))
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
	// Entry successful - no confirmation message
}

// HandleReactionAdd handles 🎉 reaction to join giveaway
func HandleReactionAdd(s *discordgo.Session, r *discordgo.MessageReactionAdd, store *giveawayStore) {
	if r.Emoji.Name != "🎉" {
		return
	}
	g := store.getByMessageID(r.MessageID)
	if g == nil || g.Ended {
		return
	}
	userID := r.UserID
	// Check if already entered
	for _, e := range g.Entries {
		if e == userID {
			return
		}
	}
	// Check requirements
	if req := store.effectiveRequireRole(g); req != "" {
		if !memberHasRole(s, g.GuildID, userID, req) {
			return
		}
	}
	if rid := store.joinedVoiceGateChanID(); rid != "" && !memberInRequiredVoiceChannel(s, g.GuildID, userID, rid) {
		_, _ = s.ChannelMessageSend(r.ChannelID, fmt.Sprintf(
			"<@%s> you are not following the requirements. join a vc <#%s>",
			userID, rid))
		return
	}
	// Add entry
	g.Entries = append(g.Entries, userID)
	if err := store.put(g); err != nil {
		log.Printf("Failed to save entry for user %s: %v", userID, err)
		return
	}
	// Debounce embed update for performance
	debounceUpdate(s, store, g)
}

// HandleReactionRemove handles 🎉 reaction removal to leave giveaway
func HandleReactionRemove(s *discordgo.Session, r *discordgo.MessageReactionRemove, store *giveawayStore) {
	if r.Emoji.Name != "🎉" {
		return
	}
	g := store.getByMessageID(r.MessageID)
	if g == nil || g.Ended {
		return
	}
	userID := r.UserID
	// Remove entry
	for i, e := range g.Entries {
		if e == userID {
			g.Entries = append(g.Entries[:i], g.Entries[i+1:]...)
			if err := store.put(g); err != nil {
				log.Printf("Failed to remove entry for user %s: %v", userID, err)
				return
			}
			// Debounce embed update for performance
			debounceUpdate(s, store, g)
			break
		}
	}
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

	// Send congratulatory message
	log.Printf("Giveaway %s ended. Entries: %d, Winners: %d", g.ID, len(g.Entries), len(g.WinnerIDs))
	if len(g.WinnerIDs) > 0 {
		for _, winnerID := range g.WinnerIDs {
			congratsMsg := fmt.Sprintf("🎉 Congrats! <@%s> you won the **%s** giveaway! DM <@%s> to claim.", winnerID, g.Prize, g.HostID)
			if _, err := s.ChannelMessageSend(g.ChannelID, congratsMsg); err != nil {
				log.Printf("Failed to send congratulatory message: %v", err)
			} else {
				log.Printf("Sent congratulatory message for winner %s", winnerID)
			}
		}
	} else {
		// No entries
		noWinnerMsg := fmt.Sprintf("Giveaway ended with no entries. No winner for **%s**.", g.Prize)
		if _, err := s.ChannelMessageSend(g.ChannelID, noWinnerMsg); err != nil {
			log.Printf("Failed to send no winner message: %v", err)
		} else {
			log.Printf("Sent no winner message for giveaway %s", g.ID)
		}
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

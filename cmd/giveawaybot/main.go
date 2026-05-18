package main

import (
	"log"
	"os"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()

	token := strings.TrimSpace(os.Getenv("DISCORD_BOT_TOKEN"))
	guildID := strings.TrimSpace(os.Getenv("DISCORD_GUILD_ID"))
	vcRoleID := strings.TrimSpace(os.Getenv("VOICE_ROLE_ID"))
	vcJoinRoleID := strings.TrimSpace(os.Getenv("VC_ROLE_ID"))
	requireVoiceChan := giveawayRequireVoiceChannelID()

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

	dg.Identify.Intents =
		discordgo.IntentsGuilds |
			discordgo.IntentsGuildMessages |
			discordgo.IntentsMessageContent |
			discordgo.IntentsGuildVoiceStates |
			discordgo.IntentsGuildMembers

	store := newGiveawayStore(vcRoleID, requireVoiceChan)
	if requireVoiceChan != "" {
		log.Printf("giveaway Join gate: must be in voice channel %s", requireVoiceChan)
	}
	if trusted := strings.TrimSpace(os.Getenv("GIVEAWAY_TRUST_SERVER_OWNER_ID")); trusted != "" {
		log.Printf("trusted owner override enabled for user id %s", trusted)
	}
	if err := store.load(); err != nil {
		log.Printf("giveaway store load: %v", err)
	}

	dg.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		log.Printf("logged in as %s", r.User.String())

		registerTo := guildID
		mode := "global"
		if registerTo != "" {
			mode = "guild " + registerTo + " (instant)"
		} else {
			log.Println("DISCORD_GUILD_ID / GUILD_ID unset: registering slash commands globally (can take up to ~1 hour to appear in Discord; set DISCORD_GUILD_ID for instant guild commands)")
		}
		if err := registerCommands(s, registerTo); err != nil {
			log.Printf("register slash commands (%s): %v", mode, err)
			return
		}
		log.Printf("slash commands registered (%s)", mode)
	})

	dg.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		handlePrefixedCommand(s, m, store)
	})

	dg.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		if i.Type == discordgo.InteractionMessageComponent {
			handleJoinButton(s, i, store)
			return
		}
		if i.Type != discordgo.InteractionApplicationCommand {
			return
		}
		handleGiveawaySlash(s, i, store)
	})

	dg.AddHandler(func(s *discordgo.Session, m *discordgo.GuildMemberUpdate) {
		if m.Member == nil || m.GuildID == "" {
			return
		}
		// Check if any locked roles are on the member
		for _, roleID := range m.Member.Roles {
			if store.isRoleLocked(roleID) && !store.IsFullBotAdmin(m.Member.User.ID) && !store.isUserWhitelisted(m.Member.User.ID) {
				// Role is locked and user is not a full bot admin and not whitelisted - remove it
				log.Printf("Removing locked role %s from %s (user is not a full bot admin or whitelisted)", roleID, m.Member.User.ID)
				if err := s.GuildMemberRoleRemove(m.GuildID, m.Member.User.ID, roleID); err != nil {
					log.Printf("Failed to remove locked role %s from %s: %v", roleID, m.Member.User.ID, err)
				}
			}
		}
	})

	dg.AddHandler(func(s *discordgo.Session, r *discordgo.MessageReactionAdd) {
		HandleReactionAdd(s, r, store)
	})

	dg.AddHandler(func(s *discordgo.Session, r *discordgo.MessageReactionRemove) {
		HandleReactionRemove(s, r, store)
	})

	dg.AddHandler(func(s *discordgo.Session, m *discordgo.VoiceStateUpdate) {
		log.Printf("VoiceStateUpdate: UserID=%s, GuildID=%s, ChannelID=%s, vcJoinRoleID=%s", m.UserID, m.GuildID, m.ChannelID, vcJoinRoleID)
		if m.GuildID == "" || m.ChannelID == "" || vcJoinRoleID == "" {
			log.Printf("VoiceStateUpdate: Skipping - missing required fields")
			return
		}
		// User joined a voice channel - assign VC role
		beforeChannel := ""
		if m.BeforeUpdate != nil {
			beforeChannel = m.BeforeUpdate.ChannelID
		}
		log.Printf("VoiceStateUpdate: BeforeChannel=%s, CurrentChannel=%s", beforeChannel, m.ChannelID)
		if m.BeforeUpdate == nil || m.BeforeUpdate.ChannelID == "" {
			log.Printf("User %s joined voice channel %s, assigning VC role", m.UserID, m.ChannelID)
			if err := s.GuildMemberRoleAdd(m.GuildID, m.UserID, vcJoinRoleID); err != nil {
				log.Printf("Failed to assign VC role to %s: %v", m.UserID, err)
			} else {
				log.Printf("Successfully assigned VC role to %s", m.UserID)
			}
		}
	})

	if err := dg.Open(); err != nil {
		log.Fatalf("open discord session: %v", err)
	}
	defer dg.Close()

	go runEndWatcher(dg, store)

	// Startup scan for VC role assignment
	if vcJoinRoleID != "" {
		go func() {
			time.Sleep(2 * time.Second) // Wait for guilds to be ready
			if guildID == "" {
				log.Println("No guild ID set, skipping VC role startup scan")
				return
			}
			log.Println("Starting VC role startup scan...")
			guild, err := dg.Guild(guildID)
			if err != nil {
				log.Printf("Failed to fetch guild for VC scan: %v", err)
				return
			}
			// Get all members in voice channels
			for _, vs := range guild.VoiceStates {
				if vs.ChannelID != "" && vs.UserID != "" {
					log.Printf("Assigning VC role to user %s (already in voice channel)", vs.UserID)
					if err := dg.GuildMemberRoleAdd(guildID, vs.UserID, vcJoinRoleID); err != nil {
						log.Printf("Failed to assign VC role to %s during startup scan: %v", vs.UserID, err)
					}
				}
			}
			log.Println("VC role startup scan complete")
		}()
	}

	log.Println("bot running")

	select {}
}

const defaultGiveawayVoiceChannelID = "1502867397605068810"

// giveawayRequireVoiceChannelID: empty env uses defaultGiveawayVoiceChannelID; set GIVEAWAY_REQUIRE_VOICE_CHANNEL_ID
// to another ID, or to none|off|0|- to turn the check off.
func giveawayRequireVoiceChannelID() string {
	raw := strings.TrimSpace(os.Getenv("GIVEAWAY_REQUIRE_VOICE_CHANNEL_ID"))
	switch strings.ToLower(raw) {
	case "none", "off", "-", "0":
		return ""
	default:
		if raw != "" {
			return raw
		}
		return defaultGiveawayVoiceChannelID
	}
}

// --- option helpers (slash) ---

func subcommandOptions(data *discordgo.ApplicationCommandInteractionData) []*discordgo.ApplicationCommandInteractionDataOption {
	if data == nil || len(data.Options) == 0 {
		return nil
	}
	return data.Options[0].Options
}

func optionString(opts []*discordgo.ApplicationCommandInteractionDataOption, name string) string {
	for _, o := range opts {
		if o.Name == name {
			if v, ok := o.Value.(string); ok {
				return v
			}
		}
	}
	return ""
}

func optionInt(opts []*discordgo.ApplicationCommandInteractionDataOption, name string) int {
	for _, o := range opts {
		if o.Name == name {
			switch v := o.Value.(type) {
			case float64:
				return int(v)
			case int:
				return v
			}
		}
	}
	return 0
}

func optionRoleID(opts []*discordgo.ApplicationCommandInteractionDataOption, name string) string {
	for _, o := range opts {
		if o.Name == name {
			if v, ok := o.Value.(string); ok {
				return v
			}
		}
	}
	return ""
}

func optionChannelID(opts []*discordgo.ApplicationCommandInteractionDataOption, name string) string {
	if name == "" {
		name = "channel"
	}
	return optionString(opts, name)
}

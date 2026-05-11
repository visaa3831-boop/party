package main

import (
	"log"
	"os"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()

	token := strings.TrimSpace(os.Getenv("DISCORD_BOT_TOKEN"))
	guildID := strings.TrimSpace(os.Getenv("DISCORD_GUILD_ID"))
	vcRoleID := strings.TrimSpace(os.Getenv("VOICE_ROLE_ID"))
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
			discordgo.IntentsGuildVoiceStates

	store := newGiveawayStore(vcRoleID, requireVoiceChan)
	if requireVoiceChan != "" {
		log.Printf("giveaway Join gate: must be in voice channel %s", requireVoiceChan)
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

	if err := dg.Open(); err != nil {
		log.Fatalf("open discord session: %v", err)
	}
	defer dg.Close()

	go runEndWatcher(dg, store)

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

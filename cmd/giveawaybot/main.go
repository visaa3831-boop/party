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

	store := newGiveawayStore(vcRoleID)
	if err := store.load(); err != nil {
		log.Printf("giveaway store load: %v", err)
	}

	dg.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		log.Printf("logged in as %s", r.User.String())
		if guildID != "" {
			if err := registerCommands(s, guildID); err != nil {
				log.Printf("register commands: %v", err)
			}
		} else {
			log.Println("DISCORD_GUILD_ID unset: slash commands not registered (set it for guild commands)")
		}
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

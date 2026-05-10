package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"

	"partydiscord/internal/backup"
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

	// FIX: proper intents
	dg.Identify.Intents =
		discordgo.IntentsGuilds |
			discordgo.IntentsGuildMessages |
			discordgo.IntentsMessageContent |
			discordgo.IntentsGuildVoiceStates

	store := newGiveawayStore()
	_ = store.load()

	dg.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		log.Printf("logged in as %s", r.User.String())
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
		handleGiveawayCommand(s, i, store)
	})

	// FIX: required to actually connect
	if err := dg.Open(); err != nil {
		log.Fatalf("open discord session: %v", err)
	}
	defer dg.Close()

	// register slash commands if guild provided
	if guildID != "" {
		_ = registerCommands(dg, guildID)
	}

	go runEndWatcher(dg, store)

	log.Println("bot running")

	select {}
}

/* =========================
   FIX: SAFE OPTION HELPERS
   ========================= */

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

func optionUserID(opts []*discordgo.ApplicationCommandInteractionDataOption, name string) string {
	for _, o := range opts {
		if o.Name == name {
			if v, ok := o.Value.(string); ok {
				return v
			}
		}
	}
	return ""
}

/* =========================
   EVERYTHING ELSE UNCHANGED
   ========================= */

// (rest of your file stays EXACTLY the same below this line)

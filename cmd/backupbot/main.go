// Command backupbot exports a Discord guild snapshot (roles, channels with permission overwrites,
// sidebar order, optional messages) and downloads the server icon to disk. No web UI required.
//
// Environment:
//   DISCORD_BOT_TOKEN — bot token (same app as your Discord application).
//   DISCORD_GUILD_ID  — server (guild) snowflake ID.
//   BACKUP_OUT_DIR    — optional output directory (default: ./backups/<guildId>-<timestamp>).
//   BACKUP_SKIP_MEMBERS / BACKUP_SKIP_MESSAGES — same as the web server (see .env.example).
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"

	"partydiscord/internal/backup"
)

func main() {
	_ = godotenv.Load()

	token := strings.TrimSpace(os.Getenv("DISCORD_BOT_TOKEN"))
	guildID := strings.TrimSpace(os.Getenv("DISCORD_GUILD_ID"))
	if guildID == "" {
		guildID = strings.TrimSpace(os.Getenv("GUILD_ID"))
	}
	if token == "" || guildID == "" {
		log.Fatal("set DISCORD_BOT_TOKEN and DISCORD_GUILD_ID (or GUILD_ID)")
	}

	outDir := strings.TrimSpace(os.Getenv("BACKUP_OUT_DIR"))
	if outDir == "" {
		stamp := time.Now().UTC().Format("20060102T150405")
		outDir = filepath.Join("backups", fmt.Sprintf("%s-%s", guildID, stamp))
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}

	opts := backup.Options{
		SkipMembers:  strings.EqualFold(os.Getenv("BACKUP_SKIP_MEMBERS"), "true"),
		SkipMessages: strings.EqualFold(os.Getenv("BACKUP_SKIP_MESSAGES"), "true"),
		Logf:         log.Printf,
	}

	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		log.Fatalf("discord session: %v", err)
	}

	payload, err := backup.Build(dg, guildID, opts)
	if err != nil {
		log.Fatalf("backup: %v", err)
	}

	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		log.Fatalf("json: %v", err)
	}
	jsonPath := filepath.Join(outDir, "guild-backup.json")
	if err := os.WriteFile(jsonPath, data, 0o644); err != nil {
		log.Fatalf("write json: %v", err)
	}

	iconPath, err := backup.SaveGuildIcon(http.DefaultClient, payload.Guild, outDir)
	if err != nil {
		log.Printf("guild icon: %v", err)
	} else if iconPath != "" {
		log.Printf("saved icon: %s", iconPath)
	}

	log.Printf("wrote %s (%d bytes)", jsonPath, len(data))
}

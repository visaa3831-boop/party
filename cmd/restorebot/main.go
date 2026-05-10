// Command restorebot applies a guild-backup.json produced by backupbot (or the web export) onto another
// Discord server you control. It recreates roles (except managed/integration roles), categories/channels in
// sidebar order, and permission overwrites.
//
// Safety: requires RESTORE_CONFIRM=yes. Bot needs Manage Roles + Manage Channels (Administrator is simplest).
//
// Environment:
//
//	DISCORD_BOT_TOKEN      — same bot as backups (must be invited to the TARGET server).
//	DISCORD_TARGET_GUILD_ID — server to create roles/channels in.
//	RESTORE_BACKUP_JSON    — path to guild-backup.json
//	RESTORE_CONFIRM        — must be "yes" to run
//	RESTORE_DRY_RUN        — optional "true" to log actions without calling the API
//	RESTORE_DELAY_MS       — optional delay between API calls (e.g. 300)
package main

import (
	"encoding/json"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"

	"partydiscord/internal/backup"
	"partydiscord/internal/restore"
)

func main() {
	_ = godotenv.Load()

	if !strings.EqualFold(os.Getenv("RESTORE_CONFIRM"), "yes") {
		log.Fatal("set RESTORE_CONFIRM=yes to apply a backup to the target guild (safety check)")
	}

	token := strings.TrimSpace(os.Getenv("DISCORD_BOT_TOKEN"))
	targetGuild := strings.TrimSpace(os.Getenv("DISCORD_TARGET_GUILD_ID"))
	if targetGuild == "" {
		targetGuild = strings.TrimSpace(os.Getenv("GUILD_ID"))
	}
	backupPath := strings.TrimSpace(os.Getenv("RESTORE_BACKUP_JSON"))
	if token == "" || targetGuild == "" || backupPath == "" {
		log.Fatal("need DISCORD_BOT_TOKEN, DISCORD_TARGET_GUILD_ID, RESTORE_BACKUP_JSON")
	}

	raw, err := os.ReadFile(backupPath)
	if err != nil {
		log.Fatalf("read backup: %v", err)
	}
	var payload backup.FullBackup
	if err := json.Unmarshal(raw, &payload); err != nil {
		log.Fatalf("parse backup: %v", err)
	}
	if payload.Format != "" && payload.Format != backup.FormatID {
		log.Printf("warning: unknown format %q; continuing if JSON matches FullBackup", payload.Format)
	}

	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		log.Fatalf("discord session: %v", err)
	}

	opts := restore.Options{
		DryRun: strings.EqualFold(os.Getenv("RESTORE_DRY_RUN"), "true"),
		Delay:  parseDelayMS(os.Getenv("RESTORE_DELAY_MS")),
		Logf:   log.Printf,
	}

	if err := restore.ToTargetGuild(dg, targetGuild, &payload, opts); err != nil {
		log.Fatalf("restore: %v", err)
	}
	log.Print("restore: ok")
}

func parseDelayMS(s string) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0
	}
	return time.Duration(n) * time.Millisecond
}

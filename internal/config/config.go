package config

import (
	"fmt"
	"os"
	"strings"
)

type Config struct {
	DiscordClientID     string
	DiscordClientSecret string
	DiscordRedirectURI  string
	SessionSecret       string
	BotToken            string
	Port                string
	SkipMembers         bool
	SkipMessages        bool
}

func Load() (*Config, error) {
	c := &Config{
		DiscordClientID:     strings.TrimSpace(os.Getenv("DISCORD_CLIENT_ID")),
		DiscordClientSecret: strings.TrimSpace(os.Getenv("DISCORD_CLIENT_SECRET")),
		DiscordRedirectURI:  strings.TrimSpace(os.Getenv("DISCORD_REDIRECT_URI")),
		SessionSecret:       strings.TrimSpace(os.Getenv("SESSION_SECRET")),
		BotToken:            strings.TrimSpace(os.Getenv("DISCORD_BOT_TOKEN")),
		Port:                strings.TrimSpace(os.Getenv("PORT")),
		SkipMembers:         strings.EqualFold(os.Getenv("BACKUP_SKIP_MEMBERS"), "true"),
		SkipMessages:        strings.EqualFold(os.Getenv("BACKUP_SKIP_MESSAGES"), "true"),
	}
	if c.Port == "" {
		c.Port = "3000"
	}
	var miss []string
	if c.DiscordClientID == "" {
		miss = append(miss, "DISCORD_CLIENT_ID")
	}
	if c.DiscordClientSecret == "" {
		miss = append(miss, "DISCORD_CLIENT_SECRET")
	}
	if c.DiscordRedirectURI == "" {
		miss = append(miss, "DISCORD_REDIRECT_URI")
	}
	if c.SessionSecret == "" {
		miss = append(miss, "SESSION_SECRET")
	}
	if len(miss) > 0 {
		return nil, fmt.Errorf("missing env: %s (copy .env.example to .env)", strings.Join(miss, ", "))
	}
	return c, nil
}

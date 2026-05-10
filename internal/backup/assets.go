package backup

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// SaveGuildIcon downloads the guild icon from the CDN into dir. The file is named
// guild_icon.png, guild_icon.gif, or guild_icon.webp depending on the asset. If the guild has
// no icon, SaveGuildIcon returns nil without creating a file.
func SaveGuildIcon(client *http.Client, guild *discordgo.Guild, dir string) (savedPath string, err error) {
	if guild == nil || guild.Icon == "" {
		return "", nil
	}
	if client == nil {
		client = http.DefaultClient
	}
	url := discordgo.EndpointGuildIcon(guild.ID, guild.Icon)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("guild icon HTTP %s", resp.Status)
	}

	ext := ".png"
	if strings.HasPrefix(guild.Icon, "a_") {
		ext = ".gif"
	} else if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "webp") {
		ext = ".webp"
	}
	name := "guild_icon" + ext
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return "", err
	}
	return path, nil
}

package backup

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/bwmarrin/discordgo"
)

const FormatID = "partydiscord-full-backup"

// FullBackup is as complete as the Discord bot API allows for a single guild.
// Attachment URLs may expire; download assets separately if you need them forever.
type FullBackup struct {
	Format     string    `json:"format"`
	Version    int       `json:"version"`
	ExportedAt string    `json:"exportedAt"`
	Guild      *discordgo.Guild `json:"guild"`
	Roles      []*discordgo.Role `json:"roles"`
	Channels   []*discordgo.Channel `json:"channels"`
	Emojis     []*discordgo.Emoji `json:"emojis"`
	Stickers   json.RawMessage `json:"stickers"`
	Webhooks   []*discordgo.Webhook `json:"webhooks"`
	Invites    []*discordgo.Invite `json:"invites"`
	Bans       []*discordgo.GuildBan `json:"bans"`
	Members    []*discordgo.Member `json:"members,omitempty"`
	Scheduled  []*discordgo.GuildScheduledEvent `json:"scheduled_events"`
	AutoMod    []*discordgo.AutoModerationRule `json:"auto_moderation_rules"`
	Integrations []*discordgo.Integration `json:"integrations"`
	ActiveThreads *discordgo.ThreadsList `json:"active_threads"`
	// Messages keyed by channel ID, oldest → newest within each slice.
	Messages map[string][]json.RawMessage `json:"messages"`
	// ThreadMembers keyed by thread (channel) ID when listing succeeded.
	ThreadMembers map[string][]*discordgo.ThreadMember `json:"thread_members,omitempty"`
	// ChannelSidebarOrder lists channel IDs in Discord sidebar order (categories, positions, threads after parents).
	ChannelSidebarOrder []string `json:"channel_sidebar_order,omitempty"`
	Notes      []string `json:"notes"`
}

type Options struct {
	SkipMembers   bool
	SkipMessages  bool
	Logf          func(format string, args ...interface{})
}

func logf(opts Options, format string, args ...interface{}) {
	if opts.Logf != nil {
		opts.Logf(format, args...)
	} else {
		log.Printf(format, args...)
	}
}

func channelHasTextMessages(t discordgo.ChannelType) bool {
	switch t {
	case discordgo.ChannelTypeGuildText,
		discordgo.ChannelTypeGuildNews,
		discordgo.ChannelTypeGuildPublicThread,
		discordgo.ChannelTypeGuildPrivateThread,
		discordgo.ChannelTypeGuildNewsThread:
		return true
	default:
		return false
	}
}

func fetchAllChannelMessages(s *discordgo.Session, channelID string) ([]json.RawMessage, error) {
	var out []json.RawMessage
	before := ""
	for {
		msgs, err := s.ChannelMessages(channelID, 100, before, "", "")
		if err != nil {
			return out, err
		}
		if len(msgs) == 0 {
			break
		}
		for i := len(msgs) - 1; i >= 0; i-- {
			raw, err := json.Marshal(msgs[i])
			if err != nil {
				continue
			}
			out = append(out, raw)
		}
		if len(msgs) < 100 {
			break
		}
		before = msgs[len(msgs)-1].ID
	}
	return out, nil
}

func collectArchivedForumThreads(s *discordgo.Session, forumChannelID string, opts Options) []*discordgo.Channel {
	var out []*discordgo.Channel
	var before *time.Time
	for {
		list, err := s.ThreadsArchived(forumChannelID, before, 100)
		if err != nil {
			logf(opts, "threads archived public %s: %v", forumChannelID, err)
			break
		}
		if list == nil || len(list.Threads) == 0 {
			break
		}
		out = append(out, list.Threads...)
		if !list.HasMore {
			break
		}
		last := list.Threads[len(list.Threads)-1]
		if last.ThreadMetadata == nil {
			break
		}
		t := last.ThreadMetadata.ArchiveTimestamp
		before = &t
	}
	before = nil
	for {
		list, err := s.ThreadsPrivateArchived(forumChannelID, before, 100)
		if err != nil {
			logf(opts, "threads archived private %s: %v", forumChannelID, err)
			break
		}
		if list == nil || len(list.Threads) == 0 {
			break
		}
		out = append(out, list.Threads...)
		if !list.HasMore {
			break
		}
		last := list.Threads[len(list.Threads)-1]
		if last.ThreadMetadata == nil {
			break
		}
		t := last.ThreadMetadata.ArchiveTimestamp
		before = &t
	}
	return out
}

func fetchGuildStickers(s *discordgo.Session, guildID string) json.RawMessage {
	ep := discordgo.EndpointGuildStickers(guildID)
	body, err := s.RequestWithBucketID("GET", ep, nil, ep)
	if err != nil {
		return json.RawMessage("[]")
	}
	if !json.Valid(body) {
		return json.RawMessage("[]")
	}
	return json.RawMessage(body)
}

func fetchAllBans(s *discordgo.Session, guildID string) ([]*discordgo.GuildBan, error) {
	var all []*discordgo.GuildBan
	after := ""
	for {
		batch, err := s.GuildBans(guildID, 1000, "", after)
		if err != nil {
			return all, err
		}
		if len(batch) == 0 {
			break
		}
		all = append(all, batch...)
		if len(batch) < 1000 {
			break
		}
		after = batch[len(batch)-1].User.ID
	}
	return all, nil
}

func fetchAllMembers(s *discordgo.Session, guildID string) ([]*discordgo.Member, error) {
	var all []*discordgo.Member
	after := ""
	for {
		batch, err := s.GuildMembers(guildID, after, 1000)
		if err != nil {
			return all, err
		}
		if len(batch) == 0 {
			break
		}
		all = append(all, batch...)
		if len(batch) < 1000 {
			break
		}
		after = batch[len(batch)-1].User.ID
	}
	return all, nil
}

// Build creates a full JSON-serializable backup. The bot must be in the guild with broad read access
// (Administrator is simplest). This can take a long time and produce a very large object on big servers.
func Build(s *discordgo.Session, guildID string, opts Options) (*FullBackup, error) {
	guild, err := s.Guild(guildID)
	if err != nil {
		return nil, fmt.Errorf("guild: %w", err)
	}

	channels, err := s.GuildChannels(guildID)
	if err != nil {
		return nil, fmt.Errorf("channels: %w", err)
	}
	roles, err := s.GuildRoles(guildID)
	if err != nil {
		return nil, fmt.Errorf("roles: %w", err)
	}
	emojis, err := s.GuildEmojis(guildID)
	if err != nil {
		return nil, fmt.Errorf("emojis: %w", err)
	}

	stickers := fetchGuildStickers(s, guildID)

	webhooks, err := s.GuildWebhooks(guildID)
	if err != nil {
		logf(opts, "webhooks: %v", err)
		webhooks = nil
	}
	invites, err := s.GuildInvites(guildID)
	if err != nil {
		logf(opts, "invites: %v", err)
		invites = nil
	}
	bans, err := fetchAllBans(s, guildID)
	if err != nil {
		logf(opts, "bans: %v", err)
		bans = nil
	}
	var members []*discordgo.Member
	if !opts.SkipMembers {
		members, err = fetchAllMembers(s, guildID)
		if err != nil {
			logf(opts, "members: %v", err)
			members = nil
		}
	}
	scheduled, err := s.GuildScheduledEvents(guildID, true)
	if err != nil {
		logf(opts, "scheduled events: %v", err)
		scheduled = nil
	}
	autoMod, err := s.AutoModerationRules(guildID)
	if err != nil {
		logf(opts, "auto mod: %v", err)
		autoMod = nil
	}
	integrations, err := s.GuildIntegrations(guildID)
	if err != nil {
		logf(opts, "integrations: %v", err)
		integrations = nil
	}

	sidebarOrder := SidebarOrderedChannelIDs(channels)

	var activeThreads *discordgo.ThreadsList
	var messages map[string][]json.RawMessage
	var threadMembers map[string][]*discordgo.ThreadMember

	if opts.SkipMessages {
		messages = make(map[string][]json.RawMessage)
		threadMembers = nil
	} else {
		activeThreads, err = s.GuildThreadsActive(guildID)
		if err != nil {
			logf(opts, "active threads: %v", err)
			activeThreads = nil
		}

		messageChannelIDs := make(map[string]struct{})
		for _, ch := range channels {
			if ch == nil {
				continue
			}
			if channelHasTextMessages(ch.Type) {
				messageChannelIDs[ch.ID] = struct{}{}
			}
			if ch.Type == discordgo.ChannelTypeGuildForum {
				for _, th := range collectArchivedForumThreads(s, ch.ID, opts) {
					if th != nil {
						messageChannelIDs[th.ID] = struct{}{}
					}
				}
			}
		}
		if activeThreads != nil {
			for _, th := range activeThreads.Threads {
				if th != nil && channelHasTextMessages(th.Type) {
					messageChannelIDs[th.ID] = struct{}{}
				}
			}
		}

		messages = make(map[string][]json.RawMessage)
		for cid := range messageChannelIDs {
			logf(opts, "messages: channel %s", cid)
			msgs, err := fetchAllChannelMessages(s, cid)
			if err != nil {
				logf(opts, "messages %s: %v", cid, err)
				continue
			}
			messages[cid] = msgs
		}

		threadIDs := make(map[string]struct{})
		for _, ch := range channels {
			if ch != nil && isThreadChannelType(ch.Type) {
				threadIDs[ch.ID] = struct{}{}
			}
		}
		if activeThreads != nil {
			for _, th := range activeThreads.Threads {
				if th != nil {
					threadIDs[th.ID] = struct{}{}
				}
			}
		}
		for _, ch := range channels {
			if ch != nil && ch.Type == discordgo.ChannelTypeGuildForum {
				for _, th := range collectArchivedForumThreads(s, ch.ID, opts) {
					if th != nil {
						threadIDs[th.ID] = struct{}{}
					}
				}
			}
		}

		threadMembers = make(map[string][]*discordgo.ThreadMember)
		for cid := range threadIDs {
			tm, err := fetchThreadMembersPaged(s, cid)
			if err != nil {
				logf(opts, "thread members %s: %v", cid, err)
				continue
			}
			if len(tm) > 0 {
				threadMembers[cid] = tm
			}
		}
	}

	out := &FullBackup{
		Format:        FormatID,
		Version:       1,
		ExportedAt:    time.Now().UTC().Format(time.RFC3339),
		Guild:         guild,
		Roles:         roles,
		Channels:      channels,
		Emojis:        emojis,
		Stickers:      stickers,
		Webhooks:      webhooks,
		Invites:       invites,
		Bans:          bans,
		Members:       members,
		Scheduled:     scheduled,
		AutoMod:       autoMod,
		Integrations:  integrations,
		ActiveThreads: activeThreads,
		Messages:            messages,
		ThreadMembers:       threadMembers,
		ChannelSidebarOrder: sidebarOrder,
		Notes: []string{
			"Backup includes structure, settings fields on returned objects, messages (per accessible channel/thread), bans, invites, webhooks, scheduled events, auto-mod rules, integrations, stickers, emojis, optional full member list.",
			"channel_sidebar_order matches Discord sidebar order (categories, channel positions, threads after their parent). Each channel includes permission_overwrites from the API.",
			"Enable the bot's **Message Content Intent** (Developer Portal → Bot) or message bodies may come back empty.",
			"Webhook URLs and tokens are sensitive; keep this file private.",
			"Attachment and embed remote URLs may expire; re-host files if you need permanent copies.",
			"Private threads you never joined may be omitted. Very large guilds: set BACKUP_SKIP_MEMBERS=true to reduce size.",
			"Set BACKUP_SKIP_MESSAGES=true for roles, icon (download separately), channels, and permissions without fetching history.",
			"Discord API + bot permissions limit completeness; some data has no public read API.",
		},
	}
	return out, nil
}

func isThreadChannelType(t discordgo.ChannelType) bool {
	switch t {
	case discordgo.ChannelTypeGuildPublicThread,
		discordgo.ChannelTypeGuildPrivateThread,
		discordgo.ChannelTypeGuildNewsThread:
		return true
	default:
		return false
	}
}

func fetchThreadMembersPaged(s *discordgo.Session, threadID string) ([]*discordgo.ThreadMember, error) {
	var all []*discordgo.ThreadMember
	after := ""
	for {
		batch, err := s.ThreadMembers(threadID, 100, false, after)
		if err != nil {
			return all, err
		}
		if len(batch) == 0 {
			break
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			break
		}
		after = batch[len(batch)-1].UserID
		if after == "" {
			break
		}
	}
	return all, nil
}

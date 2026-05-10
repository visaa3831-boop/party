package restore

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"partydiscord/internal/backup"
)

// rolePositionUpdate is the minimal PATCH body Discord expects for role reorder (avoid sending empty Role fields).
type rolePositionUpdate struct {
	ID       string `json:"id"`
	Position int    `json:"position"`
}

// Options controls restore behavior.
type Options struct {
	DryRun bool
	// Delay pauses between API calls to reduce rate-limit bursts (optional).
	Delay time.Duration
	Logf  func(format string, args ...interface{})
}

func logf(o Options, format string, args ...interface{}) {
	if o.Logf != nil {
		o.Logf(format, args...)
	} else {
		log.Printf(format, args...)
	}
}

func maybeSleep(o Options) {
	if o.Delay > 0 {
		time.Sleep(o.Delay)
	}
}

func isThreadType(t discordgo.ChannelType) bool {
	switch t {
	case discordgo.ChannelTypeGuildPublicThread,
		discordgo.ChannelTypeGuildPrivateThread,
		discordgo.ChannelTypeGuildNewsThread:
		return true
	default:
		return false
	}
}

func remapOverwrites(src []*discordgo.PermissionOverwrite, roleMap map[string]string, srcGuildID, tgtGuildID string, o Options) []*discordgo.PermissionOverwrite {
	var out []*discordgo.PermissionOverwrite
	for _, ow := range src {
		if ow == nil {
			continue
		}
		id := ow.ID
		switch ow.Type {
		case discordgo.PermissionOverwriteTypeRole:
			if id == srcGuildID {
				id = tgtGuildID
			} else if mapped, ok := roleMap[id]; ok {
				id = mapped
			} else {
				logf(o, "restore: skip permission overwrite for unknown role id %s", ow.ID)
				continue
			}
		case discordgo.PermissionOverwriteTypeMember:
			// Same Discord user ID across guilds.
		default:
			continue
		}
		out = append(out, &discordgo.PermissionOverwrite{
			ID:    id,
			Type:  ow.Type,
			Allow: ow.Allow,
			Deny:  ow.Deny,
		})
	}
	return out
}

// Discord only allows certain channel types at create time (see API error BASE_TYPE_CHOICES). Announcement
// channels (type 5) must be created as guild text (0) then updated.
func guildChannelCreateTypeAPI(orig discordgo.ChannelType) (create discordgo.ChannelType, patchTo *discordgo.ChannelType) {
	switch orig {
	case discordgo.ChannelTypeGuildNews:
		t := discordgo.ChannelTypeGuildNews
		return discordgo.ChannelTypeGuildText, &t
	default:
		return orig, nil
	}
}

func patchChannelType(s *discordgo.Session, channelID string, typ discordgo.ChannelType, o Options) error {
	payload := map[string]int{"type": int(typ)}
	_, err := s.RequestWithBucketID("PATCH", discordgo.EndpointChannel(channelID), payload, discordgo.EndpointChannel(channelID))
	if err != nil {
		return err
	}
	logf(o, "restore: set channel %s type -> %d", channelID, int(typ))
	maybeSleep(o)
	return nil
}

func cloneForumTags(old []discordgo.ForumTag) []discordgo.ForumTag {
	out := make([]discordgo.ForumTag, 0, len(old))
	for _, t := range old {
		out = append(out, discordgo.ForumTag{
			Name:      t.Name,
			Moderated: t.Moderated,
			EmojiID:   t.EmojiID,
			EmojiName: t.EmojiName,
		})
	}
	return out
}

// ToTargetGuild recreates roles (except managed integrations), then categories/channels in sidebar order,
// applying permission overwrites with role IDs mapped to newly created roles. @everyone permissions are
// copied onto the target server's everyone role (targetGuildID → role id).
//
// Limitations: thread channels are skipped (cannot recreate threads from backup alone). Managed roles are not
// recreated; overwrites referencing missing roles are skipped. The bot must have Manage Roles and Manage Channels.
// Role reorder needs the bot’s own role near the top of the hierarchy; otherwise Discord may reject moves—drag the
// bot role up in Server Settings → Roles, then run restore again or finish ordering by hand. Announcement channels
// are created as text then switched to announcement (API restriction). Forum tags referencing guild-specific emojis may fail.
func ToTargetGuild(s *discordgo.Session, targetGuildID string, b *backup.FullBackup, o Options) error {
	if b == nil || b.Guild == nil {
		return fmt.Errorf("backup missing guild")
	}
	srcGuildID := b.Guild.ID

	if _, err := s.Guild(targetGuildID); err != nil {
		return fmt.Errorf("target guild: %w", err)
	}

	roleMap := make(map[string]string)
	roleMap[srcGuildID] = targetGuildID

	// --- @everyone on target ---
	var everyoneSrc *discordgo.Role
	for _, r := range b.Roles {
		if r != nil && r.ID == srcGuildID {
			everyoneSrc = r
			break
		}
	}
	if everyoneSrc != nil && !o.DryRun {
		perms := everyoneSrc.Permissions
		edit := &discordgo.RoleParams{Permissions: &perms}
		if _, err := s.GuildRoleEdit(targetGuildID, targetGuildID, edit); err != nil {
			logf(o, "restore: edit @everyone on target: %v", err)
		}
		maybeSleep(o)
	} else if everyoneSrc != nil && o.DryRun {
		logf(o, "restore: would sync @everyone permissions bitmask")
	}

	// --- create roles (exclude managed + everyone) ---
	var toCreate []*discordgo.Role
	for _, r := range b.Roles {
		if r == nil || r.ID == srcGuildID || r.Managed {
			continue
		}
		toCreate = append(toCreate, r)
	}
	sortRolesForRestore(toCreate)

	for _, r := range toCreate {
		name := r.Name
		if o.DryRun {
			logf(o, "restore: would create role %q (src id %s)", name, r.ID)
			continue
		}
		color := r.Color
		hoist := r.Hoist
		mention := r.Mentionable
		perms := r.Permissions
		params := &discordgo.RoleParams{
			Name:        name,
			Color:       &color,
			Hoist:       &hoist,
			Mentionable: &mention,
			Permissions: &perms,
		}
		if r.UnicodeEmoji != "" {
			u := r.UnicodeEmoji
			params.UnicodeEmoji = &u
		}
		newRole, err := s.GuildRoleCreate(targetGuildID, params)
		if err != nil {
			return fmt.Errorf("create role %q: %w", name, err)
		}
		roleMap[r.ID] = newRole.ID
		logf(o, "restore: created role %q -> %s", name, newRole.ID)
		maybeSleep(o)
	}

	if !o.DryRun {
		if err := reorderRolesFromBackup(s, targetGuildID, b, roleMap, srcGuildID, o); err != nil {
			logf(o, "restore: role reorder: %v", err)
		}
	}

	// --- channels ---
	byID := make(map[string]*discordgo.Channel)
	for _, ch := range b.Channels {
		if ch != nil {
			byID[ch.ID] = ch
		}
	}

	order := b.ChannelSidebarOrder
	if len(order) == 0 {
		order = backup.SidebarOrderedChannelIDs(b.Channels)
	}

	channelMap := make(map[string]string)

	for _, id := range order {
		ch := byID[id]
		if ch == nil {
			continue
		}
		if isThreadType(ch.Type) {
			logf(o, "restore: skip thread channel %q (%s)", ch.Name, ch.ID)
			continue
		}

		parentNew := ""
		if ch.ParentID != "" {
			mapped, ok := channelMap[ch.ParentID]
			if !ok {
				return fmt.Errorf("channel %q: parent %s not restored yet (ordering bug or missing parent)", ch.Name, ch.ParentID)
			}
			parentNew = mapped
		}

		overs := remapOverwrites(ch.PermissionOverwrites, roleMap, srcGuildID, targetGuildID, o)

		createType, patchChannelKind := guildChannelCreateTypeAPI(ch.Type)

		data := discordgo.GuildChannelCreateData{
			Name:                 ch.Name,
			Type:                 createType,
			Topic:                ch.Topic,
			PermissionOverwrites: overs,
			ParentID:             parentNew,
			NSFW:                 ch.NSFW,
		}
		if ch.Type == discordgo.ChannelTypeGuildVoice ||
			ch.Type == discordgo.ChannelTypeGuildStageVoice {
			data.Bitrate = ch.Bitrate
			data.UserLimit = ch.UserLimit
		}
		if ch.RateLimitPerUser > 0 {
			data.RateLimitPerUser = ch.RateLimitPerUser
		}

		if o.DryRun {
			logf(o, "restore: would create channel %q type=%d (api create %d) parent=%s", ch.Name, int(ch.Type), int(createType), parentNew)
			channelMap[ch.ID] = "dry-run-" + ch.ID
			continue
		}

		created, err := s.GuildChannelCreateComplex(targetGuildID, data)
		if err != nil {
			return fmt.Errorf("create channel %q: %w", ch.Name, err)
		}
		channelMap[ch.ID] = created.ID
		logf(o, "restore: created channel %q -> %s", ch.Name, created.ID)
		maybeSleep(o)

		if patchChannelKind != nil {
			if err := patchChannelType(s, created.ID, *patchChannelKind, o); err != nil {
				logf(o, "restore: channel %q set type %d: %v", ch.Name, int(*patchChannelKind), err)
			}
		}

		if ch.Type == discordgo.ChannelTypeGuildForum && len(ch.AvailableTags) > 0 {
			tags := cloneForumTags(ch.AvailableTags)
			edit := &discordgo.ChannelEdit{AvailableTags: &tags}
			if _, err := s.ChannelEditComplex(created.ID, edit); err != nil {
				logf(o, "restore: forum tags for %q: %v", ch.Name, err)
			}
			maybeSleep(o)
		}
	}

	logf(o, "restore: finished")
	return nil
}

func sortRolesForRestore(roles []*discordgo.Role) {
	sort.SliceStable(roles, func(i, j int) bool {
		if roles[i].Position != roles[j].Position {
			return roles[i].Position < roles[j].Position
		}
		return strings.Compare(roles[i].ID, roles[j].ID) < 0
	})
}

// reorderRolesFromBackup assigns sequential positions bottom→top: @everyone, restored roles in backup
// position order (low → high), then any other roles (e.g. integrations) by API position. Pre-existing roles
// end up above the restored block; drag them in Discord if you need the bot under manual roles.
func reorderRolesFromBackup(s *discordgo.Session, targetGuildID string, b *backup.FullBackup, roleMap map[string]string, srcGuildID string, o Options) error {
	rev := make(map[string]string)
	for oldID, newID := range roleMap {
		if oldID == srcGuildID {
			continue
		}
		rev[newID] = oldID
	}

	var toCreate []*discordgo.Role
	for _, r := range b.Roles {
		if r == nil || r.ID == srcGuildID || r.Managed {
			continue
		}
		toCreate = append(toCreate, r)
	}
	sortRolesForRestore(toCreate)

	allRoles, err := s.GuildRoles(targetGuildID)
	if err != nil {
		return err
	}

	ordered := make([]string, 0, len(allRoles))
	ordered = append(ordered, targetGuildID)
	for _, r := range toCreate {
		if nw := roleMap[r.ID]; nw != "" {
			ordered = append(ordered, nw)
		}
	}

	var extra []*discordgo.Role
	for _, r := range allRoles {
		if r == nil || r.ID == targetGuildID {
			continue
		}
		if _, ok := rev[r.ID]; ok {
			continue
		}
		extra = append(extra, r)
	}
	sort.SliceStable(extra, func(i, j int) bool {
		if extra[i].Position != extra[j].Position {
			return extra[i].Position < extra[j].Position
		}
		return extra[i].ID < extra[j].ID
	})
	for _, r := range extra {
		ordered = append(ordered, r.ID)
	}

	if len(ordered) != len(allRoles) {
		return fmt.Errorf("role reorder: expected %d roles, built %d", len(allRoles), len(ordered))
	}

	updates := make([]rolePositionUpdate, len(ordered))
	for i, id := range ordered {
		updates[i] = rolePositionUpdate{ID: id, Position: i}
	}

	_, err = s.RequestWithBucketID("PATCH", discordgo.EndpointGuildRoles(targetGuildID), updates, discordgo.EndpointGuildRoles(targetGuildID))
	if err != nil {
		return err
	}
	logf(o, "restore: reordered %d roles (everyone + restored + other)", len(updates))
	maybeSleep(o)
	return nil
}

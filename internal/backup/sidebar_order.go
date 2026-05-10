package backup

import (
	"sort"

	"github.com/bwmarrin/discordgo"
)

func sortChannelsStable(chs []*discordgo.Channel) {
	sort.SliceStable(chs, func(i, j int) bool {
		if chs[i].Position != chs[j].Position {
			return chs[i].Position < chs[j].Position
		}
		return chs[i].ID < chs[j].ID
	})
}

// SidebarOrderedChannels returns channels in Discord sidebar order: root channels and categories
// sorted by position, channels nested under each category, and thread channels immediately after
// their parent text, announcement, or forum channel. Permission overwrites are unchanged on each
// channel object — only list order changes.
func SidebarOrderedChannels(all []*discordgo.Channel) []*discordgo.Channel {
	byParent := make(map[string][]*discordgo.Channel)
	for _, ch := range all {
		if ch == nil {
			continue
		}
		p := ch.ParentID
		byParent[p] = append(byParent[p], ch)
	}
	for _, list := range byParent {
		sortChannelsStable(list)
	}

	var walk func(parent string) []*discordgo.Channel
	walk = func(parent string) []*discordgo.Channel {
		var out []*discordgo.Channel
		for _, ch := range byParent[parent] {
			if parent == "" && isThreadChannelType(ch.Type) {
				continue
			}
			out = append(out, ch)
			switch ch.Type {
			case discordgo.ChannelTypeGuildCategory:
				out = append(out, walk(ch.ID)...)
			case discordgo.ChannelTypeGuildText, discordgo.ChannelTypeGuildNews, discordgo.ChannelTypeGuildForum:
				out = appendThreadChildren(out, byParent, ch.ID)
			}
		}
		return out
	}

	ordered := walk("")
	seen := make(map[string]struct{}, len(ordered))
	for _, ch := range ordered {
		seen[ch.ID] = struct{}{}
	}
	var rest []*discordgo.Channel
	for _, ch := range all {
		if ch == nil {
			continue
		}
		if _, ok := seen[ch.ID]; ok {
			continue
		}
		rest = append(rest, ch)
	}
	if len(rest) > 0 {
		sortChannelsStable(rest)
		ordered = append(ordered, rest...)
	}
	return ordered
}

func appendThreadChildren(out []*discordgo.Channel, byParent map[string][]*discordgo.Channel, parentID string) []*discordgo.Channel {
	subs := byParent[parentID]
	var threads []*discordgo.Channel
	for _, s := range subs {
		if s == nil || !isThreadChannelType(s.Type) {
			continue
		}
		threads = append(threads, s)
	}
	if len(threads) == 0 {
		return out
	}
	sortChannelsStable(threads)
	return append(out, threads...)
}

// SidebarOrderedChannelIDs returns channel IDs in the same order as SidebarOrderedChannels.
func SidebarOrderedChannelIDs(all []*discordgo.Channel) []string {
	ordered := SidebarOrderedChannels(all)
	ids := make([]string, 0, len(ordered))
	for _, ch := range ordered {
		ids = append(ids, ch.ID)
	}
	return ids
}

package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// parseFlexibleDuration parses values like "30s", "5m", "2h", "3d", "90m", or "120" (plain number = minutes).
func parseFlexibleDuration(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("empty duration")
	}
	s := strings.ToLower(raw)

	i := len(s)
	for i > 0 {
		c := s[i-1]
		if (c >= 'a' && c <= 'z') || c == '"' || c == '\'' {
			i--
			continue
		}
		break
	}
	numPart := strings.TrimSpace(s[:i])
	unitPart := strings.TrimSpace(s[i:])
	if numPart == "" {
		return 0, fmt.Errorf("invalid duration %q", raw)
	}

	n64, err := strconv.ParseUint(numPart, 10, 32)
	if err != nil || n64 == 0 {
		return 0, fmt.Errorf("invalid duration %q", raw)
	}
	n := uint64(n64)

	switch unitPart {
	case "", "m", "min", "mins", "minute", "minutes":
		return time.Duration(n) * time.Minute, nil
	case "s", "sec", "secs", "second", "seconds":
		return time.Duration(n) * time.Second, nil
	case "h", "hr", "hrs", "hour", "hours":
		return time.Duration(n) * time.Hour, nil
	case "d", "day", "days":
		return time.Duration(n) * 24 * time.Hour, nil
	case "w", "wk", "week", "weeks":
		return time.Duration(n) * 7 * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("unknown unit in %q (use s, m, h, d, w, or no unit for minutes)", raw)
	}
}

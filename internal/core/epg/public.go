package epg

import (
	"fmt"
	"strings"
)

// PublicTvgID is the playlist/XMLTV id for a channel bound to a source channel.
// Empty if either input is missing so callers can omit tvg-id.
func PublicTvgID(sourceID int64, sourceTvgID string) string {
	sourceTvgID = strings.TrimSpace(sourceTvgID)
	if sourceID <= 0 || sourceTvgID == "" {
		return ""
	}
	return fmt.Sprintf("epg%d-%s", sourceID, sourceTvgID)
}

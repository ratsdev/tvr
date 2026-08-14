package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/ratsdev/tvr/internal/core/epg"
	"github.com/ratsdev/tvr/internal/core/store"
)

func (s *Server) handleListRelays(w http.ResponseWriter, r *http.Request) {
	relays, err := s.store.ListRelays(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	type item struct {
		store.Relay
		PlaylistURL string `json:"playlist_url"`
		EPGURL      string `json:"epg_url"`
	}
	out := make([]item, 0, len(relays))
	base := s.publicBaseURL(r)
	for _, rel := range relays {
		out = append(out, item{
			Relay:       rel,
			PlaylistURL: fmt.Sprintf("%s/r/%s/playlist.m3u", base, rel.Slug),
			EPGURL:      fmt.Sprintf("%s/r/%s/epg.xml", base, rel.Slug),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateRelay(w http.ResponseWriter, r *http.Request) {
	var in store.RelayInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	rel, err := s.store.CreateRelay(r.Context(), in)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	detail, err := s.store.GetRelayDetail(r.Context(), rel.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, detail)
}

func (s *Server) handleGetRelay(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	detail, err := s.store.GetRelayDetail(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleUpdateRelay(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var in store.RelayInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if _, err := s.workflows.UpdateRelay(r.Context(), id, in); err != nil {
		s.writeWorkflowError(w, err)
		return
	}
	detail, err := s.store.GetRelayDetail(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleDeleteRelay(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.workflows.DeleteRelay(r.Context(), id); err != nil {
		s.writeWorkflowError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleReplaceRelayLayout(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var layout store.RelayLayout
	if err := decodeJSON(r, &layout); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	detail, err := s.store.ReplaceRelayLayout(r.Context(), id, layout)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleCreateRelayGroup(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	g, err := s.store.CreateRelayGroup(r.Context(), id, body.Name)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

func (s *Server) handleUpdateRelayGroup(w http.ResponseWriter, r *http.Request) {
	relayID, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	groupID, err := pathID(r, "groupId")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	g, err := s.store.UpdateRelayGroup(r.Context(), relayID, groupID, body.Name)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) handleDeleteRelayGroup(w http.ResponseWriter, r *http.Request) {
	relayID, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	groupID, err := pathID(r, "groupId")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.DeleteRelayGroup(r.Context(), relayID, groupID); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAddMembership(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var in store.MembershipInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	m, err := s.workflows.AddMembership(r.Context(), id, in)
	if err != nil {
		s.writeWorkflowError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) handleUpdateMembership(w http.ResponseWriter, r *http.Request) {
	relayID, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	membershipID, err := pathID(r, "membershipId")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var in store.MembershipInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	m, err := s.workflows.UpdateMembership(r.Context(), relayID, membershipID, in)
	if err != nil {
		s.writeWorkflowError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) handleDeleteMembership(w http.ResponseWriter, r *http.Request) {
	relayID, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	membershipID, err := pathID(r, "membershipId")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.workflows.DeleteMembership(r.Context(), relayID, membershipID); err != nil {
		s.writeWorkflowError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRelayPlaylist(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	_, lineup, err := s.store.ListRelayLineup(r.Context(), slug)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	base := s.publicBaseURL(r)
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	fmt.Fprintf(w, `#EXTM3U url-tvg="%s/r/%s/epg.xml"`+"\n", base, slug)
	for _, e := range lineup {
		attrs := make([]string, 0, 5)
		var sourceID int64
		if e.EPGSourceID != nil {
			sourceID = *e.EPGSourceID
		}
		if pub := epg.PublicTvgID(sourceID, e.TvgID); pub != "" {
			attrs = append(attrs, fmt.Sprintf(`tvg-id="%s"`, escapeAttr(pub)))
		}
		attrs = append(attrs, fmt.Sprintf(`tvg-name="%s"`, escapeAttr(e.Name)))
		if e.LogoURL != "" {
			attrs = append(attrs, fmt.Sprintf(`tvg-logo="%s"`, escapeAttr(e.LogoURL)))
		}
		if e.Number > 0 {
			attrs = append(attrs, fmt.Sprintf(`tvg-chno="%d"`, e.Number))
		}
		if e.GroupTitle != "" {
			attrs = append(attrs, fmt.Sprintf(`group-title="%s"`, escapeAttr(e.GroupTitle)))
		}
		fmt.Fprintf(w, "#EXTINF:-1 %s,%s\n", strings.Join(attrs, " "), sanitizePlaylistText(e.Name))
		fmt.Fprintf(w, "%s/stream/%s\n", base, e.ChannelID)
	}
}

func (s *Server) handleRelayEPG(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if _, err := s.store.GetRelayBySlug(r.Context(), slug); err != nil {
		writeStoreError(w, err)
		return
	}
	f, fi, err := s.epg.OpenRelayCache(slug)
	if err != nil {
		http.Error(w, "epg not available yet", http.StatusServiceUnavailable)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=60")
	http.ServeContent(w, r, "epg.xml", fi.ModTime(), f)
}

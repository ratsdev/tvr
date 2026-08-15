package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ratsdev/tvr/internal/core/store"
)

func (s *Server) handleExportBackup(w http.ResponseWriter, r *http.Request) {
	backup, err := s.store.ExportLibrary(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	name := fmt.Sprintf("tvr-library-%s.json", time.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(backup)
}

type libraryRestoreResponse struct {
	store.LibraryRestoreResult
	Warning string `json:"warning,omitempty"`
}

func (s *Server) handleRestoreBackup(w http.ResponseWriter, r *http.Request) {
	var in store.LibraryBackup
	if err := decodeJSONBody(r, &in, 32<<20, false); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.channelMu.Lock()
	defer s.channelMu.Unlock()
	result, err := s.workflows.RestoreLibrary(r.Context(), in)
	if err != nil {
		s.writeWorkflowError(w, err)
		return
	}
	out := libraryRestoreResponse{LibraryRestoreResult: result}
	if err := s.syncRestoredLive(result.RemovedChannelIDs, result.ChannelIDs); err != nil {
		s.logger.Error("update live sessions after restore", "err", err)
		out.Warning = fmt.Sprintf("live session update failed: %v", err)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) syncRestoredLive(removed, ids []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), channelStreamCleanupTimeout)
	defer cancel()
	for _, id := range removed {
		if err := s.live.BlockChannel(ctx, id); err != nil {
			return err
		}
	}
	return s.publishChannelIDs(ids, true)
}

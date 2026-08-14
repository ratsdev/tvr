package epg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jqjiang/tvr/internal/core/store"
)

func (s *Service) writeRelayCache(slug string, doc *tvDocument) error {
	if err := os.MkdirAll(s.cacheDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(s.cacheDir, slug+".xml")
	tmp, err := os.CreateTemp(s.cacheDir, slug+"-*.xml.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()
	if err := writeXMLTV(tmp, doc); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func mergeDocument(dst, src *tvDocument, rewrite map[string]string) {
	if src == nil {
		return
	}
	for id, ch := range src.Channels {
		publicID, ok := rewrite[id]
		if !ok {
			continue
		}
		ch.ID = publicID
		dst.Channels[publicID] = ch
	}
	for _, prog := range src.Programmes {
		publicID, ok := rewrite[prog.Channel]
		if !ok {
			continue
		}
		prog.Channel = publicID
		dst.Programmes = append(dst.Programmes, prog)
	}
}

func (s *Service) relaysReferencingSource(ctx context.Context, sourceID int64) ([]int64, error) {
	mappings, err := s.store.ListAllRelayEPGMappings(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[int64]struct{}{}
	var ids []int64
	for _, m := range mappings {
		if m.EPGSourceID != sourceID {
			continue
		}
		if _, ok := seen[m.RelayID]; ok {
			continue
		}
		seen[m.RelayID] = struct{}{}
		ids = append(ids, m.RelayID)
	}
	return ids, nil
}

func (s *Service) processRelay(ctx context.Context, relayID int64) error {
	relay, err := s.store.GetRelay(ctx, relayID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	mappings, err := s.store.ListAllRelayEPGMappings(ctx)
	if err != nil {
		return err
	}
	allowed := map[int64]map[string]string{}
	needed := map[int64]struct{}{}
	for _, m := range mappings {
		if m.RelayID != relayID {
			continue
		}
		publicID := PublicTvgID(m.EPGSourceID, m.TvgID)
		if publicID == "" {
			continue
		}
		if allowed[m.EPGSourceID] == nil {
			allowed[m.EPGSourceID] = map[string]string{}
		}
		allowed[m.EPGSourceID][m.TvgID] = publicID
		needed[m.EPGSourceID] = struct{}{}
	}

	docs := map[int64]*tvDocument{}
	for sourceID := range needed {
		src, err := s.store.GetEPGSource(ctx, sourceID)
		if err != nil {
			s.setError(fmt.Sprintf("relay %s incomplete: source %d unavailable", relay.Slug, sourceID))
			return nil
		}
		_, xmlPath, _, ok := s.eligibleSource(sourceID, src.URL)
		if !ok {
			s.setError(fmt.Sprintf("relay %s incomplete: source %q unavailable", relay.Slug, src.Name))
			return nil
		}
		doc, err := s.loadSourceCacheDocPath(xmlPath)
		if err != nil {
			s.setError(fmt.Sprintf("relay %s incomplete: source %q unreadable", relay.Slug, src.Name))
			return nil
		}
		docs[sourceID] = doc
	}

	merged := &tvDocument{
		GeneratorInfoName: "tvr",
		Channels:          map[string]tvChannel{},
		Programmes:        []tvProgramme{},
	}
	for sourceID, tvgIDs := range allowed {
		mergeDocument(merged, docs[sourceID], tvgIDs)
	}
	if err := s.writeRelayCache(relay.Slug, merged); err != nil {
		return err
	}
	s.setOK()
	return nil
}

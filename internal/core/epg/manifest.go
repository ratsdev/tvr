package epg

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type sourceManifest struct {
	Generation string `json:"generation"`
	SourceURL  string `json:"source_url"`
	FetchedAt  string `json:"fetched_at"`
}

type indexFile struct {
	SourceURL  string        `json:"source_url"`
	Generation string        `json:"generation"`
	Channels   []ChannelInfo `json:"channels"`
}

type indexTag struct {
	SourceURL  string
	Generation string
	Channels   []ChannelInfo
}

func normalizeSourceURL(raw string) string {
	return strings.TrimSpace(raw)
}

func newGenerationID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%d-%s", time.Now().UnixNano(), hex.EncodeToString(b[:]))
}

func (s *Service) sourceManifestPath(id int64) string {
	return filepath.Join(s.sourceDir, fmt.Sprintf("%d.manifest.json", id))
}

func (s *Service) sourceGenerationXMLPath(id int64, gen string) string {
	return filepath.Join(s.sourceDir, fmt.Sprintf("%d.%s.xml", id, gen))
}

func (s *Service) sourceGenerationIndexPath(id int64, gen string) string {
	return filepath.Join(s.indexDir, fmt.Sprintf("%d.%s.json", id, gen))
}

func (s *Service) readSourceManifest(id int64) (sourceManifest, error) {
	data, err := os.ReadFile(s.sourceManifestPath(id))
	if err != nil {
		return sourceManifest{}, err
	}
	var m sourceManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return sourceManifest{}, err
	}
	m.SourceURL = normalizeSourceURL(m.SourceURL)
	return m, nil
}

func (s *Service) writeSourceManifest(id int64, m sourceManifest) error {
	if err := os.MkdirAll(s.sourceDir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := s.sourceManifestPath(id) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.sourceManifestPath(id))
}

func (s *Service) writeGenerationIndex(id int64, gen, sourceURL string, channels []ChannelInfo) error {
	if err := os.MkdirAll(s.indexDir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(indexFile{
		SourceURL:  normalizeSourceURL(sourceURL),
		Generation: gen,
		Channels:   channels,
	})
	if err != nil {
		return err
	}
	path := s.sourceGenerationIndexPath(id, gen)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Service) writeGenerationXML(id int64, gen string, doc *tvDocument) error {
	if err := os.MkdirAll(s.sourceDir, 0o755); err != nil {
		return err
	}
	path := s.sourceGenerationXMLPath(id, gen)
	tmp, err := os.CreateTemp(s.sourceDir, fmt.Sprintf("%d-*.xml.tmp", id))
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

// writeSourceGenerationFiles writes XML/index for a new generation without publishing the manifest.
func (s *Service) writeSourceGenerationFiles(id int64, sourceURL string, doc *tvDocument) (string, []ChannelInfo, error) {
	gen := newGenerationID()
	channels := indexFromDoc(doc)
	if err := s.writeGenerationXML(id, gen, doc); err != nil {
		return "", nil, err
	}
	if err := s.writeGenerationIndex(id, gen, sourceURL, channels); err != nil {
		_ = os.Remove(s.sourceGenerationXMLPath(id, gen))
		return "", nil, err
	}
	return gen, channels, nil
}

func (s *Service) loadIndexFile(path string) (indexFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return indexFile{}, err
	}
	var tagged indexFile
	if err := json.Unmarshal(data, &tagged); err != nil {
		return indexFile{}, err
	}
	return tagged, nil
}

func (s *Service) eligibleSource(id int64, dbURL string) (sourceManifest, string, indexTag, bool) {
	dbURL = normalizeSourceURL(dbURL)
	m, err := s.readSourceManifest(id)
	if err != nil || m.Generation == "" || m.SourceURL == "" || m.SourceURL != dbURL {
		return sourceManifest{}, "", indexTag{}, false
	}
	xmlPath := s.sourceGenerationXMLPath(id, m.Generation)
	if _, err := os.Stat(xmlPath); err != nil {
		return sourceManifest{}, "", indexTag{}, false
	}
	s.mu.Lock()
	tag, ok := s.indexes[id]
	s.mu.Unlock()
	if !ok || tag.Generation != m.Generation || normalizeSourceURL(tag.SourceURL) != dbURL {
		idx, err := s.loadIndexFile(s.sourceGenerationIndexPath(id, m.Generation))
		if err != nil || normalizeSourceURL(idx.SourceURL) != dbURL || idx.Generation != m.Generation {
			// Rebuild index from XML if needed.
			doc, err := s.loadSourceCacheDocPath(xmlPath)
			if err != nil {
				return sourceManifest{}, "", indexTag{}, false
			}
			channels := indexFromDoc(doc)
			if err := s.writeGenerationIndex(id, m.Generation, dbURL, channels); err != nil {
				s.logger.Warn("epg index repair write failed", "source_id", id, "generation", m.Generation, "err", err)
			}
			tag = indexTag{SourceURL: dbURL, Generation: m.Generation, Channels: channels}
		} else {
			tag = indexTag{SourceURL: dbURL, Generation: m.Generation, Channels: idx.Channels}
		}
		s.mu.Lock()
		s.indexes[id] = tag
		s.mu.Unlock()
	}
	return m, xmlPath, tag, true
}

func (s *Service) loadSourceCacheDocPath(path string) (*tvDocument, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseXMLTV(f)
}

func (s *Service) deleteSourceFiles(id int64) {
	_ = os.Remove(s.sourceManifestPath(id))
	entries, _ := os.ReadDir(s.sourceDir)
	prefix := fmt.Sprintf("%d.", id)
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, prefix) && (strings.HasSuffix(name, ".xml") || strings.HasSuffix(name, ".json")) {
			_ = os.Remove(filepath.Join(s.sourceDir, name))
		}
	}
	entries, _ = os.ReadDir(s.indexDir)
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".json") {
			_ = os.Remove(filepath.Join(s.indexDir, name))
		}
	}
	s.mu.Lock()
	delete(s.indexes, id)
	s.mu.Unlock()
}

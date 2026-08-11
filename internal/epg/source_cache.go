package epg

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/jqjiang/tvr/internal/store"
)

func (s *Service) fetchSource(ctx context.Context, src store.EPGSource) (*tvDocument, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "tvr/1.0")
	req.Header.Set("Accept", "application/xml, text/xml, application/gzip, */*")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	limited := io.LimitReader(resp.Body, s.maxBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > s.maxBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", s.maxBytes)
	}
	reader, err := openMaybeGzip(raw)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	limitedDec := &io.LimitedReader{R: reader, N: s.maxBytes + 1}
	doc, err := parseXMLTV(limitedDec)
	if err != nil {
		return nil, err
	}
	if limitedDec.N == 0 {
		return nil, fmt.Errorf("decompressed response exceeds %d bytes", s.maxBytes)
	}
	return doc, nil
}

func openMaybeGzip(raw []byte) (io.ReadCloser, error) {
	if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		gr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		return gr, nil
	}
	return io.NopCloser(bytes.NewReader(raw)), nil
}

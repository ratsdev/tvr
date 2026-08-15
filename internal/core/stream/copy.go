package stream

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/ratsdev/tvr/internal/core/mpegts"
)

func (s *session) copyMPEGTS(ctx context.Context, body io.Reader, opts mpegTSCopyOptions) error {
	buf := make([]byte, mpegts.PacketSize*32)
	carry := make([]byte, 0, mpegts.PacketSize)
	probed := 0
	var total int64
	gotMedia := false

	for {
		select {
		case <-s.stopCh:
			return nil
		case <-ctx.Done():
			return nil
		default:
		}
		if s.viewerCount() == 0 {
			return nil
		}

		n, readErr := body.Read(buf)
		if n > 0 {
			if opts.maxBytes > 0 {
				total += int64(n)
				if total > opts.maxBytes {
					return fmt.Errorf("segment exceeds %d bytes", opts.maxBytes)
				}
			}
			data := append(carry, buf[:n]...)
			framed, rest := mpegts.ExtractFramed(data)
			carry = rest
			if len(framed) > 0 {
				s.broadcastFramed(framed)
				gotMedia = true
				s.setState("streaming", "")
				s.markReady()
			} else if !opts.segment && !s.everReady.Load() {
				probed += n
				if probed > maxProbeBytes {
					return fmt.Errorf("no mpeg-ts sync in probe window")
				}
			}
		}
		if readErr != nil {
			if s.stopped.Load() || errors.Is(readErr, context.Canceled) {
				return nil
			}
			if errors.Is(readErr, io.EOF) {
				if opts.segment {
					if !gotMedia {
						return fmt.Errorf("segment is not mpeg-ts")
					}
					return nil
				}
				if !s.everReady.Load() {
					return fmt.Errorf("upstream closed before mpeg-ts ready")
				}
				return fmt.Errorf("upstream closed")
			}
			return readErr
		}
	}
}

func (s *session) observeFramed(data []byte) {
	for i := 0; i+mpegts.PacketSize <= len(data); i += mpegts.PacketSize {
		s.observePacket(data[i : i+mpegts.PacketSize])
	}
}

// broadcastFramed observes PAT/PMT and queues data in live-sized bursts.
func (s *session) broadcastFramed(data []byte) {
	s.observeFramed(data)
	for len(data) >= mpegts.PacketSize {
		burst := mpegts.PacketSize * 32
		if burst > len(data) {
			burst = len(data) - (len(data) % mpegts.PacketSize)
		} else {
			burst = burst - (burst % mpegts.PacketSize)
		}
		if burst < mpegts.PacketSize {
			break
		}
		chunk := append([]byte(nil), data[:burst]...)
		data = data[burst:]
		s.broadcast(chunk)
	}
}

func (s *session) observePacket(pkt []byte) {
	if len(pkt) != mpegts.PacketSize || pkt[0] != mpegts.SyncByte {
		return
	}
	rai := mpegts.HasRandomAccess(pkt)
	if !mpegts.HasPUSI(pkt) && !rai {
		return
	}
	pid := mpegts.PID(pkt)

	s.mu.Lock()
	defer s.mu.Unlock()
	if rai {
		s.seenRAI = true
	}
	if !mpegts.HasPUSI(pkt) {
		return
	}
	if pid == patPID {
		s.pat = append([]byte(nil), pkt...)
		return
	}
	// Heuristic: treat packets with table_id 0x02 as PMT.
	payloadStart := 4
	if pkt[3]&0x20 != 0 { // adaptation field
		afLen := int(pkt[4])
		payloadStart = 5 + afLen
	}
	if payloadStart >= len(pkt) {
		return
	}
	// pointer_field
	pointer := int(pkt[payloadStart])
	i := payloadStart + 1 + pointer
	if i >= len(pkt) {
		return
	}
	if pkt[i] == 0x02 {
		s.pmts[pid] = append([]byte(nil), pkt...)
	}
}

func (s *session) startupPacketsLocked() []byte {
	if len(s.pat) == 0 && len(s.pmts) == 0 {
		return nil
	}
	out := make([]byte, 0, mpegts.PacketSize*(1+len(s.pmts)))
	if len(s.pat) == mpegts.PacketSize {
		out = append(out, s.pat...)
	}
	for _, pmt := range s.pmts {
		if len(pmt) == mpegts.PacketSize {
			out = append(out, pmt...)
		}
	}
	return out
}

func keyframeSuffix(data []byte) ([]byte, bool) {
	for i := 0; i+mpegts.PacketSize <= len(data); i += mpegts.PacketSize {
		if mpegts.HasRandomAccess(data[i : i+mpegts.PacketSize]) {
			return data[i:], true
		}
	}
	return nil, false
}

func (s *session) broadcast(pkt []byte) {
	s.mu.Lock()
	s.bytesSent += int64(len(pkt))
	var cancel context.CancelFunc
	for id, v := range s.viewers {
		out := pkt
		if v.waitKeyframe {
			cut, ok := keyframeSuffix(pkt)
			if !ok {
				continue
			}
			v.waitKeyframe = false
			if startup := s.startupPacketsLocked(); len(startup) > 0 {
				out = append(startup, cut...)
			} else {
				out = cut
			}
		}
		select {
		case v.ch <- out:
		default:
			// Slow client: drop them so they don't block the upstream.
			s.opts.Logger.Debug("dropping slow viewer", "channel_id", s.channelID, "viewer_id", id)
			if !v.closed.Swap(true) {
				close(v.ch)
			}
			delete(s.viewers, id)
		}
	}
	if len(s.viewers) == 0 {
		s.state = "idle"
		cancel = s.pumpCancel
		s.pumpCancel = nil
	}
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

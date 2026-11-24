package deck

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"time"
)

// oggPackets reads an Ogg Opus stream and calls fn once per Opus packet.
// One page can hold several packets and a packet can span pages (lacing value
// 255 = continues); sending whole pages as packets garbles the sound.
// The OpusHead and OpusTags header packets are skipped.
func oggPackets(r io.Reader, fn func([]byte) error) error {
	br := bufio.NewReader(r)
	hdr := make([]byte, 27)
	var pkt []byte
	for {
		if _, err := io.ReadFull(br, hdr); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if string(hdr[:4]) != "OggS" {
			return errors.New("ogg: lost sync")
		}
		lacing := make([]byte, hdr[26])
		if _, err := io.ReadFull(br, lacing); err != nil {
			return err
		}
		for _, l := range lacing {
			seg := make([]byte, l)
			if _, err := io.ReadFull(br, seg); err != nil {
				return err
			}
			pkt = append(pkt, seg...)
			if l == 255 {
				continue // packet goes on in the next segment (maybe the next page)
			}
			if !bytes.HasPrefix(pkt, []byte("OpusHead")) && !bytes.HasPrefix(pkt, []byte("OpusTags")) && len(pkt) > 0 {
				if err := fn(pkt); err != nil {
					return err
				}
			}
			pkt = nil
		}
	}
}

// opusDuration reads the play time of an Opus packet from its TOC byte (RFC 6716, 3.1).
func opusDuration(pkt []byte) time.Duration {
	if len(pkt) == 0 {
		return 20 * time.Millisecond
	}
	cfg := pkt[0] >> 3
	var frame time.Duration
	switch {
	case cfg < 12: // SILK: 10, 20, 40, 60 ms
		frame = []time.Duration{10, 20, 40, 60}[cfg%4] * time.Millisecond
	case cfg < 16: // hybrid: 10, 20 ms
		frame = []time.Duration{10, 20}[cfg%2] * time.Millisecond
	default: // CELT: 2.5, 5, 10, 20 ms
		frame = []time.Duration{2500, 5000, 10000, 20000}[cfg%4] * time.Microsecond
	}
	frames := 1
	switch pkt[0] & 3 {
	case 1, 2:
		frames = 2
	case 3:
		if len(pkt) < 2 {
			return 20 * time.Millisecond
		}
		frames = int(pkt[1] & 0x3f)
	}
	return frame * time.Duration(frames)
}

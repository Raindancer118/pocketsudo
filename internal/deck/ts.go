package deck

import (
	"bufio"
	"io"
)

// tsVideo turns an MPEG-TS stream (gpu-screen-recorder -c mpegts) into plain
// Annex-B H.264: find the video PID via PAT and PMT, strip the PES headers.
// Raw "-c h264" output would be simpler but lacks SPS/PPS (global header).
func tsVideo(r io.Reader) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(demuxTS(bufio.NewReaderSize(r, 64<<10), pw))
	}()
	return pr
}

func demuxTS(r *bufio.Reader, w io.Writer) error {
	pkt := make([]byte, 188)
	pmtPID, videoPID := -1, -1
	for {
		if _, err := io.ReadFull(r, pkt[:1]); err != nil {
			return eofIsFine(err)
		}
		if pkt[0] != 0x47 {
			continue // lost sync: look for the next sync byte
		}
		if _, err := io.ReadFull(r, pkt[1:]); err != nil {
			return eofIsFine(err)
		}
		pid := int(pkt[1]&0x1f)<<8 | int(pkt[2])
		start := pkt[1]&0x40 != 0
		afc := pkt[3] >> 4 & 3
		off := 4
		if afc&2 != 0 {
			off += 1 + int(pkt[4])
		}
		if afc&1 == 0 || off >= 188 {
			continue // no payload
		}
		payload := pkt[off:]
		switch {
		case pid == 0 && start:
			if p := patPMT(payload); p >= 0 {
				pmtPID = p
			}
		case pid == pmtPID && start:
			if v := pmtVideo(payload); v >= 0 {
				videoPID = v
			}
		case pid == videoPID && videoPID >= 0:
			if start {
				// PES header: 00 00 01 stream_id len(2) flags(2) header_len(1) …
				if len(payload) < 9 || payload[0] != 0 || payload[1] != 0 || payload[2] != 1 {
					continue
				}
				h := 9 + int(payload[8])
				if h > len(payload) {
					continue
				}
				payload = payload[h:]
			}
			if _, err := w.Write(payload); err != nil {
				return err
			}
		}
	}
}

func eofIsFine(err error) error {
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return nil
	}
	return err
}

// section returns a PSI section after its pointer field, bounded by section_length minus CRC.
func section(payload []byte) []byte {
	if len(payload) < 1 {
		return nil
	}
	p := 1 + int(payload[0])
	if p+3 > len(payload) {
		return nil
	}
	s := payload[p:]
	end := 3 + (int(s[1]&0x0f)<<8 | int(s[2])) - 4
	if end > len(s) || end < 8 {
		return nil
	}
	return s[:end]
}

// patPMT: PID of the first program's PMT.
func patPMT(payload []byte) int {
	s := section(payload)
	for i := 8; s != nil && i+4 <= len(s); i += 4 {
		if prog := int(s[i])<<8 | int(s[i+1]); prog != 0 {
			return int(s[i+2]&0x1f)<<8 | int(s[i+3])
		}
	}
	return -1
}

// pmtVideo: PID of the H.264 stream (type 0x1b).
func pmtVideo(payload []byte) int {
	s := section(payload)
	if s == nil || len(s) < 12 {
		return -1
	}
	for i := 12 + (int(s[10]&0x0f)<<8 | int(s[11])); i+5 <= len(s); {
		typ, pid := s[i], int(s[i+1]&0x1f)<<8|int(s[i+2])
		if typ == 0x1b {
			return pid
		}
		i += 5 + (int(s[i+3]&0x0f)<<8 | int(s[i+4]))
	}
	return -1
}

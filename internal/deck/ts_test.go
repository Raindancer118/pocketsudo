package deck

import (
	"bytes"
	"io"
	"testing"
)

// tsPacket builds one 188-byte transport packet, padded with an adaptation field.
func tsPacket(pid uint16, start bool, cc byte, payload []byte) []byte {
	p := make([]byte, 0, 188)
	b1 := byte(pid>>8) & 0x1f
	if start {
		b1 |= 0x40
	}
	p = append(p, 0x47, b1, byte(pid))
	free := 184 - len(payload)
	if free == 0 {
		return append(append(p, 0x10|cc&0x0f), payload...)
	}
	p = append(p, 0x30|cc&0x0f, byte(free-1))
	if free > 1 {
		p = append(p, 0x00)
		p = append(p, bytes.Repeat([]byte{0xff}, free-2)...)
	}
	return append(p, payload...)
}

// tsStream wraps an Annex-B access unit list into PAT, PMT and video PES packets.
func tsStream(units [][]byte) []byte {
	var out bytes.Buffer
	pat := []byte{0x00, 0x00, 0xb0, 0x0d, 0x00, 0x01, 0xc1, 0x00, 0x00, 0x00, 0x01, 0xf0, 0x00, 0, 0, 0, 0}
	out.Write(tsPacket(0, true, 0, pat))
	// PMT on 0x1000: one H.264 stream (type 0x1b) on PID 0x100
	pmt := []byte{0x00, 0x02, 0xb0, 0x12, 0x00, 0x01, 0xc1, 0x00, 0x00, 0xe1, 0x00, 0xf0, 0x00, 0x1b, 0xe1, 0x00, 0xf0, 0x00, 0, 0, 0, 0}
	out.Write(tsPacket(0x1000, true, 0, pmt))
	cc := byte(0)
	for _, u := range units {
		pes := append([]byte{0, 0, 1, 0xe0, 0, 0, 0x80, 0x80, 0x05, 0x21, 0, 1, 0, 1}, u...)
		for first := true; len(pes) > 0; first = false {
			n := min(len(pes), 184)
			out.Write(tsPacket(0x100, first, cc, pes[:n]))
			cc++
			pes = pes[n:]
		}
	}
	return out.Bytes()
}

func TestTSDemuxYieldsAnnexB(t *testing.T) {
	u1 := annexB(1)                                                               // SPS, PPS, IDR in one unit
	u2 := append([]byte{0, 0, 0, 1, 0x41, 0x9a}, bytes.Repeat([]byte{7}, 500)...) // a P slice spanning packets
	got, err := io.ReadAll(tsVideo(bytes.NewReader(tsStream([][]byte{u1, u2}))))
	if err != nil {
		t.Fatal(err)
	}
	if want := append(append([]byte{}, u1...), u2...); !bytes.Equal(got, want) {
		t.Fatalf("demuxed %d bytes, want %d", len(got), len(want))
	}
}

func TestTSDemuxSurvivesGarbage(t *testing.T) {
	// a cut stream or noise must not hang or panic, just end
	data := append(bytes.Repeat([]byte{0x47, 1, 2}, 100), tsStream([][]byte{annexB(1)})[:300]...)
	if _, err := io.ReadAll(tsVideo(bytes.NewReader(data))); err != nil && err != io.ErrUnexpectedEOF {
		t.Fatalf("garbage: %v", err)
	}
}

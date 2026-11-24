package deck

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
	"time"
)

// oggPage builds one Ogg page holding the given lacing values and body.
func oggPage(lacing []byte, body []byte, continued bool) []byte {
	var b bytes.Buffer
	b.WriteString("OggS")
	b.WriteByte(0)
	flags := byte(0)
	if continued {
		flags = 1
	}
	b.WriteByte(flags)
	binary.Write(&b, binary.LittleEndian, uint64(0)) // granule
	binary.Write(&b, binary.LittleEndian, uint32(1)) // serial
	binary.Write(&b, binary.LittleEndian, uint32(0)) // sequence
	binary.Write(&b, binary.LittleEndian, uint32(0)) // crc (not checked)
	b.WriteByte(byte(len(lacing)))
	b.Write(lacing)
	b.Write(body)
	return b.Bytes()
}

func TestOggPacketsSplitsPagesAndSkipsHeaders(t *testing.T) {
	head := append([]byte("OpusHead"), make([]byte, 11)...)
	tags := append([]byte("OpusTags"), make([]byte, 8)...)
	p1 := bytes.Repeat([]byte{1}, 100)
	p2 := bytes.Repeat([]byte{2}, 40)
	big := bytes.Repeat([]byte{3}, 300) // 255 + 45, split across two pages

	var stream []byte
	stream = append(stream, oggPage([]byte{byte(len(head))}, head, false)...)
	stream = append(stream, oggPage([]byte{byte(len(tags))}, tags, false)...)
	// two packets in one page, then the start of the big one
	stream = append(stream, oggPage([]byte{100, 40, 255}, append(append(append([]byte{}, p1...), p2...), big[:255]...), false)...)
	stream = append(stream, oggPage([]byte{45}, big[255:], true)...)

	var got [][]byte
	if err := oggPackets(bytes.NewReader(stream), func(p []byte) error {
		got = append(got, append([]byte{}, p...))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || !bytes.Equal(got[0], p1) || !bytes.Equal(got[1], p2) || !bytes.Equal(got[2], big) {
		t.Fatalf("packets: %d, sizes %v", len(got), func() []int {
			var s []int
			for _, g := range got {
				s = append(s, len(g))
			}
			return s
		}())
	}
}

func TestOpusDuration(t *testing.T) {
	cases := []struct {
		pkt  []byte
		want time.Duration
	}{
		{[]byte{31<<3 | 0}, 20 * time.Millisecond},    // CELT 20 ms, one frame (ffmpeg's default)
		{[]byte{29<<3 | 1}, 10 * time.Millisecond},    // CELT 5 ms, two frames
		{[]byte{1<<3 | 0}, 20 * time.Millisecond},     // SILK 20 ms
		{[]byte{3<<3 | 3, 3}, 180 * time.Millisecond}, // SILK 60 ms, code 3 with 3 frames
		{[]byte{16<<3 | 0}, 2500 * time.Microsecond},  // CELT 2.5 ms
		{[]byte{}, 20 * time.Millisecond},             // broken: assume the default
	}
	for _, c := range cases {
		if got := opusDuration(c.pkt); got != c.want {
			t.Errorf("toc %v: %v, want %v", c.pkt, got, c.want)
		}
	}
}

// fakeOpus is an endless Ogg Opus capture: headers, then pages with two 20 ms packets each.
type fakeOpus struct{ buf bytes.Buffer }

func newFakeOpus() *fakeOpus {
	f := &fakeOpus{}
	f.buf.Write(oggPage([]byte{19}, append([]byte("OpusHead"), make([]byte, 11)...), false))
	f.buf.Write(oggPage([]byte{16}, append([]byte("OpusTags"), make([]byte, 8)...), false))
	return f
}

func (f *fakeOpus) Read(p []byte) (int, error) {
	if f.buf.Len() == 0 {
		time.Sleep(40 * time.Millisecond)
		pkt := append([]byte{31<<3 | 4}, bytes.Repeat([]byte{7}, 59)...) // CELT 20 ms stereo, 60 bytes
		f.buf.Write(oggPage([]byte{60, 60}, append(append([]byte{}, pkt...), pkt...), false))
	}
	return f.buf.Read(p)
}

func (f *fakeOpus) Close() error { return nil }

func TestOpusStereo(t *testing.T) {
	with := "m=audio 9 UDP/TLS/RTP/SAVPF 111\r\na=rtpmap:111 opus/48000/2\r\na=fmtp:111 minptime=10;useinbandfec=1\r\na=sendonly\r\n"
	if got := opusStereo(with); !strings.Contains(got, "a=fmtp:111 minptime=10;useinbandfec=1;stereo=1;sprop-stereo=1\r\n") {
		t.Fatalf("existing fmtp:\n%q", got)
	}
	without := "m=audio 9 UDP/TLS/RTP/SAVPF 111\r\na=rtpmap:111 opus/48000/2\r\na=sendonly\r\n"
	if got := opusStereo(without); got != "m=audio 9 UDP/TLS/RTP/SAVPF 111\r\na=rtpmap:111 opus/48000/2\r\na=fmtp:111 stereo=1;sprop-stereo=1\r\na=sendonly\r\n" {
		t.Fatalf("new fmtp:\n%q", got)
	}
	if got := opusStereo(opusStereo(with)); strings.Count(got, ";stereo=1") != 1 {
		t.Fatal("stereo added twice")
	}
}

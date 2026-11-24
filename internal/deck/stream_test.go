package deck

import (
	"bytes"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

// annexB is a tiny fake H.264 stream: SPS, PPS and an IDR slice, repeated.
func annexB(frames int) []byte {
	var b bytes.Buffer
	for i := 0; i < frames; i++ {
		b.Write([]byte{0, 0, 0, 1, 0x67, 0x42, 0xe0, 0x1f, 0xda, 0x02, 0x80})
		b.Write([]byte{0, 0, 0, 1, 0x68, 0xce, 0x3c, 0x80})
		b.Write(append([]byte{0, 0, 0, 1, 0x65, 0x88, 0x84}, bytes.Repeat([]byte{0x11}, 200)...))
	}
	return b.Bytes()
}

// liveFeed delivers one fake frame every 33 ms, like a real capture device.
type liveFeed struct{ left int }

func (l *liveFeed) Read(p []byte) (int, error) {
	if l.left == 0 {
		return 0, io.EOF
	}
	l.left--
	time.Sleep(33 * time.Millisecond)
	return copy(p, annexB(1)), nil
}

func (l *liveFeed) Close() error { return nil }

// viewer is the phone side of a stream in tests.
func newViewer(t *testing.T) (*webrtc.PeerConnection, chan struct{}, *webrtc.DataChannel) {
	t.Helper()
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatal(err)
	}
	gotVideo := make(chan struct{}, 1)
	pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if _, _, err := tr.ReadRTP(); err == nil {
			select {
			case gotVideo <- struct{}{}:
			default:
			}
		}
	})
	dc, err := pc.CreateDataChannel("input", nil)
	if err != nil {
		t.Fatal(err)
	}
	offer, _ := pc.CreateOffer(nil)
	gather := webrtc.GatheringCompletePromise(pc)
	pc.SetLocalDescription(offer)
	<-gather
	return pc, gotVideo, dc
}

func TestScreenStream(t *testing.T) {
	r := newRig(t)
	r.agent.iceServers = nil // local test: host candidates only
	r.sys.pipe = func(name string, args ...string) io.ReadCloser { return &liveFeed{left: 300} }
	r.sys.out["hyprctl monitors -j"] = `[{"name":"DP-6","x":1920,"y":120,"width":1920,"height":1080,"scale":1},{"name":"eDP-1","x":5760,"y":300,"width":1920,"height":1080,"scale":1.2}]`

	pc, gotVideo, dc := newViewer(t)
	body := map[string]string{"offer": pc.LocalDescription().SDP, "source": "screen", "output": "eDP-1", "audio": "none"}
	if res := result(t, r.send("stream.start", body, false)); res.OK {
		t.Fatal("stream without a remote session")
	}
	result(t, r.send("session.open", nil, true))
	res := result(t, r.send("stream.start", body, false))
	if !res.OK {
		t.Fatalf("stream.start: %+v", res)
	}
	var ans struct {
		Answer        string
		Width, Height int
	}
	json.Unmarshal(res.Data, &ans)
	if ans.Width != 1600 || ans.Height != 900 {
		t.Fatalf("logical size of eDP-1 at scale 1.2 should be 1600x900, got %dx%d", ans.Width, ans.Height)
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: ans.Answer}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gotVideo:
	case <-time.After(10 * time.Second):
		t.Fatal("no video arrived")
	}
	if !r.sys.ran("wf-recorder") {
		t.Fatal("screen capture not started")
	}

	// a tap in the middle of the stream clicks the middle of eDP-1 (logical coords)
	opened := make(chan struct{})
	var once sync.Once
	dc.OnOpen(func() { once.Do(func() { close(opened) }) })
	if dc.ReadyState() == webrtc.DataChannelStateOpen {
		once.Do(func() { close(opened) })
	}
	select {
	case <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("input channel did not open")
	}
	dc.SendText(`{"t":"tap","x":0.5,"y":0.5}`)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !r.sys.ran("ydotool click 0xC0") {
		time.Sleep(50 * time.Millisecond)
	}
	if !r.sys.ran("hyprctl dispatch hl.dsp.cursor.move({ x = 6560, y = 750 })") || !r.sys.ran("ydotool click 0xC0") {
		t.Fatalf("tap not mapped to a click: %v", r.sys.calls)
	}

	if res := result(t, r.send("stream.stop", nil, false)); !res.OK {
		t.Fatalf("stop: %+v", res)
	}
}

func TestCameraNeedsFingerEvenWithSession(t *testing.T) {
	r := newRig(t)
	r.agent.iceServers = nil
	r.sys.pipe = func(name string, args ...string) io.ReadCloser {
		return io.NopCloser(bytes.NewReader(annexB(10)))
	}
	result(t, r.send("session.open", nil, true))
	pc, _, _ := newViewer(t)
	body := map[string]string{"offer": pc.LocalDescription().SDP, "source": "camera", "audio": "none"}
	if res := result(t, r.send("stream.start", body, false)); res.OK {
		t.Fatal("camera started without a fresh fingerprint")
	}
	body["source"], body["audio"] = "screen", "mic"
	if res := result(t, r.send("stream.start", body, false)); res.OK {
		t.Fatal("microphone started without a fresh fingerprint")
	}
	body["source"], body["audio"] = "camera", "none"
	if res := result(t, r.send("stream.start", body, true)); !res.OK {
		t.Fatalf("signed camera start: %+v", res)
	}
	if !r.sys.ran("notify-send") {
		t.Fatal("no visible camera indicator on the computer")
	}
	result(t, r.send("stream.stop", nil, false))
}

func TestMonitorsList(t *testing.T) {
	r := newRig(t)
	r.sys.out["hyprctl monitors -j"] = `[{"name":"eDP-1","x":5760,"y":300,"width":1920,"height":1080,"scale":1.2},{"name":"DP-8","x":0,"y":0,"width":1920,"height":1200,"scale":1}]`
	res := result(t, r.send("monitors.list", nil, false))
	var ms []monitor
	json.Unmarshal(res.Data, &ms)
	if !res.OK || len(ms) != 2 || ms[0].Name != "DP-8" {
		t.Fatalf("monitors left to right expected: %+v %+v", res, ms)
	}
}

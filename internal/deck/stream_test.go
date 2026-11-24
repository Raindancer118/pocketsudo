package deck

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/Raindancer118/pocketsudo/internal/host"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
	// -D: Hyprland sends no frame at all for a still screen otherwise, and the stream stays black
	if !r.sys.ran("wf-recorder -o eDP-1 -D") {
		t.Fatalf("screen capture not continuous: %v", r.sys.calls)
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
	r := cameraRig(t)
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

const v4l2Devices = `Integrated Camera: Integrated C (usb-0000:00:14.0-8):
	/dev/video0
	/dev/video1
	/dev/video2
	/dev/video3
	/dev/media0

USB 2.0 Camera: USB Camera (usb-0000:2f:00.0-2.3):
	/dev/video4
	/dev/video5
	/dev/media2
`

func cameraRig(t *testing.T) *rig {
	r := newRig(t)
	r.agent.iceServers = nil
	r.sys.out["v4l2-ctl --list-devices"] = v4l2Devices
	r.sys.out["v4l2-ctl -d /dev/video0 --list-formats"] = "[0]: 'MJPG' (Motion-JPEG, compressed)"
	r.sys.out["v4l2-ctl -d /dev/video2 --list-formats"] = "[0]: 'GREY' (8-bit Greyscale)"
	r.sys.out["v4l2-ctl -d /dev/video4 --list-formats"] = "[0]: 'MJPG' (Motion-JPEG, compressed)"
	r.sys.out["hyprctl monitors -j"] = `[{"name":"eDP-1","x":0,"y":0,"width":1920,"height":1080,"scale":1}]`
	r.sys.pipe = func(name string, args ...string) io.ReadCloser { return &liveFeed{left: 300} }
	return r
}

func TestCamerasList(t *testing.T) {
	r := cameraRig(t)
	res := result(t, r.send("cameras.list", nil, false))
	var cams []struct{ Device, Name string }
	json.Unmarshal(res.Data, &cams)
	if !res.OK || len(cams) != 2 || cams[0].Device != "/dev/video0" || cams[1].Device != "/dev/video4" || cams[1].Name != "USB 2.0 Camera" {
		t.Fatalf("cameras %+v %+v", res, cams)
	}
}

func TestSwitchSourceDuringStream(t *testing.T) {
	r := cameraRig(t)
	result(t, r.send("session.open", nil, true))
	pc, gotVideo, _ := newViewer(t)
	res := result(t, r.send("stream.start", map[string]string{"offer": pc.LocalDescription().SDP, "source": "screen", "output": "eDP-1", "audio": "none"}, false))
	var ans struct{ Answer string }
	json.Unmarshal(res.Data, &ans)
	pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: ans.Answer})
	select {
	case <-gotVideo:
	case <-time.After(10 * time.Second):
		t.Fatal("no video")
	}
	toCam := map[string]string{"source": "camera", "device": "/dev/video4"}
	if res := result(t, r.send("stream.switch", toCam, false)); res.OK {
		t.Fatal("switched to a camera without a fresh fingerprint")
	}
	if res := result(t, r.send("stream.switch", toCam, true)); !res.OK {
		t.Fatalf("signed switch: %+v", res)
	}
	if !r.sys.ran("ffmpeg -hide_banner -loglevel error -f v4l2 -input_format mjpeg -framerate 30 -video_size 1280x720 -i /dev/video4") {
		t.Fatalf("USB camera not used: %v", r.sys.calls)
	}
	if res := result(t, r.send("stream.switch", map[string]string{"source": "screen", "output": "eDP-1"}, false)); !res.OK {
		t.Fatalf("back to the screen: %+v", res)
	}
	if res := result(t, r.send("stream.switch", map[string]string{"source": "camera", "device": "/etc/passwd"}, true)); res.OK {
		t.Fatal("accepted a non-camera device")
	}
}

func TestSystemAudioArrivesAsOpusPackets(t *testing.T) {
	r := newRig(t)
	r.agent.iceServers = nil
	r.sys.out["hyprctl monitors -j"] = `[{"name":"eDP-1","x":0,"y":0,"width":1920,"height":1080,"scale":1}]`
	r.sys.pipe = func(name string, args ...string) io.ReadCloser {
		if name == "ffmpeg" {
			return newFakeOpus()
		}
		return &liveFeed{left: 300}
	}
	result(t, r.send("session.open", nil, true))

	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
	pc.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
	sizes := make(chan int, 16)
	pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if tr.Kind() != webrtc.RTPCodecTypeAudio {
			return
		}
		for {
			p, _, err := tr.ReadRTP()
			if err != nil {
				return
			}
			select {
			case sizes <- len(p.Payload):
			default:
			}
		}
	})
	pc.CreateDataChannel("input", nil)
	offer, _ := pc.CreateOffer(nil)
	gather := webrtc.GatheringCompletePromise(pc)
	pc.SetLocalDescription(offer)
	<-gather

	res := result(t, r.send("stream.start", map[string]string{"offer": pc.LocalDescription().SDP, "source": "screen", "output": "eDP-1", "audio": "system"}, false))
	if !res.OK {
		t.Fatalf("stream.start: %+v", res)
	}
	var ans struct{ Answer string }
	json.Unmarshal(res.Data, &ans)
	if !strings.Contains(ans.Answer, "stereo=1") {
		t.Fatalf("answer does not offer stereo:\n%s", ans.Answer)
	}
	pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: ans.Answer})
	// every RTP payload is exactly one Opus packet (60 bytes), never a whole page or a header
	for i := 0; i < 4; i++ {
		select {
		case n := <-sizes:
			if n != 60 {
				t.Fatalf("audio payload %d bytes, want one 60 byte Opus packet", n)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("no audio arrived")
		}
	}
	result(t, r.send("stream.stop", nil, false))
}

// The phone brings TURN; one relayed side is enough (TURN permissions are per IP),
// and every agent allocation per network interface ate into coturn's quota.
func TestAgentUsesStunOnly(t *testing.T) {
	// a relay that does hand out TURN credentials
	turn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"urls":["turn:turn.example:3478?transport=udp"],"username":"1:abc","credential":"x","ttl":3600}`))
	}))
	defer turn.Close()
	// an agent of its own: swapping the client of a running one races with its poll loop
	a := &Agent{client: host.NewClient(turn.URL, ""), iceServers: defaultICE()}
	for _, s := range a.iceConfig(context.Background()) {
		for _, u := range s.URLs {
			if strings.HasPrefix(u, "turn") {
				t.Fatalf("agent gathers TURN: %v", u)
			}
		}
	}
}

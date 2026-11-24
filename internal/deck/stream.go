package deck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/pion/webrtc/v4/pkg/media/h264reader"
	"github.com/pion/webrtc/v4/pkg/media/oggreader"
)

// Screen, camera and sound go straight to the phone over WebRTC. The deck
// channel only carries the (sealed) offer and answer; touch input comes back
// over a data channel.

func defaultICE() []webrtc.ICEServer {
	return []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}}
}

type streamer struct {
	pc     *webrtc.PeerConnection
	cancel context.CancelFunc
	mon    monitor
	notice bool // camera or microphone indicator shown
}

type monitor struct {
	Name   string  `json:"name"`
	X      int     `json:"x"`
	Y      int     `json:"y"`
	Width  int     `json:"width"`
	Height int     `json:"height"`
	Scale  float64 `json:"scale"`
}

// logical size in Hyprland's layout coordinates
func (m monitor) size() (int, int) {
	s := m.Scale
	if s <= 0 {
		s = 1
	}
	return int(math.Round(float64(m.Width) / s)), int(math.Round(float64(m.Height) / s))
}

func (a *Agent) monitors(ctx context.Context) []monitor {
	var ms []monitor
	if out, err := a.sys.Run(ctx, "hyprctl", "monitors", "-j"); err == nil {
		json.Unmarshal([]byte(out), &ms)
	}
	return ms
}

type streamReq struct {
	Offer  string
	Source string // "screen" or "camera"
	Output string // monitor name for "screen"
	Audio  string // "none", "system" or "mic"
}

func (a *Agent) streamStart(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	var req streamReq
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	private := req.Source == "camera" || req.Audio == "mic"
	if private && !isAuthed(ctx) {
		// camera and microphone need a fresh finger every time, a session is not enough
		return nil, errNeedsFinger
	}
	if req.Offer == "" {
		return nil, errors.New("missing offer")
	}
	a.stopStream()

	var mon monitor
	if req.Source != "camera" {
		for _, m := range a.monitors(ctx) {
			if m.Name == req.Output || req.Output == "" {
				mon = m
				break
			}
		}
		if mon.Name == "" {
			return nil, fmt.Errorf("no monitor %q", req.Output)
		}
	}

	me := &webrtc.MediaEngine{}
	if err := me.RegisterDefaultCodecs(); err != nil {
		return nil, err
	}
	pc, err := webrtc.NewAPI(webrtc.WithMediaEngine(me)).NewPeerConnection(webrtc.Configuration{ICEServers: a.iceServers})
	if err != nil {
		return nil, err
	}
	video, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{
		MimeType: webrtc.MimeTypeH264, ClockRate: 90000,
		SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"}, "video", "pocketdeck")
	if err != nil {
		pc.Close()
		return nil, err
	}
	if _, err := pc.AddTrack(video); err != nil {
		pc.Close()
		return nil, err
	}
	var audio *webrtc.TrackLocalStaticSample
	if req.Audio == "system" || req.Audio == "mic" {
		audio, err = webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "audio", "pocketdeck")
		if err == nil {
			_, err = pc.AddTrack(audio)
		}
		if err != nil {
			pc.Close()
			return nil, err
		}
	}

	sctx, cancel := context.WithCancel(context.Background())
	st := &streamer{pc: pc, cancel: cancel, mon: mon, notice: private}
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		if dc.Label() != "input" {
			return
		}
		dc.OnMessage(func(msg webrtc.DataChannelMessage) { a.input(sctx, st.mon, msg.Data) })
	})
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateClosed || s == webrtc.PeerConnectionStateDisconnected {
			a.endStream(st)
		}
	})

	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: req.Offer}); err != nil {
		cancel()
		pc.Close()
		return nil, fmt.Errorf("offer: %w", err)
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		cancel()
		pc.Close()
		return nil, err
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		cancel()
		pc.Close()
		return nil, err
	}
	select {
	case <-gathered:
	case <-time.After(8 * time.Second): // send what we have; host candidates come first
	}

	a.mu.Lock()
	a.stream = st
	a.mu.Unlock()
	if private {
		what := "Kamera"
		if req.Source != "camera" {
			what = "Mikrofon"
		} else if req.Audio == "mic" {
			what = "Kamera und Mikrofon"
		}
		a.sys.Start(ctx, "notify-send", "-a", "PocketDeck", "-u", "critical", "-t", "0",
			"PocketDeck: "+what+" aktiv", "Wird gerade an "+a.cfg.DeviceName+" übertragen.")
	}

	go a.pumpVideo(sctx, video, req, mon)
	if audio != nil {
		go a.pumpAudio(sctx, audio, req.Audio)
	}
	w, h := mon.size()
	if req.Source == "camera" {
		w, h = 1280, 720
	}
	return ok(map[string]any{"answer": pc.LocalDescription().SDP, "width": w, "height": h})
}

func (a *Agent) stopStream() {
	a.mu.Lock()
	st := a.stream
	a.mu.Unlock()
	if st != nil {
		a.endStream(st)
	}
}

var endOnce sync.Mutex

func (a *Agent) endStream(st *streamer) {
	endOnce.Lock()
	defer endOnce.Unlock()
	a.mu.Lock()
	if a.stream == st {
		a.stream = nil
	}
	a.mu.Unlock()
	st.cancel()
	st.pc.Close()
	if st.notice {
		st.notice = false
		a.sys.Start(context.Background(), "notify-send", "-a", "PocketDeck", "PocketDeck", "Übertragung beendet.")
	}
}

func (a *Agent) streamStop(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	a.stopStream()
	return nil, nil
}

// pumpVideo feeds H.264 from wf-recorder (screen) or ffmpeg (camera) into the track.
func (a *Agent) pumpVideo(ctx context.Context, track *webrtc.TrackLocalStaticSample, req streamReq, mon monitor) {
	var rc io.ReadCloser
	var err error
	if req.Source == "camera" {
		rc, err = a.sys.Pipe(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error",
			"-f", "v4l2", "-framerate", "30", "-video_size", "1280x720", "-i", "/dev/video0",
			"-vaapi_device", "/dev/dri/renderD128", "-vf", "format=nv12,hwupload",
			"-c:v", "h264_vaapi", "-profile:v", "constrained_baseline", "-g", "30", "-bf", "0", "-f", "h264", "-")
	} else {
		rc, err = a.sys.Pipe(ctx, "wf-recorder", "-o", mon.Name, "-m", "h264", "-c", "h264_vaapi",
			"-d", "/dev/dri/renderD128", "-r", "30", "-p", "profile=constrained_baseline", "-p", "g=30", "-p", "bf=0",
			"-p", "b=3M", "-p", "maxrate=4M", "-p", "rc_mode=VBR",
			"-f", "/dev/stdout", "-y")
	}
	if err != nil {
		a.log.Warn("video capture", "err", err)
		return
	}
	defer rc.Close()
	reader, err := h264reader.NewReader(rc)
	if err != nil {
		return
	}
	frame := time.Second / 30
	for ctx.Err() == nil {
		nal, err := reader.NextNAL()
		if err != nil {
			return
		}
		if err := track.WriteSample(media.Sample{Data: nal.Data, Duration: frame}); err != nil {
			return
		}
	}
}

// pumpAudio sends Opus from PipeWire: what the computer plays, or its microphone.
func (a *Agent) pumpAudio(ctx context.Context, track *webrtc.TrackLocalStaticSample, which string) {
	src := "@DEFAULT_MONITOR@"
	if which == "mic" {
		src = "default"
	}
	rc, err := a.sys.Pipe(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "pulse", "-i", src,
		"-c:a", "libopus", "-b:a", "96k", "-page_duration", "20000", "-f", "ogg", "-")
	if err != nil {
		a.log.Warn("audio capture", "err", err)
		return
	}
	defer rc.Close()
	ogg, _, err := oggreader.NewWith(rc)
	if err != nil {
		return
	}
	var last uint64
	for ctx.Err() == nil {
		page, hdr, err := ogg.ParseNextPage()
		if err != nil {
			return
		}
		samples := hdr.GranulePosition - last
		last = hdr.GranulePosition
		if samples == 0 || samples > 48000 {
			samples = 960
		}
		if err := track.WriteSample(media.Sample{Data: page, Duration: time.Duration(samples) * time.Second / 48000}); err != nil {
			return
		}
	}
}

// ---- touch input from the phone ----

type inputMsg struct {
	T      string  `json:"t"` // tap, right, move, down, up, scroll
	X, Y   float64 // 0..1 within the streamed monitor
	DY     float64 `json:"dy"`
	Button string  `json:"button"`
}

func (a *Agent) input(ctx context.Context, mon monitor, raw []byte) {
	var m inputMsg
	if json.Unmarshal(raw, &m) != nil || mon.Name == "" {
		return
	}
	w, h := mon.size()
	x := mon.X + int(math.Round(clamp01(m.X)*float64(w)))
	y := mon.Y + int(math.Round(clamp01(m.Y)*float64(h)))
	move := func() {
		out, err := a.sys.Run(ctx, "hyprctl", "dispatch", fmt.Sprintf("hl.dsp.cursor.move({ x = %d, y = %d })", x, y))
		if err != nil || strings.Contains(strings.ToLower(out), "error") {
			a.sys.Run(ctx, "hyprctl", "dispatch", "movecursor", fmt.Sprint(x), fmt.Sprint(y))
		}
	}
	switch m.T {
	case "move":
		move()
	case "tap":
		move()
		a.sys.Run(ctx, "ydotool", "click", "0xC0")
	case "right":
		move()
		a.sys.Run(ctx, "ydotool", "click", "0xC1")
	case "down":
		move()
		a.sys.Run(ctx, "ydotool", "click", "0x40")
	case "up":
		move()
		a.sys.Run(ctx, "ydotool", "click", "0x80")
	case "scroll":
		steps := int(math.Round(m.DY))
		if steps != 0 {
			a.sys.Run(ctx, "ydotool", "mousemove", "--wheel", "-x", "0", "-y", fmt.Sprint(-steps))
		}
	}
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

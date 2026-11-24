package deck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Raindancer118/pocketsudo/protocol"
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

// iceConfig adds the relay's TURN server (short-lived credentials) to STUN,
// so mobile networks that block direct connections still work.
func (a *Agent) iceConfig(ctx context.Context) []webrtc.ICEServer {
	servers := append([]webrtc.ICEServer{}, a.iceServers...)
	var c protocol.TurnCreds
	tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := a.client.Do(tctx, "GET", "/v1/channels/"+a.cfg.Channel+"/turn", nil, &c); err == nil && len(c.URLs) > 0 {
		servers = append(servers, webrtc.ICEServer{URLs: c.URLs, Username: c.Username, Credential: c.Credential})
	}
	return servers
}

type streamer struct {
	pc       *webrtc.PeerConnection
	ctx      context.Context
	cancel   context.CancelFunc
	video    *webrtc.TrackLocalStaticSample
	pumpStop context.CancelFunc // current video source; replaced on switch
	mon      monitor            // empty while a camera is shown (no touch input)
	camera   bool
	mic      bool
	notice   bool // camera or microphone indicator shown
	mu       sync.Mutex
}

type camera struct {
	Device string `json:"device"`
	Name   string `json:"name"`
}

// cameras lists capture devices with a colour format (skips IR and metadata nodes).
func (a *Agent) cameras(ctx context.Context) []camera {
	out, err := a.sys.Run(ctx, "v4l2-ctl", "--list-devices")
	if err != nil {
		return nil
	}
	var cams []camera
	name, found := "", false
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, " ") {
			name, found = strings.TrimSpace(strings.SplitN(line, ":", 2)[0]), false
			continue
		}
		dev := strings.TrimSpace(line)
		if found || !strings.HasPrefix(dev, "/dev/video") {
			continue
		}
		f, err := a.sys.Run(ctx, "v4l2-ctl", "-d", dev, "--list-formats")
		if err == nil && (strings.Contains(f, "MJPG") || strings.Contains(f, "YUYV")) {
			cams = append(cams, camera{Device: dev, Name: name})
			found = true
		}
	}
	return cams
}

func (a *Agent) camerasList(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	cams := a.cameras(ctx)
	if cams == nil {
		cams = []camera{}
	}
	return ok(cams)
}

// resolveSource checks the requested picture source and returns its monitor or camera device.
func (a *Agent) resolveSource(ctx context.Context, source, output, device string) (monitor, string, error) {
	if source == "camera" {
		cams := a.cameras(ctx)
		for _, c := range cams {
			if c.Device == device || device == "" {
				return monitor{}, c.Device, nil
			}
		}
		return monitor{}, "", fmt.Errorf("no camera %q", device)
	}
	for _, m := range a.monitors(ctx) {
		if m.Name == output || output == "" {
			return m, "", nil
		}
	}
	return monitor{}, "", fmt.Errorf("no monitor %q", output)
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

func (a *Agent) monitorsList(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	ms := a.monitors(ctx)
	sort.Slice(ms, func(i, j int) bool { return ms[i].X < ms[j].X })
	return ok(ms)
}

type streamReq struct {
	Offer  string
	Source string // "screen" or "camera"
	Output string // monitor name for "screen"
	Device string // camera device for "camera" (default: first camera)
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

	mon, device, err := a.resolveSource(ctx, req.Source, req.Output, req.Device)
	if err != nil {
		return nil, err
	}

	me := &webrtc.MediaEngine{}
	if err := me.RegisterDefaultCodecs(); err != nil {
		return nil, err
	}
	pc, err := webrtc.NewAPI(webrtc.WithMediaEngine(me)).NewPeerConnection(webrtc.Configuration{ICEServers: a.iceConfig(ctx)})
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
	st := &streamer{pc: pc, ctx: sctx, cancel: cancel, video: video, mon: mon,
		camera: req.Source == "camera", mic: req.Audio == "mic", notice: private}
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		if dc.Label() != "input" {
			return
		}
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			st.mu.Lock()
			mon := st.mon
			st.mu.Unlock()
			a.input(sctx, mon, msg.Data)
		})
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

	a.switchVideo(st, mon, device)
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

// switchVideo replaces the picture source of a running stream; the WebRTC
// connection stays up and the decoder picks up the new SPS/PPS.
func (a *Agent) switchVideo(st *streamer, mon monitor, device string) {
	st.mu.Lock()
	if st.pumpStop != nil {
		st.pumpStop()
	}
	pctx, stop := context.WithCancel(st.ctx)
	st.pumpStop = stop
	st.mon = mon
	st.camera = device != ""
	st.mu.Unlock()
	go a.pumpVideo(pctx, st.video, mon, device)
}

func (a *Agent) streamSwitch(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	var req streamReq
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	if req.Source == "camera" && !isAuthed(ctx) {
		return nil, errNeedsFinger
	}
	a.mu.Lock()
	st := a.stream
	a.mu.Unlock()
	if st == nil {
		return nil, errors.New("no stream running")
	}
	mon, device, err := a.resolveSource(ctx, req.Source, req.Output, req.Device)
	if err != nil {
		return nil, err
	}
	st.mu.Lock()
	wasCamera := st.camera
	st.mu.Unlock()
	a.switchVideo(st, mon, device)
	if device != "" && !wasCamera {
		st.notice = true
		a.sys.Start(ctx, "notify-send", "-a", "PocketDeck", "-u", "critical", "-t", "0",
			"PocketDeck: Kamera aktiv", "Wird gerade an "+a.cfg.DeviceName+" übertragen.")
	} else if device == "" && wasCamera && !st.mic {
		st.notice = false
		a.sys.Start(ctx, "notify-send", "-a", "PocketDeck", "PocketDeck", "Kamera aus.")
	}
	w, h := mon.size()
	if device != "" {
		w, h = 1280, 720
	}
	return ok(map[string]any{"width": w, "height": h})
}

// pumpVideo feeds H.264 from wf-recorder (screen) or ffmpeg (camera) into the track.
func (a *Agent) pumpVideo(ctx context.Context, track *webrtc.TrackLocalStaticSample, mon monitor, device string) {
	var rc io.ReadCloser
	var err error
	if device != "" {
		rc, err = a.sys.Pipe(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error",
			"-f", "v4l2", "-input_format", "mjpeg", "-framerate", "30", "-video_size", "1280x720", "-i", device,
			"-vaapi_device", "/dev/dri/renderD128", "-vf", "format=nv12,hwupload",
			"-c:v", "h264_vaapi", "-profile:v", "constrained_baseline", "-g", "30", "-bf", "0", "-f", "h264", "-")
	} else {
		// -D: without it a still screen yields no frame at all (stream stays black). A still
		// frame then costs ~20 bytes; the keyframes dominate, hence one every 5 s (~1 Mbit/s idle).
		rc, err = a.sys.Pipe(ctx, "wf-recorder", "-o", mon.Name, "-D", "-m", "h264", "-c", "h264_vaapi",
			"-d", "/dev/dri/renderD128", "-r", "30", "-p", "profile=constrained_baseline", "-p", "g=150", "-p", "bf=0",
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

package deck

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/pion/webrtc/v4/pkg/media/h264reader"
)

// Screen, camera and sound go straight to the phone over WebRTC. The deck
// channel only carries the (sealed) offer and answer; touch input comes back
// over a data channel.

func defaultICE() []webrtc.ICEServer {
	return []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}}
}

// iceConfig adds the relay's TURN server (short-lived credentials) to STUN,
// so mobile networks that block direct connections still work.
// iceConfig: STUN only. The phone brings TURN; one relayed side is enough
// (TURN permissions are per IP), and agent allocations, one per network
// interface, used up coturn's quota.
func (a *Agent) iceConfig(_ context.Context) []webrtc.ICEServer {
	return append([]webrtc.ICEServer{}, a.iceServers...)
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
	t0 := time.Now()
	ice := a.iceConfig(ctx)
	pc, err := webrtc.NewAPI(webrtc.WithMediaEngine(me)).NewPeerConnection(webrtc.Configuration{ICEServers: ice})
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
		// stereo: music and videos, not a phone call
		audio, err = webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus,
			ClockRate: 48000, Channels: 2, SDPFmtpLine: "minptime=10;useinbandfec=1;stereo=1;sprop-stereo=1"}, "audio", "pocketdeck")
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
	tGather := time.Since(t0)

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
	a.log.Info("stream answer", "gathered", tGather.Round(time.Millisecond),
		"total", time.Since(t0).Round(time.Millisecond))
	return ok(map[string]any{"answer": opusStereo(pc.LocalDescription().SDP), "width": w, "height": h})
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
// encoder is one way to produce H.264 for a stream.
type encoder struct {
	name string // vaapi, nvenc, x264
	cmd  []string
}

func (a *Agent) pumpVideo(ctx context.Context, track *webrtc.TrackLocalStaticSample, mon monitor, device string) {
	var kind string
	var vaapi, nvenc, x264 encoder
	if device != "" {
		kind = "camera"
		in := []string{"ffmpeg", "-hide_banner", "-loglevel", "error",
			"-f", "v4l2", "-input_format", "mjpeg", "-framerate", "30", "-video_size", "1280x720", "-i", device}
		vaapi = encoder{"vaapi", append(slices.Clone(in), "-vaapi_device", "/dev/dri/renderD128", "-vf", "format=nv12,hwupload",
			"-c:v", "h264_vaapi", "-profile:v", "constrained_baseline", "-g", "30", "-bf", "0", "-f", "h264", "-")}
		nvenc = encoder{"nvenc", append(slices.Clone(in), "-c:v", "h264_nvenc", "-preset", "p1", "-tune", "ull",
			"-profile:v", "baseline", "-zerolatency", "1", "-g", "30", "-bf", "0", "-f", "h264", "-")}
		x264 = encoder{"x264", append(slices.Clone(in), "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
			"-profile:v", "baseline", "-pix_fmt", "yuv420p", "-g", "30", "-bf", "0", "-f", "h264", "-")}
	} else {
		kind = "screen"
		// -D: without it a still screen yields no frame at all (stream stays black). A still
		// frame then costs ~20 bytes; the keyframes dominate, hence one every 5 s (~1 Mbit/s idle).
		base := []string{"wf-recorder", "-o", mon.Name, "-D", "-m", "h264"}
		tail := []string{"-r", "30", "-p", "g=150", "-p", "bf=0", "-p", "b=3M", "-f", "/dev/stdout", "-y"}
		vaapi = encoder{"vaapi", slices.Concat(base, []string{"-c", "h264_vaapi", "-d", "/dev/dri/renderD128",
			"-p", "profile=constrained_baseline", "-p", "maxrate=4M", "-p", "rc_mode=VBR"}, tail)}
		nvenc = encoder{"nvenc", slices.Concat(base, []string{"-c", "h264_nvenc", "-p", "preset=p1", "-p", "tune=ull",
			"-p", "profile=baseline", "-p", "zerolatency=1"}, tail)}
		x264 = encoder{"x264", slices.Concat(base, []string{"-c", "libx264", "-p", "preset=ultrafast", "-p", "tune=zerolatency",
			"-p", "profile=baseline"}, tail)}
	}
	// NVENC is free while the NVIDIA GPU is awake anyway (compositor, vela …) but
	// would wake it on battery otherwise; software encoding is the last resort (~1 core).
	cands := []encoder{vaapi, nvenc, x264}
	if a.gpuAwake() {
		cands = []encoder{nvenc, vaapi, x264}
	}
	rc := a.openEncoder(ctx, kind, cands)
	if rc == nil {
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

// openEncoder starts the first encoder that delivers data. A driver or ffmpeg
// update can break one (VAAPI with ffmpeg 9.1, Nov 2026); encoders that failed
// are skipped for later streams until the agent restarts.
func (a *Agent) openEncoder(ctx context.Context, kind string, cands []encoder) io.ReadCloser {
	for _, e := range cands {
		key := kind + ":" + e.name
		a.mu.Lock()
		bad := a.badEncoders[key]
		a.mu.Unlock()
		if bad {
			continue
		}
		ectx, cancel := context.WithCancel(ctx)
		rc, err := a.sys.Pipe(ectx, e.cmd[0], e.cmd[1:]...)
		if err == nil {
			br := bufio.NewReader(rc)
			peeked := make(chan error, 1)
			go func() {
				_, err := br.Peek(1)
				peeked <- err
			}()
			select {
			case err = <-peeked:
			case <-time.After(a.encoderProbe):
				err = errors.New("no frame in time")
			}
			if err == nil {
				return &encoderPipe{Reader: br, rc: rc, cancel: cancel}
			}
			rc.Close()
		}
		cancel()
		if ctx.Err() != nil {
			return nil
		}
		a.log.Warn("video encoder failed, trying the next", "kind", kind, "encoder", e.name, "err", err)
		a.mu.Lock()
		a.badEncoders[key] = true
		a.mu.Unlock()
	}
	a.log.Warn("no video encoder works", "kind", kind)
	return nil
}

type encoderPipe struct {
	io.Reader
	rc     io.ReadCloser
	cancel context.CancelFunc
}

func (e *encoderPipe) Close() error {
	err := e.rc.Close()
	e.cancel()
	return err
}

// pumpAudio sends Opus from PipeWire: what the computer plays, or its microphone.
func (a *Agent) pumpAudio(ctx context.Context, track *webrtc.TrackLocalStaticSample, which string) {
	src := "@DEFAULT_MONITOR@"
	if which == "mic" {
		src = "default"
	}
	// 48 kHz stereo, 20 ms frames, music mode; short pages keep the delay low
	rc, err := a.sys.Pipe(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-fflags", "nobuffer",
		"-f", "pulse", "-i", src, "-ar", "48000", "-ac", "2",
		"-c:a", "libopus", "-b:a", "128k", "-application", "audio", "-frame_duration", "20",
		"-page_duration", "20000", "-f", "ogg", "-")
	if err != nil {
		a.log.Warn("audio capture", "err", err)
		return
	}
	defer rc.Close()
	err = oggPackets(rc, func(pkt []byte) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return track.WriteSample(media.Sample{Data: pkt, Duration: opusDuration(pkt)})
	})
	if err != nil && ctx.Err() == nil {
		a.log.Warn("audio stream", "err", err)
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

var opusRtpmap = regexp.MustCompile(`(?m)^a=rtpmap:(\d+) opus/48000/2`)

// opusStereo marks the Opus codec as stereo in an SDP. pion answers with its
// registry's mono-default fmtp; without stereo=1 Android plays the sound as a
// downmixed voice call.
func opusStereo(sdp string) string {
	m := opusRtpmap.FindStringSubmatch(sdp)
	if m == nil {
		return sdp
	}
	fmtp := regexp.MustCompile(`(?m)^(a=fmtp:` + m[1] + ` [^\r\n]*)`)
	if loc := fmtp.FindStringIndex(sdp); loc != nil {
		line := sdp[loc[0]:loc[1]]
		if strings.Contains(line, "stereo=1") {
			return sdp
		}
		return sdp[:loc[1]] + ";stereo=1;sprop-stereo=1" + sdp[loc[1]:]
	}
	i := strings.Index(sdp, m[0]) + len(m[0])
	return sdp[:i] + "\r\na=fmtp:" + m[1] + " stereo=1;sprop-stereo=1" + sdp[i:]
}

// nvidiaAwake: is an NVIDIA GPU powered up right now (runtime PM "active")?
func nvidiaAwake() bool {
	devs, _ := filepath.Glob("/sys/bus/pci/devices/*")
	for _, d := range devs {
		vendor, _ := os.ReadFile(filepath.Join(d, "vendor"))
		class, _ := os.ReadFile(filepath.Join(d, "class"))
		if strings.TrimSpace(string(vendor)) != "0x10de" || !strings.HasPrefix(string(class), "0x03") {
			continue
		}
		st, _ := os.ReadFile(filepath.Join(d, "power", "runtime_status"))
		if strings.TrimSpace(string(st)) == "active" {
			return true
		}
	}
	return false
}

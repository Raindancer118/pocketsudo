package de.raindancer118.pocketdeck;

import android.content.Intent;
import android.os.Bundle;
import android.view.GestureDetector;
import android.view.MotionEvent;
import android.view.View;
import android.view.WindowInsets;
import android.view.WindowInsetsController;
import android.widget.LinearLayout;
import android.widget.TextView;

import androidx.annotation.NonNull;
import androidx.appcompat.app.AppCompatActivity;

import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;
import org.webrtc.DataChannel;
import org.webrtc.DefaultVideoDecoderFactory;
import org.webrtc.DefaultVideoEncoderFactory;
import org.webrtc.EglBase;
import org.webrtc.IceCandidate;
import org.webrtc.MediaConstraints;
import org.webrtc.MediaStream;
import org.webrtc.MediaStreamTrack;
import org.webrtc.PeerConnection;
import org.webrtc.PeerConnectionFactory;
import org.webrtc.RendererCommon;
import org.webrtc.RtpReceiver;
import org.webrtc.RtpTransceiver;
import org.webrtc.SdpObserver;
import org.webrtc.SessionDescription;
import org.webrtc.SurfaceViewRenderer;
import org.webrtc.VideoTrack;

import java.nio.ByteBuffer;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.List;

import de.raindancer118.pocketcore.DeckCrypto;

/** Live screen or webcam of the computer; touch becomes mouse input. */
public class StreamActivity extends AppCompatActivity {
    private static final long MOVE_EVERY_MS = 30;

    private Deck deck;
    private EglBase egl;
    private PeerConnectionFactory factory;
    private PeerConnection pc;
    private DataChannel input;
    private SurfaceViewRenderer video;
    private View picker, bar;
    private TextView state, pickerMessage;
    private final List<TextView> sourceViews = new ArrayList<>(), audioViews = new ArrayList<>();
    private String source = "", audio = "none";
    /** id → [kind ("screen"/"camera"), output or device, label] */
    private final java.util.LinkedHashMap<String, String[]> sources = new java.util.LinkedHashMap<>();
    private int frameW, frameH;
    private long lastMove;
    private float scrollAcc;
    private static final int MAX_RECONNECTS = 3;
    private int reconnects;
    private boolean resumeStream;
    private final android.os.Handler main = new android.os.Handler(android.os.Looper.getMainLooper());
    private final Runnable checkDisconnected = () -> {
        if (pc != null && pc.connectionState() != PeerConnection.PeerConnectionState.CONNECTED) reconnect();
    };
    private final Zoom zoom = new Zoom();
    private org.webrtc.audio.AudioDeviceModule adm;
    // two-finger gesture: decided once the fingers moved far enough
    private static final int TWO_NONE = 0, TWO_UNDECIDED = 1, TWO_PINCH = 2, TWO_SCROLL = 3;
    private int two = TWO_NONE;
    private float startSpan, lastSpan, startFx, startFy, lastFx, lastFy;

    @Override
    protected void onCreate(Bundle s) {
        super.onCreate(s);
        getWindow().setBackgroundDrawableResource(android.R.color.black);
        setVolumeControlStream(android.media.AudioManager.STREAM_MUSIC);
        setContentView(R.layout.activity_stream);
        deck = Deck.get(this);
        video = findViewById(R.id.video);
        picker = findViewById(R.id.picker);
        bar = findViewById(R.id.bar);
        state = findViewById(R.id.stream_state);
        pickerMessage = findViewById(R.id.picker_message);
        findViewById(R.id.start).setOnClickListener(v -> start());
        findViewById(R.id.stop).setOnClickListener(v -> finish());
        findViewById(R.id.kbd).setOnClickListener(v -> startActivity(new Intent(this, KeyboardActivity.class)));
        findViewById(R.id.switch_source).setOnClickListener(v -> {
            View sw = findViewById(R.id.switcher);
            sw.setVisibility(sw.getVisibility() == View.VISIBLE ? View.GONE : View.VISIBLE);
        });
        findViewById(R.id.stream_root).addOnLayoutChangeListener((v, l, t, r, b, ol, ot, or, ob) -> {
            if (r - l != or - ol || b - t != ob - ot) v.post(this::fitVideo);
        });
        video.setPivotX(0);
        video.setPivotY(0);
        video.addOnLayoutChangeListener((v, l, t, r, b, ol, ot, or, ob) -> {
            // new picture size (source switch, rotation): start unzoomed again
            if (r - l != or - ol || b - t != ob - ot) zoom.reset();
            View parent = (View) v.getParent();
            zoom.layout(parent.getWidth(), parent.getHeight(), l, t, r - l, b - t);
            applyZoom();
        });
        setupTouch();
        loadSources();
        prefetchIce();
        String[][] audios = {{"none", getString(R.string.audio_none)}, {"system", getString(R.string.audio_system)}, {"mic", getString(R.string.audio_mic)}};
        LinearLayout ar = findViewById(R.id.audios);
        for (String[] a : audios) {
            TextView t = chip(a[1]);
            t.setOnClickListener(v -> {
                audio = a[0];
                select(audioViews, t);
            });
            audioViews.add(t);
            ar.addView(t);
        }
        select(audioViews, audioViews.get(0));
    }

    @Override
    protected void onResume() {
        super.onResume();
        deck.startPolling();
    }

    @Override
    protected void onStart() {
        super.onStart();
        if (resumeStream) {
            // back from another app: the old surface and connection are gone, start fresh
            resumeStream = false;
            reconnects = 0;
            state(getString(R.string.stream_connecting), R.drawable.dot_warn);
            start();
        }
    }

    @Override
    protected void onStop() {
        // nobody watches: stop capturing (data, and no camera or mic running unseen)
        if (pc != null && !isFinishing()) {
            deck.send("stream.stop", "{}", null);
            teardown();
            resumeStream = true;
        }
        super.onStop();
    }

    @Override
    protected void onDestroy() {
        deck.send("stream.stop", "{}", null);
        teardown();
        super.onDestroy();
    }

    private void teardown() {
        main.removeCallbacks(checkDisconnected);
        main.removeCallbacks(sendOfferNow);
        if (input != null) input.close();
        if (pc != null) pc.close();
        input = null;
        pc = null;
        video.release();
        if (factory != null) factory.dispose();
        if (adm != null) adm.release();
        adm = null;
        if (egl != null) egl.release();
        factory = null;
        egl = null;
    }

    // ---- picker ----

    private void loadSources() {
        pickerMessage.setText(R.string.loading_monitors);
        deck.send("monitors.list", "{}", (ok, err, data) -> {
            try {
                JSONArray ms = ok ? new JSONArray(data) : new JSONArray();
                for (int i = 0; i < ms.length(); i++) {
                    String name = ms.getJSONObject(i).getString("name");
                    sources.put("screen:" + name, new String[]{"screen", name, name});
                }
            } catch (JSONException ignored) {
            }
            deck.send("cameras.list", "{}", (ok2, err2, data2) -> {
                pickerMessage.setText(ok ? "" : err);
                try {
                    JSONArray cs = ok2 ? new JSONArray(data2) : new JSONArray();
                    for (int i = 0; i < cs.length(); i++) {
                        JSONObject c = cs.getJSONObject(i);
                        sources.put("camera:" + c.getString("device"), new String[]{"camera", c.getString("device"), c.getString("name")});
                    }
                } catch (JSONException ignored) {
                }
                buildSourceChips();
            });
        });
    }

    private void buildSourceChips() {
        LinearLayout sr = findViewById(R.id.sources), sw = findViewById(R.id.switch_list);
        sr.removeAllViews();
        sw.removeAllViews();
        sourceViews.clear();
        for (java.util.Map.Entry<String, String[]> e : sources.entrySet()) {
            String id = e.getKey();
            String[] src = e.getValue();
            String label = "camera".equals(src[0]) ? src[2] : src[2];
            TextView t = chip(label);
            t.setTag(id);
            t.setOnClickListener(v -> {
                source = id;
                select(sourceViews, t);
            });
            sourceViews.add(t);
            LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(dp(240), dp(44));
            lp.bottomMargin = dp(8);
            sr.addView(t, lp);

            TextView live = chip(label);
            live.setOnClickListener(v -> switchTo(id));
            sw.addView(live);
        }
        if (!sourceViews.isEmpty()) {
            select(sourceViews, sourceViews.get(0));
            source = (String) sourceViews.get(0).getTag();
        }
    }

    /** Switches the running stream without reconnecting; cameras need the finger. */
    private void switchTo(String id) {
        String[] src = sources.get(id);
        if (src == null) return;
        String body;
        try {
            JSONObject o = new JSONObject().put("source", src[0]);
            if ("camera".equals(src[0])) o.put("device", src[1]);
            else o.put("output", src[1]);
            body = o.toString();
        } catch (JSONException e) {
            return;
        }
        findViewById(R.id.switcher).setVisibility(View.GONE);
        Deck.Callback cb = (ok, err, data) -> {
            if (ok) source = id;
            else state(err, R.drawable.dot_warn);
        };
        DeckCrypto.Message m = DeckCrypto.newMessage("stream.switch", body);
        if (!"camera".equals(src[0])) {
            deck.send(m, cb);
            return;
        }
        BioSign.sign(this, m, getString(R.string.bio_camera, deck.pairing().hostname), new BioSign.Result() {
            @Override
            public void signed(DeckCrypto.Message signed) {
                deck.send(signed, cb);
            }

            @Override
            public void failed(String why) {
                state(why, R.drawable.dot_warn);
            }
        });
    }

    private TextView chip(String label) {
        TextView t = new TextView(this, null, 0, R.style.Text);
        t.setText(label);
        t.setTextSize(14);
        t.setGravity(android.view.Gravity.CENTER);
        t.setPadding(dp(16), 0, dp(16), 0);
        t.setBackgroundResource(R.drawable.bg_key);
        t.setMinHeight(dp(44));
        LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(-2, dp(44));
        lp.setMarginEnd(dp(8));
        t.setLayoutParams(lp);
        return t;
    }

    private void select(List<TextView> group, TextView on) {
        for (TextView t : group) {
            boolean lit = t == on;
            t.setBackgroundResource(lit ? R.drawable.bg_key_on : R.drawable.bg_key);
            t.setTextColor(getColor(lit ? R.color.on_ink : R.color.text1));
        }
    }

    // ---- WebRTC ----

    private long t0;
    /*
     * The offer goes out as soon as it is useful, not when gathering is complete:
     * Android only reports "complete" after every network (Wi-Fi, mobile IPv6) and
     * every TURN transport answered or timed out, which takes seconds.
     */
    private static final long OFFER_MAX_WAIT_MS = 1500, OFFER_AFTER_PUBLIC_MS = 100;
    private boolean offerSent;
    private final Runnable sendOfferNow = () -> {
        if (offerSent || pc == null) return;
        SessionDescription local = pc.getLocalDescription();
        if (local == null) return; // not set yet; the next trigger sends it
        offerSent = true;
        sendOffer(local.description);
    };

    /** Connection timing per step, to see where the seconds go (adb logcat -s PocketDeck). */
    private void mark(String step) {
        android.util.Log.i("PocketDeck", "stream " + step + " +" + (android.os.SystemClock.elapsedRealtime() - t0) + " ms");
    }

    // TURN credentials live an hour: fetched while the picker is open, so the tap
    // on "start" does not wait for a relay round trip
    private static List<PeerConnection.IceServer> iceCache;
    private static long iceCacheUntil;

    private void prefetchIce() {
        deck.io().execute(this::iceServers);
    }

    /** STUN plus the relay's TURN credentials; cached, safe to call off the UI thread. */
    private List<PeerConnection.IceServer> iceServers() {
        synchronized (StreamActivity.class) {
            if (iceCache != null && System.currentTimeMillis() < iceCacheUntil) return iceCache;
        }
        List<PeerConnection.IceServer> ice = new ArrayList<>();
        ice.add(PeerConnection.IceServer.builder("stun:stun.l.google.com:19302").createIceServer());
        DeckStore.Pairing p = deck.pairing();
        long ttl = 0;
        try {
            JSONObject t = new de.raindancer118.pocketcore.Relay(p.relay, p.phoneToken).get("/v1/channels/" + p.channel + "/turn");
            JSONArray urls = t.getJSONArray("urls");
            List<String> list = new ArrayList<>();
            for (int i = 0; i < urls.length(); i++) list.add(urls.getString(i));
            ice.add(PeerConnection.IceServer.builder(list).setUsername(t.getString("username"))
                    .setPassword(t.getString("credential")).createIceServer());
            ttl = Math.max(0, t.optLong("ttl", 3600) - 300) * 1000; // renew 5 min early
        } catch (Exception ignored) {
            // no TURN: direct connections still work on most networks
        }
        synchronized (StreamActivity.class) {
            if (ttl > 0) {
                iceCache = ice;
                iceCacheUntil = System.currentTimeMillis() + ttl;
            }
        }
        return ice;
    }

    private void start() {
        t0 = android.os.SystemClock.elapsedRealtime();
        if (source.isEmpty()) return;
        pickerMessage.setText(R.string.stream_connecting);
        deck.io().execute(() -> {
            List<PeerConnection.IceServer> ice = iceServers();
            runOnUiThread(() -> connect(ice));
        });
    }

    private void connect(List<PeerConnection.IceServer> ice) {
        mark("turn creds");
        offerSent = false;
        egl = EglBase.create();
        PeerConnectionFactory.initialize(PeerConnectionFactory.InitializationOptions.builder(getApplicationContext())
                .createInitializationOptions());
        factory = PeerConnectionFactory.builder()
                .setVideoDecoderFactory(new DefaultVideoDecoderFactory(egl.getEglBaseContext()))
                .setVideoEncoderFactory(new DefaultVideoEncoderFactory(egl.getEglBaseContext(), true, true))
                .setAudioDeviceModule(audioModule())
                .createPeerConnectionFactory();
        // two restarts can race (session refresh, reconnect): init on a live renderer throws
        video.release();
        video.init(egl.getEglBaseContext(), new RendererCommon.RendererEvents() {
            @Override
            public void onFirstFrameRendered() {
                mark("first frame");
                runOnUiThread(() -> state(getString(R.string.stream_live), R.drawable.dot_live));
            }

            @Override
            public void onFrameResolutionChanged(int w, int h, int rotation) {
                frameW = rotation % 180 == 0 ? w : h;
                frameH = rotation % 180 == 0 ? h : w;
                runOnUiThread(StreamActivity.this::fitVideo);
            }
        });
        video.setScalingType(RendererCommon.ScalingType.SCALE_ASPECT_FIT);

        PeerConnection.RTCConfiguration cfg = new PeerConnection.RTCConfiguration(ice);
        cfg.sdpSemantics = PeerConnection.SdpSemantics.UNIFIED_PLAN;
        pc = factory.createPeerConnection(cfg, new Observer());
        if (pc == null) {
            pickerMessage.setText(R.string.stream_failed);
            return;
        }
        pc.addTransceiver(MediaStreamTrack.MediaType.MEDIA_TYPE_VIDEO,
                new RtpTransceiver.RtpTransceiverInit(RtpTransceiver.RtpTransceiverDirection.RECV_ONLY));
        if (!"none".equals(audio)) {
            pc.addTransceiver(MediaStreamTrack.MediaType.MEDIA_TYPE_AUDIO,
                    new RtpTransceiver.RtpTransceiverInit(RtpTransceiver.RtpTransceiverDirection.RECV_ONLY));
        }
        input = pc.createDataChannel("input", new DataChannel.Init());
        pc.createOffer(new Sdp() {
            @Override
            public void onCreateSuccess(SessionDescription offer) {
                pc.setLocalDescription(new Sdp(), new SessionDescription(offer.type, SdpTweaks.opusStereo(offer.description)));
                main.postDelayed(sendOfferNow, OFFER_MAX_WAIT_MS);
            }
        }, new MediaConstraints());
    }

    /** Called once ICE gathering is complete: the offer carries all candidates. */
    private void sendOffer(String sdp) {
        mark("offer gathered");
        String body;
        try {
            String[] src = sources.get(source);
            boolean cam = src != null && "camera".equals(src[0]);
            body = new JSONObject().put("offer", sdp).put("source", cam ? "camera" : "screen")
                    .put("output", cam || src == null ? "" : src[1]).put("device", cam ? src[1] : "")
                    .put("audio", audio).toString();
        } catch (JSONException e) {
            return;
        }
        DeckCrypto.Message m = DeckCrypto.newMessage("stream.start", body);
        Deck.Callback answer = (ok, err, data) -> {
            if (!ok) {
                if (Session.isSessionError(err)) {
                    // session ran out: one finger, then start again with a fresh offer
                    Session.open(this, (o, e) -> {
                        if (o) restart();
                        else pickerMessage.setText(e);
                    });
                } else {
                    pickerMessage.setText(err);
                }
                return;
            }
            try {
                mark("answer");
                String sdpAnswer = new JSONObject(data).getString("answer");
                pc.setRemoteDescription(new Sdp(), new SessionDescription(SessionDescription.Type.ANSWER, sdpAnswer));
                picker.setVisibility(View.GONE);
                bar.setVisibility(View.VISIBLE);
                state(getString(R.string.stream_connecting), R.drawable.dot_warn);
                immersive();
            } catch (JSONException e) {
                pickerMessage.setText(e.getMessage());
            }
        };
        boolean privateSource = source.startsWith("camera:") || "mic".equals(audio);
        if (!privateSource) {
            deck.send(m, answer);
            return;
        }
        // camera and microphone need a fresh finger every time
        BioSign.sign(this, m, getString(R.string.bio_camera, deck.pairing().hostname), new BioSign.Result() {
            @Override
            public void signed(DeckCrypto.Message signed) {
                deck.send(signed, answer);
            }

            @Override
            public void failed(String why) {
                pickerMessage.setText(why);
            }
        });
    }

    /**
     * Plays the computer's sound as media in stereo. WebRTC's default is a voice
     * call: earpiece routing and speech processing, which garbles music.
     */
    private org.webrtc.audio.AudioDeviceModule audioModule() {
        adm = org.webrtc.audio.JavaAudioDeviceModule.builder(this)
                .setAudioAttributes(new android.media.AudioAttributes.Builder()
                        .setUsage(android.media.AudioAttributes.USAGE_MEDIA)
                        .setContentType(android.media.AudioAttributes.CONTENT_TYPE_MUSIC)
                        .build())
                .setUseStereoOutput(true)
                .setUseHardwareAcousticEchoCanceler(false)
                .setUseHardwareNoiseSuppressor(false)
                .createAudioDeviceModule();
        return adm;
    }

    private void restart() {
        teardown();
        start();
    }

    /** Connection lost: try again a few times before giving up. */
    private void reconnect() {
        if (pc == null || isFinishing()) return;
        if (reconnects >= MAX_RECONNECTS) {
            state(getString(R.string.stream_failed), R.drawable.dot_off);
            return;
        }
        reconnects++;
        state(getString(R.string.stream_connecting), R.drawable.dot_warn);
        restart();
    }

    private void state(String text, int dot) {
        state.setText(text);
        state.setCompoundDrawablesRelativeWithIntrinsicBounds(dot, 0, 0, 0);
    }

    private void immersive() {
        WindowInsetsController c = getWindow().getInsetsController();
        if (c != null) {
            c.hide(WindowInsets.Type.systemBars());
            c.setSystemBarsBehavior(WindowInsetsController.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE);
        }
    }

    private class Observer implements PeerConnection.Observer {
        @Override
        public void onIceGatheringChange(PeerConnection.IceGatheringState s) {
            if (s == PeerConnection.IceGatheringState.COMPLETE) main.post(sendOfferNow);
        }

        @Override
        public void onTrack(RtpTransceiver t) {
            MediaStreamTrack track = t.getReceiver().track();
            if (track instanceof VideoTrack) ((VideoTrack) track).addSink(video);
        }

        @Override
        public void onConnectionChange(PeerConnection.PeerConnectionState s) {
            mark("connection " + s);
            // DISCONNECTED often heals by itself within a second or two; give it a moment
            runOnUiThread(() -> {
                main.removeCallbacks(checkDisconnected);
                if (s == PeerConnection.PeerConnectionState.CONNECTED) reconnects = 0;
                else if (s == PeerConnection.PeerConnectionState.FAILED) reconnect();
                else if (s == PeerConnection.PeerConnectionState.DISCONNECTED) main.postDelayed(checkDisconnected, 4000);
            });
        }

        @Override
        public void onSignalingChange(PeerConnection.SignalingState s) {
        }

        @Override
        public void onIceConnectionChange(PeerConnection.IceConnectionState s) {
        }

        @Override
        public void onIceConnectionReceivingChange(boolean b) {
        }

        @Override
        public void onIceCandidate(IceCandidate c) {
            // a public (srflx) or TURN path is enough to reach the computer from anywhere
            if (c.sdp.contains(" typ srflx") || c.sdp.contains(" typ relay")) main.postDelayed(sendOfferNow, OFFER_AFTER_PUBLIC_MS);
        }

        @Override
        public void onIceCandidatesRemoved(IceCandidate[] cs) {
        }

        @Override
        public void onAddStream(MediaStream ms) {
        }

        @Override
        public void onRemoveStream(MediaStream ms) {
        }

        @Override
        public void onDataChannel(DataChannel dc) {
        }

        @Override
        public void onRenegotiationNeeded() {
        }

        @Override
        public void onAddTrack(RtpReceiver r, MediaStream[] ms) {
        }
    }

    private static class Sdp implements SdpObserver {
        @Override
        public void onCreateSuccess(SessionDescription d) {
        }

        @Override
        public void onSetSuccess() {
        }

        @Override
        public void onCreateFailure(String e) {
        }

        @Override
        public void onSetFailure(String e) {
        }
    }

    // ---- touch → mouse ----

    private void setupTouch() {
        GestureDetector g = new GestureDetector(this, new GestureDetector.SimpleOnGestureListener() {
            // without this Android stops delivering the rest of the gesture
            @Override
            public boolean onDown(@NonNull MotionEvent e) {
                return true;
            }

            @Override
            public boolean onSingleTapConfirmed(@NonNull MotionEvent e) {
                send("tap", e.getX(), e.getY(), 0);
                return true;
            }

            @Override
            public boolean onDoubleTap(@NonNull MotionEvent e) {
                send("tap", e.getX(), e.getY(), 0);
                send("tap", e.getX(), e.getY(), 0);
                return true;
            }

            @Override
            public void onLongPress(@NonNull MotionEvent e) {
                send("right", e.getX(), e.getY(), 0);
            }

            @Override
            public boolean onScroll(MotionEvent e1, @NonNull MotionEvent e2, float dx, float dy) {
                if (zoom.zoomed()) {
                    // zoomed in: one finger moves the picture like any image viewer
                    zoom.panBy(-dx, -dy);
                    applyZoom();
                    return true;
                }
                long now = System.currentTimeMillis();
                if (now - lastMove >= MOVE_EVERY_MS) {
                    lastMove = now;
                    send("move", e2.getX(), e2.getY(), 0);
                }
                return true;
            }
        });
        findViewById(R.id.touch).setOnTouchListener((v, e) -> {
            int action = e.getActionMasked();
            if (action == MotionEvent.ACTION_DOWN) two = TWO_NONE;
            if (action == MotionEvent.ACTION_POINTER_DOWN && e.getPointerCount() == 2 && two == TWO_NONE) {
                // a second finger ends any tap, long press or drag of the first
                MotionEvent cancel = MotionEvent.obtain(e);
                cancel.setAction(MotionEvent.ACTION_CANCEL);
                g.onTouchEvent(cancel);
                cancel.recycle();
                two = TWO_UNDECIDED;
                startSpan = lastSpan = span(e);
                startFx = lastFx = (e.getX(0) + e.getX(1)) / 2;
                startFy = lastFy = (e.getY(0) + e.getY(1)) / 2;
                return true;
            }
            if (two != TWO_NONE) {
                if (action == MotionEvent.ACTION_MOVE && e.getPointerCount() >= 2) twoFingers(e);
                if (action == MotionEvent.ACTION_UP) v.performClick();
                return true;
            }
            if (action == MotionEvent.ACTION_UP) v.performClick();
            return g.onTouchEvent(e);
        });
    }

    /** Pinch zooms (and pans with the fingers), a parallel drag scrolls the computer. */
    private void twoFingers(MotionEvent e) {
        float sp = span(e), fx = (e.getX(0) + e.getX(1)) / 2, fy = (e.getY(0) + e.getY(1)) / 2;
        if (two == TWO_UNDECIDED) {
            if (Math.abs(sp - startSpan) > dp(24)) {
                two = TWO_PINCH;
            } else if (Math.abs(fy - startFy) > dp(16) || Math.abs(fx - startFx) > dp(16)) {
                two = TWO_SCROLL;
                scrollAcc = 0;
            }
        }
        if (two == TWO_PINCH && lastSpan > 0) {
            zoom.zoomBy(sp / lastSpan, fx, fy);
            zoom.panBy(fx - lastFx, fy - lastFy);
            applyZoom();
        } else if (two == TWO_SCROLL) {
            // one wheel step per 40 px
            scrollAcc += fy - lastFy;
            int steps = (int) (scrollAcc / dp(40));
            if (steps != 0) {
                scrollAcc -= steps * dp(40);
                send("scroll", fx, fy, steps);
            }
        }
        lastSpan = sp;
        lastFx = fx;
        lastFy = fy;
    }

    private static float span(MotionEvent e) {
        return (float) Math.hypot(e.getX(0) - e.getX(1), e.getY(0) - e.getY(1));
    }

    /**
     * Sizes the renderer to the picture, fitted and centred, so its bounds are
     * exactly the picture (no crop, no black bars inside the view). Done by hand:
     * wrap_content stays 0x0 until a frame is drawn, which needs a surface first.
     */
    private void fitVideo() {
        View root = findViewById(R.id.stream_root);
        int pw = root.getWidth(), ph = root.getHeight();
        if (pw == 0 || ph == 0 || frameW <= 0 || frameH <= 0) return;
        float sc = Math.min(pw / (float) frameW, ph / (float) frameH);
        android.widget.FrameLayout.LayoutParams lp = (android.widget.FrameLayout.LayoutParams) video.getLayoutParams();
        int w = Math.round(frameW * sc), h = Math.round(frameH * sc);
        if (lp.width == w && lp.height == h) return;
        lp.width = w;
        lp.height = h;
        lp.gravity = android.view.Gravity.CENTER;
        video.setLayoutParams(lp);
    }

    private void applyZoom() {
        video.setScaleX(zoom.scale());
        video.setScaleY(zoom.scale());
        video.setTranslationX(zoom.tx());
        video.setTranslationY(zoom.ty());
    }

    /** Maps a touch on the full-screen touch layer into 0..1 of the picture. */
    private void send(String type, float tx, float ty, int dy) {
        if (input == null || input.state() != DataChannel.State.OPEN) return;
        double[] p = zoom.toPicture(tx, ty);
        if (p == null) return;
        String msg = "{\"t\":\"" + type + "\",\"x\":" + p[0] + ",\"y\":" + p[1] + ",\"dy\":" + dy + "}";
        input.send(new DataChannel.Buffer(ByteBuffer.wrap(msg.getBytes(StandardCharsets.UTF_8)), false));
    }

    private int dp(int v) {
        return Math.round(v * getResources().getDisplayMetrics().density);
    }
}

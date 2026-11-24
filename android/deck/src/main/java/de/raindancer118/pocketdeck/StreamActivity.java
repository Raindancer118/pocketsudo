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
import java.util.Collections;
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
    private int frameW = 16, frameH = 9;
    private long lastMove;
    private float scrollAcc;

    @Override
    protected void onCreate(Bundle s) {
        super.onCreate(s);
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
        setupTouch();
        loadSources();
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
    protected void onDestroy() {
        deck.send("stream.stop", "{}", null);
        if (input != null) input.close();
        if (pc != null) pc.close();
        video.release();
        if (factory != null) factory.dispose();
        if (egl != null) egl.release();
        super.onDestroy();
    }

    // ---- picker ----

    private void loadSources() {
        pickerMessage.setText(R.string.loading_monitors);
        LinearLayout sr = findViewById(R.id.sources);
        deck.send("monitors.list", "{}", (ok, err, data) -> {
            pickerMessage.setText(ok ? "" : err);
            try {
                JSONArray ms = ok ? new JSONArray(data) : new JSONArray();
                for (int i = 0; i < ms.length(); i++) {
                    String name = ms.getJSONObject(i).getString("name");
                    addSource(sr, name, name);
                }
            } catch (JSONException ignored) {
            }
            addSource(sr, "camera", getString(R.string.camera));
            if (!sourceViews.isEmpty()) {
                select(sourceViews, sourceViews.get(0));
                source = (String) sourceViews.get(0).getTag();
            }
        });
    }

    private void addSource(LinearLayout parent, String id, String label) {
        TextView t = chip(label);
        t.setTag(id);
        t.setOnClickListener(v -> {
            source = id;
            select(sourceViews, t);
        });
        sourceViews.add(t);
        LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(dp(220), dp(44));
        lp.bottomMargin = dp(8);
        parent.addView(t, lp);
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

    private void start() {
        if (source.isEmpty()) return;
        pickerMessage.setText(R.string.stream_connecting);
        egl = EglBase.create();
        PeerConnectionFactory.initialize(PeerConnectionFactory.InitializationOptions.builder(getApplicationContext())
                .createInitializationOptions());
        factory = PeerConnectionFactory.builder()
                .setVideoDecoderFactory(new DefaultVideoDecoderFactory(egl.getEglBaseContext()))
                .setVideoEncoderFactory(new DefaultVideoEncoderFactory(egl.getEglBaseContext(), true, true))
                .createPeerConnectionFactory();
        video.init(egl.getEglBaseContext(), new RendererCommon.RendererEvents() {
            @Override
            public void onFirstFrameRendered() {
                runOnUiThread(() -> state(getString(R.string.stream_live), R.drawable.dot_live));
            }

            @Override
            public void onFrameResolutionChanged(int w, int h, int rotation) {
                frameW = rotation % 180 == 0 ? w : h;
                frameH = rotation % 180 == 0 ? h : w;
            }
        });
        video.setScalingType(RendererCommon.ScalingType.SCALE_ASPECT_FIT);

        PeerConnection.RTCConfiguration cfg = new PeerConnection.RTCConfiguration(Collections.singletonList(
                PeerConnection.IceServer.builder("stun:stun.l.google.com:19302").createIceServer()));
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
                pc.setLocalDescription(new Sdp(), offer);
            }
        }, new MediaConstraints());
    }

    /** Called once ICE gathering is complete: the offer carries all candidates. */
    private void sendOffer(String sdp) {
        String body;
        try {
            body = new JSONObject().put("offer", sdp).put("source", "camera".equals(source) ? "camera" : "screen")
                    .put("output", "camera".equals(source) ? "" : source).put("audio", audio).toString();
        } catch (JSONException e) {
            return;
        }
        DeckCrypto.Message m = DeckCrypto.newMessage("stream.start", body);
        Deck.Callback answer = (ok, err, data) -> {
            if (!ok) {
                pickerMessage.setText(Session.isSessionError(err) ? getString(R.string.needs_session) : err);
                return;
            }
            try {
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
        boolean privateSource = "camera".equals(source) || "mic".equals(audio);
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
            if (s == PeerConnection.IceGatheringState.COMPLETE && pc.getLocalDescription() != null) {
                String sdp = pc.getLocalDescription().description;
                runOnUiThread(() -> sendOffer(sdp));
            }
        }

        @Override
        public void onTrack(RtpTransceiver t) {
            MediaStreamTrack track = t.getReceiver().track();
            if (track instanceof VideoTrack) ((VideoTrack) track).addSink(video);
        }

        @Override
        public void onConnectionChange(PeerConnection.PeerConnectionState s) {
            if (s == PeerConnection.PeerConnectionState.FAILED || s == PeerConnection.PeerConnectionState.DISCONNECTED) {
                runOnUiThread(() -> state(getString(R.string.stream_failed), R.drawable.dot_off));
            }
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
            @Override
            public boolean onSingleTapConfirmed(@NonNull MotionEvent e) {
                send("tap", e.getX(), e.getY(), 0);
                return true;
            }

            @Override
            public void onLongPress(@NonNull MotionEvent e) {
                send("right", e.getX(), e.getY(), 0);
            }

            @Override
            public boolean onScroll(MotionEvent e1, @NonNull MotionEvent e2, float dx, float dy) {
                if (e2.getPointerCount() > 1) return false;
                long now = System.currentTimeMillis();
                if (now - lastMove >= MOVE_EVERY_MS) {
                    lastMove = now;
                    send("move", e2.getX(), e2.getY(), 0);
                }
                return true;
            }
        });
        findViewById(R.id.touch).setOnTouchListener((v, e) -> {
            if (e.getPointerCount() == 2 && e.getActionMasked() == MotionEvent.ACTION_MOVE && e.getHistorySize() > 0) {
                // two fingers: scroll, one wheel step per 40 px
                scrollAcc += e.getY(0) - e.getHistoricalY(0, 0);
                int steps = (int) (scrollAcc / dp(40));
                if (steps != 0) {
                    scrollAcc -= steps * dp(40);
                    send("scroll", e.getX(0), e.getY(0), steps);
                }
                return true;
            }
            if (e.getActionMasked() == MotionEvent.ACTION_UP) v.performClick();
            return g.onTouchEvent(e);
        });
    }

    /** Maps view coordinates into 0..1 of the actual picture (letterboxing removed). */
    private void send(String type, float vx, float vy, int dy) {
        if (input == null || input.state() != DataChannel.State.OPEN) return;
        float vw = video.getWidth(), vh = video.getHeight();
        float scale = Math.min(vw / frameW, vh / frameH);
        float w = frameW * scale, h = frameH * scale;
        float left = (vw - w) / 2, top = (vh - h) / 2;
        double x = (vx - left) / w, y = (vy - top) / h;
        if (x < 0 || x > 1 || y < 0 || y > 1) return;
        String msg = "{\"t\":\"" + type + "\",\"x\":" + x + ",\"y\":" + y + ",\"dy\":" + dy + "}";
        input.send(new DataChannel.Buffer(ByteBuffer.wrap(msg.getBytes(StandardCharsets.UTF_8)), false));
    }

    private int dp(int v) {
        return Math.round(v * getResources().getDisplayMetrics().density);
    }
}

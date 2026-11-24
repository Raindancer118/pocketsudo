package de.raindancer118.pocketdeck;

import android.content.Context;
import android.os.Handler;
import android.os.Looper;
import android.util.Log;

import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

import java.io.IOException;
import java.security.GeneralSecurityException;
import java.util.HashSet;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

import de.raindancer118.pocketcore.DeckCrypto;
import de.raindancer118.pocketcore.Relay;

/**
 * Talks to the notebook agent: seals commands into the host inbox and
 * long-polls the phone inbox for results while the app is visible.
 */
final class Deck {
    interface Callback {
        void done(boolean ok, String error, String data);
    }

    private static final String TAG = "PocketDeck";
    private static final long TIMEOUT_MS = 20_000;
    // holds only the application context, so no activity leaks
    @android.annotation.SuppressLint("StaticFieldLeak")
    private static Deck instance;

    static synchronized Deck get(Context ctx) {
        if (instance == null) instance = new Deck(ctx.getApplicationContext());
        return instance;
    }

    private final Context ctx;
    private final DeckStore store;
    private final ExecutorService io = Executors.newCachedThreadPool();
    private final Handler main = new Handler(Looper.getMainLooper());
    private final Map<String, Callback> pending = new ConcurrentHashMap<>();
    private final Set<String> seen = new HashSet<>();
    private volatile boolean polling;
    /** Remote-control session (keyboard, screen), as granted by the agent. */
    volatile long sessionUntil;
    private volatile Thread poller;
    private byte[] key;

    private Deck(Context ctx) {
        this.ctx = ctx;
        this.store = new DeckStore(ctx);
    }

    DeckStore.Pairing pairing() {
        return store.pairing();
    }

    private synchronized byte[] key(DeckStore.Pairing p) throws GeneralSecurityException {
        if (key == null) key = KxKeys.deriveKey(p);
        return key;
    }

    synchronized void forgetKey() {
        key = null;
    }

    void send(String type, String body, Callback cb) {
        send(DeckCrypto.newMessage(type, body), cb);
    }

    /** m may carry a biometric signature (m.sig); it is sealed as-is. */
    void send(DeckCrypto.Message m, Callback cb) {
        DeckStore.Pairing p = store.pairing();
        if (p == null) {
            cb.done(false, ctx.getString(R.string.not_paired), null);
            return;
        }
        if (cb != null) {
            pending.put(m.id, cb);
            main.postDelayed(() -> {
                Callback c = pending.remove(m.id);
                if (c != null) c.done(false, ctx.getString(R.string.no_answer), null);
            }, TIMEOUT_MS);
        }
        io.execute(() -> {
            try {
                String box = DeckCrypto.sealMessage(key(p), m, System.currentTimeMillis());
                new Relay(p.relay, p.phoneToken).post("/v1/channels/" + p.channel + "/inbox/host",
                        new JSONObject().put("box", box));
            } catch (GeneralSecurityException | IOException | JSONException e) {
                Log.w(TAG, "send " + m.type, e);
                Callback c = pending.remove(m.id);
                if (c != null) main.post(() -> c.done(false, e.getMessage(), null));
            }
        });
        startPolling();
    }

    synchronized void startPolling() {
        if (polling) return;
        polling = true;
        poller = new Thread(this::pollLoop, "deck-poll");
        poller.start();
    }

    synchronized void stopPolling() {
        polling = false;
        if (poller != null) poller.interrupt();
    }

    private void pollLoop() {
        while (polling) {
            DeckStore.Pairing p = store.pairing();
            if (p == null) return;
            try {
                Object r = new Relay(p.relay, p.phoneToken).call("GET",
                        "/v1/channels/" + p.channel + "/inbox/phone?wait=25&after=" + p.after, null, 40_000);
                JSONArray msgs = ((JSONObject) r).optJSONArray("messages");
                if (msgs == null || msgs.length() == 0) continue;
                for (int i = 0; i < msgs.length(); i++) {
                    JSONObject o = msgs.getJSONObject(i);
                    p.after = Math.max(p.after, o.getLong("seq"));
                    handle(p, o.getString("box"));
                }
                store.save(p);
            } catch (IOException | JSONException | ClassCastException e) {
                if (!polling) return;
                try {
                    Thread.sleep(2000);
                } catch (InterruptedException ie) {
                    return;
                }
            }
        }
    }

    private void handle(DeckStore.Pairing p, String box) {
        try {
            DeckCrypto.Message m = DeckCrypto.openMessage(key(p), box, System.currentTimeMillis(), seen);
            if (m.ref == null) return;
            Callback cb = pending.remove(m.ref);
            if (cb == null) return;
            JSONObject res = new JSONObject(m.body);
            boolean ok = res.optBoolean("ok");
            String err = res.optString("error", null);
            String data = res.has("data") ? res.get("data").toString() : null;
            main.post(() -> cb.done(ok, err, data));
        } catch (GeneralSecurityException | JSONException e) {
            Log.w(TAG, "dropped message", e);
        }
    }
}

package de.raindancer118.pocketdeck;

import android.content.Intent;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.provider.Settings;
import android.view.View;
import android.widget.TextView;

import androidx.appcompat.app.AppCompatActivity;
import androidx.biometric.BiometricManager;

import org.json.JSONException;
import org.json.JSONObject;

import java.io.IOException;
import java.security.GeneralSecurityException;
import java.util.concurrent.Executors;

import de.raindancer118.pocketcore.DeckCrypto;
import de.raindancer118.pocketcore.Keys;
import de.raindancer118.pocketcore.Protocol;
import de.raindancer118.pocketcore.Push;
import de.raindancer118.pocketcore.Relay;
import de.raindancer118.pocketcore.Ui;

/** QR pairing with a computer running `pocketdeck pair`. */
public class PairActivity extends AppCompatActivity {
    private TextView headline, code, state, done, cancel;
    private volatile boolean cancelled;
    private String signAlias, kxAlias;
    private boolean paired;

    @Override
    protected void onCreate(Bundle s) {
        super.onCreate(s);
        setContentView(R.layout.activity_pair);
        headline = findViewById(R.id.headline);
        code = findViewById(R.id.code);
        state = findViewById(R.id.state);
        done = findViewById(R.id.done);
        cancel = findViewById(R.id.cancel);
        Ui.link(cancel);
        cancel.setOnClickListener(v -> finish());
        done.setOnClickListener(v -> {
            startActivity(new Intent(this, MainActivity.class).addFlags(Intent.FLAG_ACTIVITY_CLEAR_TOP));
            finish();
        });
        start(getIntent().getData());
    }

    @Override
    protected void onDestroy() {
        cancelled = true;
        if (!paired) {
            if (signAlias != null) Keys.delete(signAlias);
            if (kxAlias != null) KxKeys.delete(kxAlias);
        }
        super.onDestroy();
    }

    private void start(Uri data) {
        String raw = data == null ? "" : data.toString().replaceFirst("^pocketdeck://", "pocketsudo://");
        Protocol.PairUri u;
        try {
            u = Protocol.PairUri.parse(raw);
        } catch (IllegalArgumentException e) {
            fail(getString(R.string.not_a_code));
            return;
        }
        Uri relay = Uri.parse(u.relay);
        boolean local = "localhost".equals(relay.getHost()) || "10.0.2.2".equals(relay.getHost());
        if (!"https".equals(relay.getScheme()) && !(BuildConfig.DEBUG && local)) {
            fail(getString(R.string.pair_insecure));
            return;
        }
        if (BiometricManager.from(this).canAuthenticate(BiometricManager.Authenticators.BIOMETRIC_STRONG)
                != BiometricManager.BIOMETRIC_SUCCESS) {
            fail(getString(R.string.no_biometric));
            return;
        }
        headline.setText(getString(R.string.pair_connecting, u.hostname));
        Executors.newSingleThreadExecutor().execute(() -> run(u));
    }

    private void run(Protocol.PairUri u) {
        try {
            Relay anon = new Relay(u.relay, null);
            Push.init(this, anon.get("/v1/config?app=deck").optJSONObject("fcm"));
            String fcmToken = Push.token(this);

            JSONObject host = anon.get("/v1/channels/" + u.channel + "/host");
            String hostPub = host.getString("host_pub");
            if (!Protocol.fingerprint(hostPub).equals(u.fingerprint) || !"deck".equals(host.optString("app"))) {
                ui(() -> fail(getString(R.string.pair_wrong_host)));
                return;
            }
            if (!"pairing".equals(host.optString("state"))) {
                ui(() -> fail(getString(R.string.pair_used)));
                return;
            }
            signAlias = "deck_sign_" + u.channel;
            kxAlias = "deck_kx_" + u.channel;
            String signPub = Keys.create(signAlias);
            String kxPub = KxKeys.create(kxAlias);
            String device = deviceName();
            JSONObject reg = (JSONObject) anon.post("/v1/channels/" + u.channel + "/phone", new JSONObject()
                    .put("phone_pub", signPub).put("phone_kx", kxPub).put("device_name", device)
                    .put("fcm_token", fcmToken)
                    .put("mac", DeckCrypto.pairMacV2(u.secret, u.channel, signPub, kxPub, device)));
            String token = reg.getString("phone_token");
            String vcode = Protocol.groupCode(Protocol.verificationCode(hostPub, signPub));
            ui(() -> {
                headline.setText(R.string.pair_compare);
                code.setVisibility(View.VISIBLE);
                code.setText(vcode);
                setState(getString(R.string.pair_confirm_there), R.drawable.dot_live);
            });
            Relay phone = new Relay(u.relay, token);
            while (!cancelled) {
                String st = phone.getLong("/v1/channels/" + u.channel + "/status?wait=25").optString("state");
                if ("active".equals(st)) break;
                if ("pairing".equals(st)) {
                    ui(() -> fail(getString(R.string.pair_rejected)));
                    return;
                }
            }
            if (cancelled) return;

            DeckStore store = new DeckStore(this);
            DeckStore.Pairing old = store.pairing();
            DeckStore.Pairing p = new DeckStore.Pairing();
            p.relay = u.relay;
            p.channel = u.channel;
            p.hostname = host.optString("hostname", u.hostname);
            p.hostPub = hostPub;
            p.hostKx = host.getString("host_kx");
            p.phoneToken = token;
            p.signAlias = signAlias;
            p.kxAlias = kxAlias;
            store.save(p);
            paired = true;
            if (old != null && !old.channel.equals(p.channel)) {
                Keys.delete(old.signAlias);
                KxKeys.delete(old.kxAlias);
            }
            Deck.get(this).forgetKey();
            ui(() -> {
                headline.setText(getString(R.string.pair_done, p.hostname));
                code.setVisibility(View.GONE);
                setState(null, 0);
                cancel.setVisibility(View.GONE);
                done.setVisibility(View.VISIBLE);
            });
        } catch (Relay.HttpException e) {
            String msg = e.code == 404 ? getString(R.string.pair_used) : getString(R.string.pair_failed, e.getMessage());
            ui(() -> fail(msg));
        } catch (IOException | JSONException | GeneralSecurityException | RuntimeException e) {
            ui(() -> fail(getString(R.string.pair_failed, String.valueOf(e.getMessage()))));
        }
    }

    private String deviceName() {
        String n = Settings.Global.getString(getContentResolver(), Settings.Global.DEVICE_NAME);
        if (n == null || n.isEmpty()) n = Build.MODEL;
        return n.length() > 64 ? n.substring(0, 64) : n;
    }

    private void ui(Runnable r) {
        if (!cancelled) runOnUiThread(r);
    }

    private void setState(String text, int dot) {
        state.setVisibility(text == null ? View.GONE : View.VISIBLE);
        state.setText(text);
        state.setCompoundDrawablesRelativeWithIntrinsicBounds(dot, 0, 0, 0);
    }

    private void fail(String msg) {
        code.setVisibility(View.GONE);
        headline.setText(msg);
        setState(null, 0);
    }
}

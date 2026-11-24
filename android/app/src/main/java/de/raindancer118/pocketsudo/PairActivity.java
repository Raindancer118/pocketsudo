package de.raindancer118.pocketsudo;

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

/**
 * Pairing from the QR code: check the notebook's key against the code, create
 * the Keystore key, register, show the verification code, wait for the "y".
 */
public class PairActivity extends AppCompatActivity {
    private TextView headline, code, state, done, cancel;
    private volatile boolean cancelled;
    private String keyAlias;
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
        if (!paired && keyAlias != null) Keys.delete(keyAlias);
        super.onDestroy();
    }

    private void start(Uri data) {
        Protocol.PairUri u;
        try {
            u = Protocol.PairUri.parse(data == null ? "" : data.toString());
        } catch (IllegalArgumentException e) {
            fail(getString(R.string.not_a_code));
            return;
        }
        Uri relay = Uri.parse(u.relay);
        boolean local = "10.0.2.2".equals(relay.getHost()) || "localhost".equals(relay.getHost());
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
        setState(null, 0);
        App.IO.execute(() -> run(u));
    }

    private void run(Protocol.PairUri u) {
        try {
            Relay anon = new Relay(u.relay, null);
            JSONObject cfg = anon.get("/v1/config");
            Push.init(this, cfg.optJSONObject("fcm"));
            String fcmToken = Push.token(this);

            JSONObject host = anon.get("/v1/channels/" + u.channel + "/host");
            String hostPub = host.getString("host_pub");
            if (!Protocol.fingerprint(hostPub).equals(u.fingerprint)) {
                ui(() -> fail(getString(R.string.pair_wrong_host)));
                return;
            }
            if (!"pairing".equals(host.optString("state"))) {
                ui(() -> fail(getString(R.string.pair_used)));
                return;
            }

            keyAlias = "pair_" + u.channel;
            String phonePub = Keys.create(keyAlias);
            String device = deviceName();
            JSONObject reg = (JSONObject) anon.post("/v1/channels/" + u.channel + "/phone", new JSONObject()
                    .put("phone_pub", phonePub).put("device_name", device).put("fcm_token", fcmToken)
                    .put("mac", Protocol.pairMac(u.secret, u.channel, phonePub, device)));
            String token = reg.getString("phone_token");

            String vcode = Protocol.groupCode(Protocol.verificationCode(hostPub, phonePub));
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

            Store.Pairing p = new Store.Pairing();
            p.relay = u.relay;
            p.channel = u.channel;
            p.hostname = host.optString("hostname", u.hostname);
            p.hostPub = hostPub;
            p.phoneToken = token;
            p.keyAlias = keyAlias;
            p.paired = System.currentTimeMillis();
            new Store(this).savePairing(p);
            paired = true;
            ui(() -> {
                headline.setText(getString(R.string.pair_done, p.hostname));
                code.setVisibility(View.GONE);
                setState(null, 0);
                cancel.setVisibility(View.GONE);
                done.setVisibility(View.VISIBLE);
                if (Build.VERSION.SDK_INT >= 33) {
                    requestPermissions(new String[]{android.Manifest.permission.POST_NOTIFICATIONS}, 1);
                }
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
        if (keyAlias != null) Keys.delete(keyAlias);
    }
}

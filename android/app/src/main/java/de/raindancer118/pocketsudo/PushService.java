package de.raindancer118.pocketsudo;

import android.util.Log;

import androidx.annotation.NonNull;

import com.google.firebase.messaging.FirebaseMessagingService;
import com.google.firebase.messaging.RemoteMessage;

import org.json.JSONException;
import org.json.JSONObject;

import java.io.IOException;
import java.security.GeneralSecurityException;
import java.util.Map;

public class PushService extends FirebaseMessagingService {
    private static final String TAG = "PocketSudo";

    @Override
    public void onMessageReceived(@NonNull RemoteMessage msg) {
        Map<String, String> d = msg.getData();
        String type = d.get("type"), channel = d.get("channel"), id = d.get("id");
        if (type == null || channel == null || id == null) return;
        Store store = new Store(this);
        Store.Pairing p = store.pairing(channel);
        if (p == null) return;

        if ("cancel".equals(type)) {
            Requests.cancelNotification(this, id);
            Protocol.Request r = App.SEEN.remove(id);
            if (r != null) store.record(r, Store.OUT_ELSEWHERE);
            App.fireCancelled(id);
            return;
        }
        if (!"request".equals(type)) return;

        Requests.notifyPlaceholder(this, p, id);
        try {
            Protocol.Request r = Requests.fetch(p, id);
            if (r.secondsLeft() <= 0) {
                Requests.cancelNotification(this, id);
                return;
            }
            Requests.notifyRequest(this, p, r);
        } catch (SecurityException | GeneralSecurityException e) {
            // never show a request the notebook did not sign
            Log.w(TAG, "rejected request", e);
            Requests.cancelNotification(this, id);
        } catch (IOException e) {
            // keep the placeholder: opening it retries the fetch
            Log.w(TAG, "fetch", e);
        }
    }

    @Override
    public void onNewToken(@NonNull String token) {
        for (Store.Pairing p : new Store(this).pairings()) {
            try {
                Relay.of(p).call("PUT", "/v1/channels/" + p.channel + "/fcm",
                        new JSONObject().put("fcm_token", token), 15000);
            } catch (IOException | JSONException e) {
                Log.w(TAG, "token update for " + p.hostname, e);
            }
        }
    }
}

package de.raindancer118.pocketdeck;

import android.util.Log;

import androidx.annotation.NonNull;

import com.google.firebase.messaging.FirebaseMessagingService;
import com.google.firebase.messaging.RemoteMessage;

import org.json.JSONObject;

import de.raindancer118.pocketcore.Relay;

/** The computer pushed something (e.g. a new clipboard): fetch it right away. */
public class DeckPushService extends FirebaseMessagingService {
    @Override
    public void onMessageReceived(@NonNull RemoteMessage msg) {
        if (!"deck".equals(msg.getData().get("type"))) return;
        try {
            Deck.get(this).pollOnce(0);
        } catch (Exception e) {
            Log.w("PocketDeck", "push fetch", e);
        }
    }

    @Override
    public void onNewToken(@NonNull String token) {
        DeckStore.Pairing p = new DeckStore(this).pairing();
        if (p == null) return;
        try {
            new Relay(p.relay, p.phoneToken).call("PUT", "/v1/channels/" + p.channel + "/fcm",
                    new JSONObject().put("fcm_token", token), 15000);
        } catch (Exception e) {
            Log.w("PocketDeck", "token update", e);
        }
    }
}

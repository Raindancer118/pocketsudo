package de.raindancer118.pocketdeck;

import android.content.Context;
import android.content.SharedPreferences;

import org.json.JSONException;
import org.json.JSONObject;

/** The one paired computer (v1 controls a single machine). */
final class DeckStore {
    static final class Pairing {
        String relay, channel, hostname, hostPub, hostKx, phoneToken, signAlias, kxAlias;
        long after;
    }

    private final SharedPreferences prefs;

    DeckStore(Context ctx) {
        prefs = ctx.getApplicationContext().getSharedPreferences("deck", Context.MODE_PRIVATE);
    }

    synchronized Pairing pairing() {
        String s = prefs.getString("pairing", "");
        if (s.isEmpty()) return null;
        try {
            JSONObject o = new JSONObject(s);
            Pairing p = new Pairing();
            p.relay = o.getString("relay");
            p.channel = o.getString("channel");
            p.hostname = o.optString("hostname");
            p.hostPub = o.getString("host_pub");
            p.hostKx = o.getString("host_kx");
            p.phoneToken = o.getString("phone_token");
            p.signAlias = o.getString("sign_alias");
            p.kxAlias = o.getString("kx_alias");
            p.after = o.optLong("after");
            return p;
        } catch (JSONException e) {
            return null;
        }
    }

    synchronized void save(Pairing p) {
        try {
            prefs.edit().putString("pairing", new JSONObject().put("relay", p.relay).put("channel", p.channel)
                    .put("hostname", p.hostname).put("host_pub", p.hostPub).put("host_kx", p.hostKx)
                    .put("phone_token", p.phoneToken).put("sign_alias", p.signAlias).put("kx_alias", p.kxAlias)
                    .put("after", p.after).toString()).apply();
        } catch (JSONException e) {
            throw new IllegalStateException(e);
        }
    }

    synchronized void clear() {
        prefs.edit().remove("pairing").apply();
    }
}

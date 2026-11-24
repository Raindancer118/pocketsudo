package de.raindancer118.pocketsudo;

import de.raindancer118.pocketcore.Protocol;

import android.content.Context;
import android.content.SharedPreferences;

import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

import java.util.ArrayList;
import java.util.List;

/** Pairings, history and small settings in SharedPreferences (excluded from backup). */
public final class Store {
    private static final int MAX_HISTORY = 100;
    private final SharedPreferences prefs;

    public Store(Context ctx) {
        prefs = ctx.getApplicationContext().getSharedPreferences("pocketsudo", Context.MODE_PRIVATE);
    }

    public static final class Pairing {
        public String relay, channel, hostname, hostPub, phoneToken, keyAlias;
        public long paired;
        public boolean keyInvalid;
        /** The relay no longer knows this channel (unpaired on the computer). */
        public boolean gone;

        JSONObject toJson() throws JSONException {
            return new JSONObject().put("relay", relay).put("channel", channel).put("hostname", hostname)
                    .put("host_pub", hostPub).put("phone_token", phoneToken).put("key_alias", keyAlias)
                    .put("paired", paired).put("key_invalid", keyInvalid).put("gone", gone);
        }

        static Pairing fromJson(JSONObject o) {
            Pairing p = new Pairing();
            p.relay = o.optString("relay");
            p.channel = o.optString("channel");
            p.hostname = o.optString("hostname");
            p.hostPub = o.optString("host_pub");
            p.phoneToken = o.optString("phone_token");
            p.keyAlias = o.optString("key_alias");
            p.paired = o.optLong("paired");
            p.keyInvalid = o.optBoolean("key_invalid");
            p.gone = o.optBoolean("gone");
            return p;
        }
    }

    public static final class Entry {
        public long time;
        public String hostname, user, cwd, command, outcome;

        JSONObject toJson() throws JSONException {
            return new JSONObject().put("time", time).put("hostname", hostname).put("user", user)
                    .put("cwd", cwd).put("command", command).put("outcome", outcome);
        }

        static Entry fromJson(JSONObject o) {
            Entry e = new Entry();
            e.time = o.optLong("time");
            e.hostname = o.optString("hostname");
            e.user = o.optString("user");
            e.cwd = o.optString("cwd");
            e.command = o.optString("command");
            e.outcome = o.optString("outcome");
            return e;
        }
    }

    public static final String OUT_APPROVED = "approved";
    public static final String OUT_DENIED = "denied";
    public static final String OUT_EXPIRED = "expired";
    public static final String OUT_ELSEWHERE = "elsewhere";
    public static final String OUT_BAD_SIG = "bad_signature";

    public synchronized List<Pairing> pairings() {
        List<Pairing> out = new ArrayList<>();
        try {
            JSONArray a = new JSONArray(prefs.getString("pairings", "[]"));
            for (int i = 0; i < a.length(); i++) out.add(Pairing.fromJson(a.getJSONObject(i)));
        } catch (JSONException ignored) {
        }
        return out;
    }

    public synchronized Pairing pairing(String channel) {
        for (Pairing p : pairings()) if (p.channel.equals(channel)) return p;
        return null;
    }

    public synchronized void savePairing(Pairing p) {
        List<Pairing> all = pairings();
        all.removeIf(q -> q.channel.equals(p.channel));
        all.add(p);
        writePairings(all);
    }

    public synchronized void removePairing(String channel) {
        List<Pairing> all = pairings();
        all.removeIf(q -> q.channel.equals(channel));
        writePairings(all);
    }

    private void writePairings(List<Pairing> all) {
        JSONArray a = new JSONArray();
        try {
            for (Pairing p : all) a.put(p.toJson());
        } catch (JSONException e) {
            throw new IllegalStateException(e);
        }
        prefs.edit().putString("pairings", a.toString()).apply();
    }

    public synchronized List<Entry> history() {
        List<Entry> out = new ArrayList<>();
        try {
            JSONArray a = new JSONArray(prefs.getString("history", "[]"));
            for (int i = 0; i < a.length(); i++) out.add(Entry.fromJson(a.getJSONObject(i)));
        } catch (JSONException ignored) {
        }
        return out;
    }

    /** Newest first. One entry per request id, so a later outcome replaces an earlier one. */
    public synchronized void record(Protocol.Request r, String outcome) {
        try {
            JSONArray old = new JSONArray(prefs.getString("history", "[]"));
            JSONArray a = new JSONArray();
            Entry e = new Entry();
            e.time = System.currentTimeMillis();
            e.hostname = r.hostname;
            e.user = r.user;
            e.cwd = r.cwd;
            e.command = r.command;
            e.outcome = outcome;
            a.put(e.toJson().put("id", r.id));
            for (int i = 0; i < old.length() && a.length() < MAX_HISTORY; i++) {
                JSONObject o = old.getJSONObject(i);
                if (!r.id.equals(o.optString("id"))) a.put(o);
            }
            prefs.edit().putString("history", a.toString()).apply();
        } catch (JSONException ignored) {
        }
    }

    /** 0.2.x kept Firebase options and the theme here; they live in pocketcore since 0.3. */
    public void migrateToCore(Context ctx) {
        android.content.SharedPreferences core = ctx.getSharedPreferences("pocketcore", Context.MODE_PRIVATE);
        android.content.SharedPreferences.Editor e = core.edit();
        boolean changed = false;
        if (!core.contains("fcm_options") && prefs.contains("fcm_options")) {
            e.putString("fcm_options", prefs.getString("fcm_options", ""));
            changed = true;
        }
        if (!core.contains("theme_mode") && prefs.contains("theme_mode")) {
            e.putInt("theme_mode", prefs.getInt("theme_mode", -1));
            changed = true;
        }
        if (changed) e.apply();
    }
}

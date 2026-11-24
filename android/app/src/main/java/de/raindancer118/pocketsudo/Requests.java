package de.raindancer118.pocketsudo;

import de.raindancer118.pocketcore.Protocol;
import de.raindancer118.pocketcore.Relay;

import android.app.NotificationManager;
import android.app.PendingIntent;
import android.content.Context;
import android.content.Intent;
import android.os.Build;
import android.util.Log;
import android.widget.RemoteViews;

import androidx.core.app.NotificationCompat;

import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

import java.io.IOException;
import java.security.GeneralSecurityException;
import java.util.ArrayList;
import java.util.List;

/** Fetching, verifying, notifying and answering requests. Network calls block. */
public final class Requests {
    private static final String TAG = "PocketSudo";

    private Requests() {}

    static String base(Store.Pairing p) {
        return "/v1/channels/" + p.channel + "/requests";
    }

    /** Fetches and verifies one request and tells the host the phone has it. */
    public static Protocol.Request fetch(Store.Pairing p, String id) throws IOException, GeneralSecurityException {
        JSONObject o = PairingRelay.of(p).get(base(p) + "/" + id);
        Protocol.Request r = verified(p, o);
        if (!r.id.equals(id)) throw new SecurityException("request id mismatch");
        try {
            PairingRelay.of(p).post(base(p) + "/" + id + "/seen", null);
        } catch (IOException e) {
            Log.w(TAG, "seen", e);
        }
        App.SEEN.put(r.id, r);
        return r;
    }

    private static Protocol.Request verified(Store.Pairing p, JSONObject o) throws GeneralSecurityException {
        try {
            Protocol.Request r = Protocol.verifyRequest(p.hostPub, o.getString("payload"), o.getString("sig"));
            if (!p.channel.equals(r.channel)) throw new SecurityException("request for another channel");
            return r;
        } catch (JSONException e) {
            throw new SecurityException("malformed request", e);
        }
    }

    /** All open requests across pairings (used when the app opens, e.g. without push). */
    public static List<Pair> pending(Context ctx) {
        List<Pair> out = new ArrayList<>();
        for (Store.Pairing p : new Store(ctx).pairings()) {
            try {
                JSONArray a = (JSONArray) PairingRelay.of(p).call("GET", base(p), null, 15000);
                for (int i = 0; i < a.length(); i++) {
                    try {
                        Protocol.Request r = verified(p, a.getJSONObject(i));
                        if (r.secondsLeft() > 0) {
                            App.SEEN.put(r.id, r);
                            out.add(new Pair(p, r));
                        }
                    } catch (GeneralSecurityException | JSONException e) {
                        Log.w(TAG, "dropping bad request", e);
                    }
                }
            } catch (Relay.HttpException e) {
                if (e.code == 401 && !p.gone) {
                    p.gone = true;
                    new Store(ctx).savePairing(p);
                }
                Log.w(TAG, "pending " + p.hostname, e);
            } catch (IOException | ClassCastException e) {
                Log.w(TAG, "pending " + p.hostname, e);
            }
        }
        return out;
    }

    public static final class Pair {
        public final Store.Pairing pairing;
        public final Protocol.Request request;

        Pair(Store.Pairing p, Protocol.Request r) {
            pairing = p;
            request = r;
        }
    }

    public static void approve(Store.Pairing p, Protocol.Request r, byte[] signature) throws IOException {
        JSONObject body = new JSONObject();
        try {
            body.put("decision", Protocol.APPROVE).put("sig", java.util.Base64.getEncoder().encodeToString(signature));
        } catch (JSONException e) {
            throw new IOException(e);
        }
        PairingRelay.of(p).post(base(p) + "/" + r.id + "/response", body);
    }

    /** Denials are unsigned on purpose; see relay.respond. */
    public static void deny(Store.Pairing p, String id) throws IOException {
        JSONObject body = new JSONObject();
        try {
            body.put("decision", Protocol.DENY).put("sig", "");
        } catch (JSONException e) {
            throw new IOException(e);
        }
        PairingRelay.of(p).post(base(p) + "/" + id + "/response", body);
    }

    // ---- notifications ----

    static int notificationId(String requestId) {
        return requestId.hashCode();
    }

    static Intent approvalIntent(Context ctx, String channel, String id) {
        return new Intent(ctx, ApprovalActivity.class)
                .putExtra(ApprovalActivity.EXTRA_CHANNEL, channel)
                .putExtra(ApprovalActivity.EXTRA_ID, id)
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK | Intent.FLAG_ACTIVITY_SINGLE_TOP);
    }

    /**
     * Shown before the request is even fetched: FCM only keeps high priority
     * for messages that lead to a visible notification right away.
     */
    static void notifyPlaceholder(Context ctx, Store.Pairing p, String id) {
        if (App.HANDLED.contains(id)) return;
        show(ctx, p, id, ctx.getString(R.string.notif_from, p.hostname), ctx.getString(R.string.notif_loading), 120, null);
    }

    /** Large layout: the command is readable in the heads-up already, the code fills the expanded view. */
    static void notifyRequest(Context ctx, Store.Pairing p, Protocol.Request r) {
        if (App.HANDLED.contains(r.id)) {
            cancelNotification(ctx, r.id);
            return;
        }
        String title = ctx.getString(R.string.notif_title, r.service, r.hostname);
        String cmd = "$ " + (r.command.isEmpty() ? r.service : r.command);
        String text = cmd + "   ·   " + ctx.getString(R.string.notif_code, r.code);
        String head = r.remote ? title + "  ·  " + ctx.getString(R.string.ssh) : title;

        RemoteViews small = new RemoteViews(ctx.getPackageName(), R.layout.notif_small);
        small.setTextViewText(R.id.n_title, head + "  ·  " + ctx.getString(R.string.notif_code, r.code));
        small.setTextViewText(R.id.n_command, cmd);
        RemoteViews big = new RemoteViews(ctx.getPackageName(), R.layout.notif_big);
        big.setTextViewText(R.id.n_title, head);
        big.setTextViewText(R.id.n_command, cmd);
        big.setTextViewText(R.id.n_code, r.code);
        big.setTextViewText(R.id.n_hint, ctx.getString(R.string.code_hint));
        show(ctx, p, r.id, title, text, Math.max(1, r.secondsLeft()), new RemoteViews[]{small, big});
    }

    private static void show(Context ctx, Store.Pairing p, String id, String title, String text, long seconds,
                             RemoteViews[] custom) {
        int nid = notificationId(id);
        int flags = PendingIntent.FLAG_UPDATE_CURRENT | PendingIntent.FLAG_IMMUTABLE;
        PendingIntent open = PendingIntent.getActivity(ctx, nid, approvalIntent(ctx, p.channel, id), flags);
        PendingIntent deny = PendingIntent.getBroadcast(ctx, nid,
                new Intent(ctx, DenyReceiver.class).putExtra(ApprovalActivity.EXTRA_CHANNEL, p.channel)
                        .putExtra(ApprovalActivity.EXTRA_ID, id), flags);
        NotificationCompat.Builder b = new NotificationCompat.Builder(ctx, App.CHANNEL_REQUESTS)
                .setSmallIcon(R.drawable.ic_notify)
                .setContentTitle(title)
                .setContentText(text)
                .setStyle(custom == null ? new NotificationCompat.BigTextStyle().bigText(text)
                        : new NotificationCompat.DecoratedCustomViewStyle())
                .setPriority(NotificationCompat.PRIORITY_MAX)
                .setCategory(NotificationCompat.CATEGORY_CALL)
                .setVisibility(NotificationCompat.VISIBILITY_PRIVATE)
                .setOnlyAlertOnce(true)
                .setAutoCancel(true)
                .setTimeoutAfter(seconds * 1000)
                .setContentIntent(open)
                .addAction(0, ctx.getString(R.string.deny), deny)
                .addAction(0, ctx.getString(R.string.approve), open);
        // lock screen: host only, the command stays private until unlocked
        b.setPublicVersion(new NotificationCompat.Builder(ctx, App.CHANNEL_REQUESTS)
                .setSmallIcon(R.drawable.ic_notify).setContentTitle(title).build());
        if (custom != null) {
            b.setCustomContentView(custom[0]).setCustomHeadsUpContentView(custom[0]).setCustomBigContentView(custom[1]);
        }
        NotificationManager nm = ctx.getSystemService(NotificationManager.class);
        if (Build.VERSION.SDK_INT < 34 || nm.canUseFullScreenIntent()) b.setFullScreenIntent(open, true);
        try {
            nm.notify(nid, b.build());
        } catch (SecurityException e) {
            Log.w(TAG, "notify", e);
        }
    }

    /** Removes notifications of requests that were decided, cancelled or expired meanwhile. */
    static void sweepNotifications(Context ctx, List<Pair> pending) {
        java.util.Set<Integer> keep = new java.util.HashSet<>();
        for (Pair p : pending) keep.add(notificationId(p.request.id));
        NotificationManager nm = ctx.getSystemService(NotificationManager.class);
        for (android.service.notification.StatusBarNotification n : nm.getActiveNotifications()) {
            if (App.CHANNEL_REQUESTS.equals(n.getNotification().getChannelId()) && !keep.contains(n.getId())) {
                nm.cancel(n.getId());
            }
        }
    }

    static void cancelNotification(Context ctx, String id) {
        ctx.getSystemService(NotificationManager.class).cancel(notificationId(id));
    }
}

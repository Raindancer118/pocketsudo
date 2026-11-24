package de.raindancer118.pocketdeck;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.content.BroadcastReceiver;
import android.content.Context;
import android.content.Intent;
import android.net.Uri;
import android.text.format.Formatter;
import android.util.Log;

import org.json.JSONObject;

/**
 * Files the computer sends ("pocketdeck send"). Nothing is stored until Tom
 * accepts: the offer shows as a notification with Accept / Decline; only then
 * the file is downloaded to Downloads/PocketHub and a second notification opens it.
 * Unanswered offers simply expire on the relay after 15 minutes.
 */
public final class Incoming extends BroadcastReceiver {
    static final String CHANNEL = "files";
    // offers pop up on screen; a channel's importance can't be raised later, hence its own channel
    static final String OFFERS = "file_offers";
    private static final String ACCEPT = "de.raindancer118.pocketdeck.ACCEPT_FILE";
    private static final String DECLINE = "de.raindancer118.pocketdeck.DECLINE_FILE";

    static void createChannel(Context ctx) {
        NotificationChannel ch = new NotificationChannel(CHANNEL, ctx.getString(R.string.files_channel),
                NotificationManager.IMPORTANCE_DEFAULT);
        NotificationChannel offers = new NotificationChannel(OFFERS, ctx.getString(R.string.file_offers_channel),
                NotificationManager.IMPORTANCE_HIGH);
        NotificationManager nm = ctx.getSystemService(NotificationManager.class);
        nm.createNotificationChannel(ch);
        nm.createNotificationChannel(offers);
    }

    /** An offer arrived: ask first. */
    static void file(Context ctx, Deck deck, JSONObject body) {
        String blob = body.optString("blob"), name = body.optString("name", "file");
        if (blob.isEmpty()) return;
        int id = blob.hashCode();
        String size = Formatter.formatShortFileSize(ctx, body.optLong("size"));
        Notification.Builder b = new Notification.Builder(ctx, OFFERS).setSmallIcon(R.drawable.ic_launcher_mono)
                .setCategory(Notification.CATEGORY_MESSAGE)
                .setContentTitle(ctx.getString(R.string.file_offer_title, deck.hostName()))
                .setContentText(ctx.getString(R.string.file_offer_text, name, size))
                .setAutoCancel(false)
                .setTimeoutAfter(15 * 60 * 1000L) // the relay drops it then anyway
                .addAction(new Notification.Action.Builder(null, ctx.getString(R.string.file_accept), action(ctx, ACCEPT, id, blob, name)).build())
                .addAction(new Notification.Action.Builder(null, ctx.getString(R.string.file_decline), action(ctx, DECLINE, id, blob, name)).build());
        android.graphics.Bitmap thumb = thumb(body.optString("thumb"));
        if (thumb != null) {
            // pictures show what is coming before it is accepted
            b.setLargeIcon(thumb).setStyle(new Notification.BigPictureStyle().bigPicture(thumb)
                    .setSummaryText(ctx.getString(R.string.file_offer_text, name, size)));
        }
        notify(ctx, id, b);
    }

    private static android.graphics.Bitmap thumb(String b64) {
        if (b64.isEmpty()) return null;
        try {
            byte[] jpg = android.util.Base64.decode(b64, android.util.Base64.DEFAULT);
            return android.graphics.BitmapFactory.decodeByteArray(jpg, 0, jpg.length);
        } catch (IllegalArgumentException e) {
            return null;
        }
    }

    private static PendingIntent action(Context ctx, String what, int id, String blob, String name) {
        Intent i = new Intent(what).setClass(ctx, Incoming.class).putExtra("id", id).putExtra("blob", blob).putExtra("name", name);
        // request code per offer and action, so two offers never share extras
        return PendingIntent.getBroadcast(ctx, id * 2 + (ACCEPT.equals(what) ? 1 : 0), i,
                PendingIntent.FLAG_IMMUTABLE | PendingIntent.FLAG_UPDATE_CURRENT);
    }

    @Override
    public void onReceive(Context ctx, Intent intent) {
        int id = intent.getIntExtra("id", 0);
        NotificationManager nm = ctx.getSystemService(NotificationManager.class);
        nm.cancel(id);
        if (!ACCEPT.equals(intent.getAction())) return; // declined: it expires on the relay
        String blob = intent.getStringExtra("blob"), name = intent.getStringExtra("name");
        if (blob == null || name == null) return;
        PendingResult async = goAsync();
        Deck deck = Deck.get(ctx);
        notify(ctx, id, builder(ctx).setContentTitle(ctx.getString(R.string.file_loading, name)).setProgress(0, 0, true));
        deck.io().execute(() -> {
            try {
                Uri uri = Transfer.saveDownload(ctx, name, deck.download(blob));
                Intent open = new Intent(Intent.ACTION_VIEW).setDataAndType(uri, Transfer.mime(name))
                        .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION | Intent.FLAG_ACTIVITY_NEW_TASK);
                PendingIntent pi = PendingIntent.getActivity(ctx, id, open, PendingIntent.FLAG_IMMUTABLE | PendingIntent.FLAG_UPDATE_CURRENT);
                notify(ctx, id, builder(ctx).setContentTitle(ctx.getString(R.string.file_received, deck.hostName()))
                        .setContentText(name).setContentIntent(pi));
            } catch (Exception e) {
                // usually expired: the relay keeps files for 15 minutes
                Log.w("PocketDeck", "incoming file", e);
                notify(ctx, id, builder(ctx).setContentTitle(ctx.getString(R.string.file_receive_failed, name))
                        .setContentText(String.valueOf(e.getMessage())));
            } finally {
                async.finish();
            }
        });
    }

    private static Notification.Builder builder(Context ctx) {
        return new Notification.Builder(ctx, CHANNEL).setSmallIcon(R.drawable.ic_launcher_mono).setAutoCancel(true);
    }

    private static void notify(Context ctx, int id, Notification.Builder b) {
        NotificationManager nm = ctx.getSystemService(NotificationManager.class);
        if (nm.areNotificationsEnabled()) nm.notify(id, b.build());
    }
}

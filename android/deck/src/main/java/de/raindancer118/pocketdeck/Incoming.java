package de.raindancer118.pocketdeck;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.content.Context;
import android.content.Intent;
import android.net.Uri;
import android.util.Log;

import org.json.JSONObject;

/**
 * Files the computer sends ("pocketdeck send"): downloaded right away, stored
 * in Downloads/PocketDeck, announced with a notification that opens the file.
 */
final class Incoming {
    static final String CHANNEL = "files";

    private Incoming() {
    }

    static void createChannel(Context ctx) {
        NotificationChannel ch = new NotificationChannel(CHANNEL, ctx.getString(R.string.files_channel),
                NotificationManager.IMPORTANCE_DEFAULT);
        ctx.getSystemService(NotificationManager.class).createNotificationChannel(ch);
    }

    static void file(Context ctx, Deck deck, JSONObject body) {
        String blob = body.optString("blob"), name = body.optString("name", "file");
        if (blob.isEmpty()) return;
        deck.io().execute(() -> {
            int id = name.hashCode() ^ blob.hashCode();
            try {
                Uri uri = Transfer.saveDownload(ctx, name, deck.download(blob));
                Intent open = new Intent(Intent.ACTION_VIEW).setDataAndType(uri, Transfer.mime(name))
                        .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION | Intent.FLAG_ACTIVITY_NEW_TASK);
                PendingIntent pi = PendingIntent.getActivity(ctx, id, open, PendingIntent.FLAG_IMMUTABLE | PendingIntent.FLAG_UPDATE_CURRENT);
                post(ctx, id, ctx.getString(R.string.file_received, deck.hostName()), name, pi);
            } catch (Exception e) {
                // usually expired: the relay keeps files for 15 minutes
                Log.w("PocketDeck", "incoming file", e);
                post(ctx, id, ctx.getString(R.string.file_receive_failed, name), String.valueOf(e.getMessage()), null);
            }
        });
    }

    private static void post(Context ctx, int id, String title, String text, PendingIntent pi) {
        NotificationManager nm = ctx.getSystemService(NotificationManager.class);
        if (!nm.areNotificationsEnabled()) return;
        Notification.Builder b = new Notification.Builder(ctx, CHANNEL)
                .setSmallIcon(R.drawable.ic_launcher_mono)
                .setContentTitle(title)
                .setContentText(text)
                .setAutoCancel(true);
        if (pi != null) b.setContentIntent(pi);
        nm.notify(id, b.build());
    }
}

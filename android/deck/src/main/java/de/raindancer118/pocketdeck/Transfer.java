package de.raindancer118.pocketdeck;

import android.content.ContentValues;
import android.content.Context;
import android.database.Cursor;
import android.net.Uri;
import android.os.Environment;
import android.os.Handler;
import android.os.Looper;
import android.provider.MediaStore;
import android.provider.OpenableColumns;

import org.json.JSONException;
import org.json.JSONObject;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;

/** Files to the computer and screenshots from it. */
final class Transfer {
    static final int MAX_BYTES = 24 << 20; // relay limit is 25 MB including the seal

    private Transfer() {}

    static void sendFile(Context ctx, Uri uri, Deck.Callback cb) {
        Deck deck = Deck.get(ctx);
        Handler main = new Handler(Looper.getMainLooper());
        deck.io().execute(() -> {
            try {
                String name = displayName(ctx, uri);
                byte[] data = readAll(ctx, uri);
                String blob = deck.upload(data);
                String body = new JSONObject().put("blob", blob).put("name", name).toString();
                main.post(() -> deck.send("file.put", body, cb));
            } catch (Exception e) {
                main.post(() -> cb.done(false, e.getMessage(), null));
            }
        });
    }

    static String displayName(Context ctx, Uri uri) {
        try (Cursor c = ctx.getContentResolver().query(uri, new String[]{OpenableColumns.DISPLAY_NAME}, null, null, null)) {
            if (c != null && c.moveToFirst()) return c.getString(0);
        } catch (RuntimeException ignored) {
        }
        String last = uri.getLastPathSegment();
        return last == null ? "datei" : last;
    }

    static byte[] readAll(Context ctx, Uri uri) throws IOException {
        try (InputStream in = ctx.getContentResolver().openInputStream(uri); ByteArrayOutputStream out = new ByteArrayOutputStream()) {
            if (in == null) throw new IOException("cannot read " + uri);
            byte[] buf = new byte[64 << 10];
            int n;
            while ((n = in.read(buf)) > 0) {
                out.write(buf, 0, n);
                if (out.size() > MAX_BYTES) throw new IOException(ctx.getString(R.string.file_too_large));
            }
            return out.toByteArray();
        }
    }

    /** Stores a JPEG in Pictures/PocketDeck and returns its content uri. */
    static Uri saveScreenshot(Context ctx, byte[] jpeg, String host) throws IOException {
        ContentValues v = new ContentValues();
        v.put(MediaStore.Images.Media.DISPLAY_NAME, host + "-" + System.currentTimeMillis() + ".jpg");
        v.put(MediaStore.Images.Media.MIME_TYPE, "image/jpeg");
        v.put(MediaStore.Images.Media.RELATIVE_PATH, Environment.DIRECTORY_PICTURES + "/PocketDeck");
        Uri uri = ctx.getContentResolver().insert(MediaStore.Images.Media.EXTERNAL_CONTENT_URI, v);
        if (uri == null) throw new IOException("gallery not available");
        try (OutputStream out = ctx.getContentResolver().openOutputStream(uri)) {
            if (out == null) throw new IOException("gallery not writable");
            out.write(jpeg);
        }
        return uri;
    }

    static String blobOf(String data) throws JSONException {
        return new JSONObject(data).getString("blob");
    }
}

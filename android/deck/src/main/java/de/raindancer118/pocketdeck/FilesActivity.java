package de.raindancer118.pocketdeck;

import android.content.ContentValues;
import android.content.Intent;
import android.net.Uri;
import android.os.Bundle;
import android.os.Environment;
import android.provider.MediaStore;
import android.text.format.DateUtils;
import android.text.format.Formatter;
import android.view.LayoutInflater;
import android.view.View;
import android.webkit.MimeTypeMap;
import android.widget.LinearLayout;
import android.widget.TextView;

import androidx.activity.result.ActivityResultLauncher;
import androidx.activity.result.contract.ActivityResultContracts;
import androidx.appcompat.app.AppCompatActivity;

import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

import java.io.OutputStream;
import java.util.Locale;

/** Browse the home folder of the computer, download files, upload into the open folder. */
public class FilesActivity extends AppCompatActivity {
    private Deck deck;
    private String path = "";
    private LinearLayout list;
    private TextView where, message, upload;
    private View closed;
    private ActivityResultLauncher<String[]> pick;

    @Override
    protected void onCreate(Bundle s) {
        super.onCreate(s);
        setContentView(R.layout.activity_files);
        deck = Deck.get(this);
        list = findViewById(R.id.list);
        where = findViewById(R.id.where);
        message = findViewById(R.id.message);
        upload = findViewById(R.id.upload);
        closed = findViewById(R.id.closed);
        de.raindancer118.pocketcore.Ui.link(upload);
        findViewById(R.id.back).setOnClickListener(v -> up());
        findViewById(R.id.open).setOnClickListener(v -> Session.open(this, (ok, err) -> {
            if (ok) load(path);
            else say(err);
        }));
        pick = registerForActivityResult(new ActivityResultContracts.OpenDocument(), this::uploadHere);
        upload.setOnClickListener(v -> pick.launch(new String[]{"*/*"}));
        // back goes one folder up before leaving the browser
        getOnBackPressedDispatcher().addCallback(this, new androidx.activity.OnBackPressedCallback(true) {
            @Override
            public void handleOnBackPressed() {
                up();
            }
        });
    }

    @Override
    protected void onResume() {
        super.onResume();
        deck.startPolling();
        load(path);
    }


    private void up() {
        if (path.isEmpty()) {
            finish();
            return;
        }
        int i = path.lastIndexOf('/');
        load(i < 0 ? "" : path.substring(0, i));
    }

    private void load(String p) {
        deck.send("fs.list", json("path", p), (ok, err, data) -> {
            if (!ok) {
                boolean needsSession = Session.isSessionError(err);
                closed.setVisibility(needsSession ? View.VISIBLE : View.GONE);
                upload.setVisibility(View.GONE);
                if (needsSession) list.removeAllViews();
                else say(err);
                return;
            }
            closed.setVisibility(View.GONE);
            upload.setVisibility(View.VISIBLE);
            say(null);
            try {
                show(new JSONObject(data));
            } catch (JSONException e) {
                say(e.getMessage());
            }
        });
    }

    private void show(JSONObject ls) throws JSONException {
        path = ls.optString("path");
        where.setText(path.isEmpty() ? "~" : "~/" + path);
        list.removeAllViews();
        JSONArray entries = ls.getJSONArray("entries");
        LayoutInflater inf = LayoutInflater.from(this);
        for (int i = 0; i < entries.length(); i++) {
            JSONObject e = entries.getJSONObject(i);
            View row = inf.inflate(R.layout.item_file, list, false);
            TextView name = row.findViewById(R.id.name), meta = row.findViewById(R.id.meta);
            boolean dir = e.getBoolean("dir");
            String n = e.getString("name");
            name.setText(n);
            name.setCompoundDrawablesRelativeWithIntrinsicBounds(dir ? R.drawable.ic_folder : R.drawable.ic_file, 0, 0, 0);
            String when = DateUtils.getRelativeTimeSpanString(e.getLong("mtime") * 1000).toString();
            meta.setText(dir ? when : Formatter.formatShortFileSize(this, e.getLong("size")) + " · " + when);
            String child = path.isEmpty() ? n : path + "/" + n;
            row.setOnClickListener(v -> {
                if (dir) load(child);
                else download(child, n);
            });
            list.addView(row);
        }
        if (entries.length() == 0) {
            TextView empty = new TextView(this, null, 0, R.style.Text_Small);
            empty.setText(R.string.folder_empty);
            empty.setPadding(0, 48, 0, 0);
            empty.setGravity(android.view.Gravity.CENTER);
            list.addView(empty);
        }
    }

    private void download(String rel, String name) {
        say(getString(R.string.downloading, name));
        deck.send("fs.get", json("path", rel), (ok, err, data) -> {
            if (!ok) {
                say(err);
                return;
            }
            deck.io().execute(() -> {
                try {
                    byte[] bytes = deck.download(Transfer.blobOf(data));
                    Uri uri = saveDownload(name, bytes);
                    runOnUiThread(() -> {
                        say(getString(R.string.downloaded, name));
                        Intent view = new Intent(Intent.ACTION_VIEW).setDataAndType(uri, mime(name))
                                .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION);
                        try {
                            startActivity(view);
                        } catch (android.content.ActivityNotFoundException ignored) {
                        }
                    });
                } catch (Exception e) {
                    runOnUiThread(() -> say(e.getMessage()));
                }
            });
        });
    }

    private Uri saveDownload(String name, byte[] bytes) throws java.io.IOException {
        ContentValues v = new ContentValues();
        v.put(MediaStore.Downloads.DISPLAY_NAME, name);
        v.put(MediaStore.Downloads.MIME_TYPE, mime(name));
        v.put(MediaStore.Downloads.RELATIVE_PATH, Environment.DIRECTORY_DOWNLOADS + "/PocketDeck");
        Uri uri = getContentResolver().insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI, v);
        if (uri == null) throw new java.io.IOException("downloads not available");
        try (OutputStream out = getContentResolver().openOutputStream(uri)) {
            if (out == null) throw new java.io.IOException("downloads not writable");
            out.write(bytes);
        }
        return uri;
    }

    private void uploadHere(Uri uri) {
        if (uri == null) return;
        say(getString(R.string.file_sending));
        String dir = path;
        deck.io().execute(() -> {
            try {
                String name = Transfer.displayName(this, uri);
                String blob = deck.upload(Transfer.readAll(this, uri));
                String body = new JSONObject().put("blob", blob).put("name", name).put("dir", dir.isEmpty() ? "." : dir).toString();
                runOnUiThread(() -> deck.send("file.put", body, (ok, err, data) -> {
                    say(ok ? getString(R.string.file_sent) : err);
                    if (ok) load(path);
                }));
            } catch (Exception e) {
                runOnUiThread(() -> say(e.getMessage()));
            }
        });
    }

    private static String mime(String name) {
        int dot = name.lastIndexOf('.');
        String m = dot < 0 ? null : MimeTypeMap.getSingleton().getMimeTypeFromExtension(name.substring(dot + 1).toLowerCase(Locale.ROOT));
        return m == null ? "application/octet-stream" : m;
    }

    private static String json(String k, String v) {
        try {
            return new JSONObject().put(k, v).toString();
        } catch (JSONException e) {
            return "{}";
        }
    }

    private void say(String s) {
        message.setVisibility(s == null ? View.GONE : View.VISIBLE);
        message.setText(s);
    }
}

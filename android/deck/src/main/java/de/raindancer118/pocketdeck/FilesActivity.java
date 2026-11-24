package de.raindancer118.pocketdeck;

import android.content.ContentValues;
import android.content.Intent;
import android.net.Uri;
import android.os.Bundle;
import android.os.Environment;
import android.os.Handler;
import android.os.Looper;
import android.provider.MediaStore;
import android.text.Editable;
import android.text.TextWatcher;
import android.text.format.DateUtils;
import android.text.format.Formatter;
import android.view.LayoutInflater;
import android.view.View;
import android.webkit.MimeTypeMap;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.TextView;

import androidx.activity.OnBackPressedCallback;
import androidx.activity.result.ActivityResultLauncher;
import androidx.activity.result.contract.ActivityResultContracts;
import androidx.appcompat.app.AppCompatActivity;

import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

import java.io.OutputStream;
import java.util.LinkedHashSet;
import java.util.Locale;
import java.util.Set;

import de.raindancer118.pocketcore.Ui;

/**
 * Browse the computer's home folder: tap for details, hold to select
 * (files and folders, downloaded as one zip), search below the open folder,
 * upload into it.
 */
public class FilesActivity extends AppCompatActivity {
    private Deck deck;
    private String path = "";
    private LinearLayout list;
    private TextView where, message, upload, selectAction;
    private EditText search;
    private View closed;
    private ActivityResultLauncher<String[]> pick;
    private final Set<String> selected = new LinkedHashSet<>();
    private final Handler main = new Handler(Looper.getMainLooper());
    private final Runnable runSearch = this::doSearch;

    @Override
    protected void onCreate(Bundle s) {
        super.onCreate(s);
        setContentView(R.layout.activity_files);
        deck = Deck.get(this);
        list = findViewById(R.id.list);
        where = findViewById(R.id.where);
        message = findViewById(R.id.message);
        upload = findViewById(R.id.upload);
        selectAction = findViewById(R.id.select_action);
        search = findViewById(R.id.search);
        closed = findViewById(R.id.closed);
        Ui.link(upload, selectAction);
        findViewById(R.id.back).setOnClickListener(v -> back());
        findViewById(R.id.open).setOnClickListener(v -> Session.open(this, (ok, err) -> {
            if (ok) load(path);
            else say(err);
        }));
        pick = registerForActivityResult(new ActivityResultContracts.OpenDocument(), this::uploadHere);
        upload.setOnClickListener(v -> pick.launch(new String[]{"*/*"}));
        selectAction.setOnClickListener(v -> downloadSelection());
        search.addTextChangedListener(new TextWatcher() {
            @Override
            public void beforeTextChanged(CharSequence c, int a, int b, int d) {
            }

            @Override
            public void onTextChanged(CharSequence c, int a, int b, int d) {
            }

            @Override
            public void afterTextChanged(Editable e) {
                main.removeCallbacks(runSearch);
                main.postDelayed(runSearch, 400);
            }
        });
        getOnBackPressedDispatcher().addCallback(this, new OnBackPressedCallback(true) {
            @Override
            public void handleOnBackPressed() {
                back();
            }
        });
    }

    @Override
    protected void onResume() {
        super.onResume();
        deck.startPolling();
        load(path);
    }

    /** Back: leave selection, then search, then go one folder up, then close. */
    private void back() {
        if (!selected.isEmpty()) {
            selected.clear();
            android.transition.TransitionManager.beginDelayedTransition(list,
                    new android.transition.AutoTransition().setDuration(180));
            markAll();
            renderSelection();
        } else if (search.getText().length() > 0) {
            search.setText("");
        } else if (!path.isEmpty()) {
            int i = path.lastIndexOf('/');
            load(i < 0 ? "" : path.substring(0, i));
        } else {
            finish();
        }
    }

    private void load(String p) {
        deck.send("fs.list", json("path", p), (ok, err, data) -> {
            if (!ok) {
                boolean needsSession = Session.isSessionError(err);
                closed.setVisibility(needsSession ? View.VISIBLE : View.GONE);
                search.setVisibility(needsSession ? View.GONE : View.VISIBLE);
                upload.setVisibility(View.GONE);
                if (needsSession) list.removeAllViews();
                else say(err);
                return;
            }
            closed.setVisibility(View.GONE);
            search.setVisibility(View.VISIBLE);
            say(null);
            try {
                JSONObject ls = new JSONObject(data);
                path = ls.optString("path");
                where.setText(path.isEmpty() ? "~" : "~/" + path);
                show(ls.getJSONArray("entries"), false);
            } catch (JSONException e) {
                say(e.getMessage());
            }
        });
    }

    private void doSearch() {
        String q = search.getText().toString().trim();
        if (q.length() < 2) {
            if (q.isEmpty()) load(path);
            return;
        }
        deck.send("fs.search", jsonOf("path", path, "query", q), (ok, err, data) -> {
            if (!ok) {
                say(err);
                return;
            }
            try {
                JSONArray r = new JSONObject(data).getJSONArray("results");
                show(r, true);
                if (r.length() == 0) say(getString(R.string.no_results));
            } catch (JSONException e) {
                say(e.getMessage());
            }
        });
    }

    /** entries from fs.list (name relative to path) or fs.search (path relative to home). */
    private void show(JSONArray entries, boolean fromSearch) throws JSONException {
        list.removeAllViews();
        LayoutInflater inf = LayoutInflater.from(this);
        for (int i = 0; i < entries.length(); i++) {
            JSONObject e = entries.getJSONObject(i);
            boolean dir = e.getBoolean("dir");
            String n = e.getString("name");
            String rel = fromSearch ? e.getString("path") : (path.isEmpty() ? n : path + "/" + n);
            View row = inf.inflate(R.layout.item_file, list, false);
            TextView name = row.findViewById(R.id.name), meta = row.findViewById(R.id.meta);
            name.setText(n);
            name.setCompoundDrawablesRelativeWithIntrinsicBounds(dir ? R.drawable.ic_folder : FileTypes.icon(n), 0, 0, 0);
            String when = DateUtils.getRelativeTimeSpanString(e.optLong("mtime") * 1000).toString();
            String info = dir ? when : Formatter.formatShortFileSize(this, e.optLong("size")) + " · " + when;
            meta.setText(fromSearch ? "~/" + rel : info);
            row.setTag(rel);
            mark(row, selected.contains(rel), !selected.isEmpty());
            row.setOnClickListener(v -> {
                if (!selected.isEmpty()) {
                    toggle(row, rel);
                } else if (dir) {
                    search.setText("");
                    load(rel);
                } else {
                    details(rel, n, info);
                }
            });
            row.setOnLongClickListener(v -> {
                row.performHapticFeedback(android.view.HapticFeedbackConstants.LONG_PRESS);
                toggle(row, rel);
                return true;
            });
            list.addView(row);
        }
        if (entries.length() == 0 && !fromSearch) {
            TextView empty = new TextView(this, null, 0, R.style.Text_Small);
            empty.setText(R.string.folder_empty);
            empty.setPadding(0, 48, 0, 0);
            empty.setGravity(android.view.Gravity.CENTER);
            list.addView(empty);
        }
        renderSelection();
    }

    // ---- selection ----

    private void toggle(View row, String rel) {
        boolean wasSelecting = !selected.isEmpty();
        if (!selected.remove(rel)) selected.add(rel);
        boolean selecting = !selected.isEmpty();
        if (wasSelecting != selecting) {
            // entering or leaving selection: every row slides and gets (or loses) its circle
            android.transition.TransitionManager.beginDelayedTransition(list,
                    new android.transition.AutoTransition().setDuration(180));
            markAll();
        } else {
            mark(row, selected.contains(rel), true);
        }
        renderSelection();
    }

    private void markAll() {
        boolean selecting = !selected.isEmpty();
        for (int i = 0; i < list.getChildCount(); i++) {
            View row = list.getChildAt(i);
            if (row.getTag() instanceof String) mark(row, selected.contains((String) row.getTag()), selecting);
        }
    }

    private void mark(View row, boolean on, boolean selecting) {
        android.widget.ImageView check = row.findViewById(R.id.check);
        check.setVisibility(selecting ? View.VISIBLE : View.GONE);
        check.setImageResource(on ? R.drawable.check_on : R.drawable.check_off);
    }

    private void renderSelection() {
        boolean selecting = !selected.isEmpty();
        selectAction.setVisibility(selecting ? View.VISIBLE : View.GONE);
        upload.setVisibility(selecting || closed.getVisibility() == View.VISIBLE ? View.GONE : View.VISIBLE);
        if (selecting) say(getResources().getQuantityString(R.plurals.selected, selected.size(), selected.size()));
        else if (message.getText().toString().matches("\\d+ .*")) say(null);
    }

    private void downloadSelection() {
        String[] paths = selected.toArray(new String[0]);
        selected.clear();
        renderSelection();
        say(getResources().getQuantityString(R.plurals.zipping, paths.length, paths.length));
        JSONObject body = new JSONObject();
        try {
            JSONArray a = new JSONArray();
            for (String p : paths) a.put(p);
            body.put("paths", a);
        } catch (JSONException ignored) {
        }
        deck.send("fs.zip", body.toString(), (ok, err, data) -> fetch(ok, err, data));
        load(path);
    }

    // ---- download / upload ----

    private void details(String rel, String name, String info) {
        Ui.confirm(this, name, info, getString(R.string.download), false, () -> {
            say(getString(R.string.downloading, name));
            deck.send("fs.get", json("path", rel), this::fetch);
        });
    }

    private void fetch(boolean ok, String err, String data) {
        if (!ok) {
            say(err);
            return;
        }
        deck.io().execute(() -> {
            try {
                JSONObject o = new JSONObject(data);
                String name = o.getString("name");
                byte[] bytes = deck.download(o.getString("blob"));
                Uri uri = saveDownload(name, bytes);
                runOnUiThread(() -> {
                    say(getString(R.string.downloaded, name));
                    try {
                        startActivity(new Intent(Intent.ACTION_VIEW).setDataAndType(uri, mime(name))
                                .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION));
                    } catch (android.content.ActivityNotFoundException ignored) {
                    }
                });
            } catch (Exception e) {
                runOnUiThread(() -> say(e.getMessage()));
            }
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
        return jsonOf(k, v);
    }

    private static String jsonOf(String... kv) {
        try {
            JSONObject o = new JSONObject();
            for (int i = 0; i + 1 < kv.length; i += 2) o.put(kv[i], kv[i + 1]);
            return o.toString();
        } catch (JSONException e) {
            return "{}";
        }
    }

    private void say(String s) {
        message.setVisibility(s == null ? View.GONE : View.VISIBLE);
        message.setText(s);
    }
}

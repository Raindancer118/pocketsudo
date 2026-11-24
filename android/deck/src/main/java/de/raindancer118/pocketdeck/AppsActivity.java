package de.raindancer118.pocketdeck;

import android.app.AlertDialog;
import android.app.Dialog;
import android.graphics.Bitmap;
import android.graphics.BitmapFactory;
import android.graphics.drawable.BitmapDrawable;
import android.graphics.drawable.ColorDrawable;
import android.graphics.drawable.Drawable;
import android.os.Bundle;
import android.text.Editable;
import android.text.TextWatcher;
import android.util.Log;
import android.view.LayoutInflater;
import android.view.View;
import android.view.inputmethod.EditorInfo;
import android.widget.EditText;
import android.widget.FrameLayout;
import android.widget.LinearLayout;
import android.widget.TextView;

import androidx.appcompat.app.AppCompatActivity;
import androidx.appcompat.content.res.AppCompatResources;

import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

import java.io.ByteArrayInputStream;
import java.io.File;
import java.io.FileOutputStream;
import java.io.IOException;
import java.util.ArrayList;
import java.util.List;
import java.util.Locale;
import java.util.zip.ZipEntry;
import java.util.zip.ZipInputStream;

import de.raindancer118.pocketcore.Ui;

/**
 * The computer's installed apps, searchable. Hold starts one; a tap on a running
 * app opens its windows: move to another screen, close, quit, or start another.
 */
public class AppsActivity extends AppCompatActivity {
    private static final String TAG = "PocketDeck";

    /** An installed app and its open windows (from apps.list). */
    private static final class Entry {
        final String id, name;
        final JSONArray running;

        Entry(JSONObject o) throws JSONException {
            id = o.getString("id");
            name = o.getString("name");
            JSONArray r = o.optJSONArray("running");
            running = r == null ? new JSONArray() : r;
        }

        boolean isRunning() {
            return running.length() > 0;
        }
    }

    private Deck deck;
    private LinearLayout list;
    private TextView message;
    private EditText search;
    private final List<Entry> apps = new ArrayList<>();
    private final List<Entry> shown = new ArrayList<>();
    private File iconDir;
    private Dialog panel;

    @Override
    protected void onCreate(Bundle s) {
        super.onCreate(s);
        setContentView(R.layout.activity_apps);
        deck = Deck.get(this);
        iconDir = new File(getFilesDir(), "app-icons");
        list = findViewById(R.id.list);
        message = findViewById(R.id.message);
        search = findViewById(R.id.search);
        findViewById(R.id.back).setOnClickListener(v -> finish());
        search.addTextChangedListener(new TextWatcher() {
            @Override
            public void beforeTextChanged(CharSequence c, int a, int b, int d) {
            }

            @Override
            public void onTextChanged(CharSequence c, int a, int b, int d) {
            }

            @Override
            public void afterTextChanged(Editable e) {
                render();
            }
        });
        // Enter starts the first hit, like a desktop launcher
        search.setOnEditorActionListener((v, id, e) -> {
            if (id == EditorInfo.IME_ACTION_GO && !shown.isEmpty()) {
                launch(shown.get(0));
                return true;
            }
            return false;
        });
        say(getString(R.string.loading_apps));
    }

    @Override
    protected void onResume() {
        super.onResume();
        deck.startPolling();
        load(true);
    }

    private void load(boolean withIcons) {
        deck.send("apps.list", "{}", (ok, err, data) -> {
            if (!ok) {
                say(err);
                return;
            }
            try {
                JSONArray a = new JSONArray(data);
                apps.clear();
                for (int i = 0; i < a.length(); i++) apps.add(new Entry(a.getJSONObject(i)));
                apps.sort((x, y) -> x.name.compareToIgnoreCase(y.name));
                say(null);
                render();
                if (withIcons) refreshIcons();
            } catch (JSONException e) {
                say(e.getMessage());
            }
        });
    }

    // ---- list ----

    private void render() {
        String q = search.getText().toString().trim().toLowerCase(Locale.ROOT);
        shown.clear();
        // without a query running apps come first; with one, names starting with it first
        if (q.isEmpty()) {
            for (Entry e : apps) if (e.isRunning()) shown.add(e);
            for (Entry e : apps) if (!e.isRunning()) shown.add(e);
        } else {
            for (Entry e : apps) if (e.name.toLowerCase(Locale.ROOT).startsWith(q)) shown.add(e);
            for (Entry e : apps) {
                String n = e.name.toLowerCase(Locale.ROOT);
                if (!n.startsWith(q) && (n.contains(q) || e.id.toLowerCase(Locale.ROOT).contains(q))) shown.add(e);
            }
        }
        list.removeAllViews();
        LayoutInflater inf = LayoutInflater.from(this);
        for (Entry app : shown) {
            View row = inf.inflate(R.layout.item_app, list, false);
            TextView name = row.findViewById(R.id.name), meta = row.findViewById(R.id.meta);
            name.setText(app.name);
            name.setCompoundDrawablesRelative(icon(app.id), null, null, null);
            if (app.isRunning()) {
                meta.setText(runningText(app));
                meta.setTextColor(getColor(R.color.text1));
                meta.setCompoundDrawablesRelativeWithIntrinsicBounds(R.drawable.dot_live, 0, 0, 0);
                meta.setCompoundDrawablePadding(dp(6));
            } else {
                meta.setText(app.id);
            }
            // hold to start, like shortcuts: a stray tap while scrolling starts nothing
            Hold.attach(row, row.findViewById(R.id.fill), () -> launch(app));
            row.setOnClickListener(v -> {
                if (app.isRunning()) showPanel(app);
                else say(getString(R.string.hold_to_start));
            });
            list.addView(row);
        }
        if (shown.isEmpty() && !apps.isEmpty()) say(getString(R.string.no_results));
        else if (!apps.isEmpty()) say(null);
    }

    private String runningText(Entry app) {
        int n = app.running.length();
        if (n == 1) return getString(R.string.app_running_one, where(app.running.optJSONObject(0)));
        return getResources().getQuantityString(R.plurals.app_running_many, n, n);
    }

    private String where(JSONObject w) {
        String ws = w.optString("workspace"), mon = w.optString("monitor");
        if (ws.startsWith("special:")) return getString(R.string.win_minimized, mon);
        return getString(R.string.win_where, ws, mon);
    }

    // ---- panel for a running app ----

    private void showPanel(Entry app) {
        if (panel != null) panel.dismiss();
        Dialog d = new Dialog(this);
        View v = LayoutInflater.from(this).inflate(R.layout.dialog_app, new FrameLayout(this), false);
        TextView title = v.findViewById(R.id.title);
        title.setText(app.name);
        title.setCompoundDrawablesRelative(icon(app.id), null, null, null);
        LinearLayout windows = v.findViewById(R.id.windows);
        for (int i = 0; i < app.running.length(); i++) {
            JSONObject w = app.running.optJSONObject(i);
            if (w == null) continue;
            View block = LayoutInflater.from(this).inflate(R.layout.item_window, windows, false);
            ((TextView) block.findViewById(R.id.win_title)).setText(w.optString("title"));
            ((TextView) block.findViewById(R.id.win_where)).setText(where(w));
            String address = w.optString("address");
            block.findViewById(R.id.win_move).setOnClickListener(x -> chooseScreen(address, w.optString("monitor")));
            block.findViewById(R.id.win_close).setOnClickListener(x ->
                    windowAction("window.close", address, null, getString(R.string.win_closed, app.name)));
            block.findViewById(R.id.win_kill).setOnClickListener(x ->
                    Ui.confirm(this, getString(R.string.kill_title, app.name), getString(R.string.kill_body),
                            getString(R.string.win_kill), true,
                            () -> windowAction("window.kill", address, null, getString(R.string.win_killed, app.name))));
            windows.addView(block);
        }
        v.findViewById(R.id.new_window).setOnClickListener(x -> {
            d.dismiss();
            launch(app);
        });
        v.findViewById(R.id.done).setOnClickListener(x -> d.dismiss());
        d.setContentView(v);
        if (d.getWindow() != null) {
            // full width with a 16 dp gap to the screen edges (a root margin is ignored in dialogs):
            // three window buttons need the room
            d.getWindow().setBackgroundDrawable(new android.graphics.drawable.InsetDrawable(new ColorDrawable(0), dp(16)));
            d.getWindow().setLayout(android.view.ViewGroup.LayoutParams.MATCH_PARENT, android.view.ViewGroup.LayoutParams.WRAP_CONTENT);
        }
        d.setOnDismissListener(x -> panel = null);
        panel = d;
        d.show();
    }

    /** Screens left to right from the agent; the window's current one is left out. */
    private void chooseScreen(String address, String current) {
        deck.send("monitors.list", "{}", (ok, err, data) -> {
            if (!ok) {
                say(err);
                return;
            }
            List<String> names = new ArrayList<>();
            try {
                JSONArray a = new JSONArray(data);
                for (int i = 0; i < a.length(); i++) {
                    String n = a.getJSONObject(i).getString("name");
                    if (!n.equals(current)) names.add(n);
                }
            } catch (JSONException e) {
                say(e.getMessage());
                return;
            }
            String[] items = names.toArray(new String[0]);
            new AlertDialog.Builder(this).setTitle(R.string.move_to).setItems(items, (dlg, which) ->
                    windowAction("window.move", address, items[which], getString(R.string.win_moved, items[which]))).show();
        });
    }

    /** Window actions need the remote session: one finger, then the same action again. */
    private void windowAction(String type, String address, String monitor, String doneText) {
        String body;
        try {
            JSONObject o = new JSONObject().put("address", address);
            if (monitor != null) o.put("monitor", monitor);
            body = o.toString();
        } catch (JSONException e) {
            return;
        }
        deck.send(type, body, (ok, err, data) -> {
            if (!ok && Session.isSessionError(err)) {
                Session.open(this, (opened, e) -> {
                    if (opened) windowAction(type, address, monitor, doneText);
                    else say(e);
                });
                return;
            }
            say(ok ? doneText : err);
            if (ok) {
                if (panel != null) panel.dismiss();
                // the window list changes a moment later (closing can take a while)
                list.postDelayed(() -> load(false), 700);
            }
        });
    }

    private void launch(Entry app) {
        String body;
        try {
            body = new JSONObject().put("id", app.id).toString();
        } catch (JSONException e) {
            return;
        }
        deck.send("app.launch", body, (ok, err, data) -> {
            say(ok ? getString(R.string.app_started, app.name) : err);
            if (ok) list.postDelayed(() -> load(false), 1500);
        });
    }

    // ---- logos ----

    /** Logos come as one zip; the phone keeps them and only fetches again when they changed. */
    private void refreshIcons() {
        String have = getSharedPreferences("deck_icons", MODE_PRIVATE).getString("app_icons_hash", "");
        String body;
        try {
            body = new JSONObject().put("have", have).toString();
        } catch (JSONException e) {
            return;
        }
        deck.send("apps.icons", body, (ok, err, data) -> {
            if (!ok) { // logos are a nice-to-have: the list works without them
                Log.w(TAG, "apps.icons: " + err);
                return;
            }
            try {
                JSONObject o = new JSONObject(data);
                String blob = o.optString("blob"), hash = o.optString("hash");
                if (blob.isEmpty()) return;
                deck.io().execute(() -> {
                    try {
                        int got = unzipIcons(deck.download(blob));
                        // only remember the hash when everything arrived, else fetch again next time
                        if (got != o.optInt("count", got)) throw new IOException("icons: " + got + " of " + o.optInt("count"));
                        getSharedPreferences("deck_icons", MODE_PRIVATE).edit().putString("app_icons_hash", hash).apply();
                        runOnUiThread(this::render);
                    } catch (Exception e) {
                        Log.w(TAG, "app icons", e);
                    }
                });
            } catch (JSONException ignored) {
            }
        });
    }

    private int unzipIcons(byte[] zip) throws IOException {
        int count = 0;
        File tmp = new File(getFilesDir(), "app-icons.new");
        deleteDir(tmp);
        if (!tmp.mkdirs()) throw new IOException("no icon dir");
        try (ZipInputStream z = new ZipInputStream(new ByteArrayInputStream(zip))) {
            byte[] buf = new byte[16384];
            for (ZipEntry e; (e = z.getNextEntry()) != null; ) {
                String n = new File(e.getName()).getName(); // no paths from the zip
                if (e.isDirectory() || !n.endsWith(".png")) continue;
                try (FileOutputStream out = new FileOutputStream(new File(tmp, n))) {
                    for (int r; (r = z.read(buf)) > 0; ) out.write(buf, 0, r);
                }
                count++;
            }
        }
        deleteDir(iconDir);
        if (!tmp.renameTo(iconDir)) throw new IOException("icons not stored");
        return count;
    }

    private static void deleteDir(File d) {
        File[] fs = d.listFiles();
        if (fs != null) for (File f : fs) //noinspection ResultOfMethodCallIgnored
            f.delete();
        //noinspection ResultOfMethodCallIgnored
        d.delete();
    }

    private Drawable icon(String id) {
        int px = dp(28);
        Bitmap b = BitmapFactory.decodeFile(new File(iconDir, id + ".png").getPath());
        Drawable d = b == null ? AppCompatResources.getDrawable(this, R.drawable.ic_apps) : new BitmapDrawable(getResources(), b);
        if (d != null) d.setBounds(0, 0, px, px);
        return d;
    }

    private int dp(int v) {
        return Math.round(v * getResources().getDisplayMetrics().density);
    }

    private void say(String s) {
        message.setVisibility(s == null ? View.GONE : View.VISIBLE);
        message.setText(s);
    }
}

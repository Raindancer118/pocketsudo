package de.raindancer118.pocketdeck;

import android.os.Bundle;
import android.text.Editable;
import android.text.TextWatcher;
import android.view.HapticFeedbackConstants;
import android.view.LayoutInflater;
import android.view.View;
import android.view.inputmethod.EditorInfo;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.TextView;

import androidx.appcompat.app.AppCompatActivity;

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

import android.graphics.Bitmap;
import android.graphics.BitmapFactory;
import android.graphics.drawable.BitmapDrawable;
import android.graphics.drawable.Drawable;

/** The computer's installed apps, searchable; a tap starts one there. */
public class AppsActivity extends AppCompatActivity {
    private Deck deck;
    private LinearLayout list;
    private TextView message;
    private EditText search;
    private final List<String[]> apps = new ArrayList<>(); // [id, name]
    private final List<String[]> shown = new ArrayList<>();
    private File iconDir;

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
                launch(shown.get(0), null);
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
        deck.send("apps.list", "{}", (ok, err, data) -> {
            if (!ok) {
                say(err);
                return;
            }
            try {
                JSONArray a = new JSONArray(data);
                apps.clear();
                for (int i = 0; i < a.length(); i++) {
                    JSONObject o = a.getJSONObject(i);
                    apps.add(new String[]{o.getString("id"), o.getString("name")});
                }
                apps.sort((x, y) -> x[1].compareToIgnoreCase(y[1]));
                say(null);
                render();
                refreshIcons();
            } catch (JSONException e) {
                say(e.getMessage());
            }
        });
    }

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
            if (!ok) return; // logos are a nice-to-have: the list works without them
            try {
                JSONObject o = new JSONObject(data);
                String blob = o.optString("blob"), hash = o.optString("hash");
                if (blob.isEmpty()) return;
                deck.io().execute(() -> {
                    try {
                        unzipIcons(deck.download(blob));
                        getSharedPreferences("deck_icons", MODE_PRIVATE).edit().putString("app_icons_hash", hash).apply();
                        runOnUiThread(this::render);
                    } catch (Exception ignored) {
                    }
                });
            } catch (JSONException ignored) {
            }
        });
    }

    private void unzipIcons(byte[] zip) throws IOException {
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
            }
        }
        deleteDir(iconDir);
        if (!tmp.renameTo(iconDir)) throw new IOException("icons not stored");
    }

    private static void deleteDir(File d) {
        File[] fs = d.listFiles();
        if (fs != null) for (File f : fs) //noinspection ResultOfMethodCallIgnored
            f.delete();
        //noinspection ResultOfMethodCallIgnored
        d.delete();
    }

    private Drawable icon(String id) {
        int px = Math.round(28 * getResources().getDisplayMetrics().density);
        Bitmap b = BitmapFactory.decodeFile(new File(iconDir, id + ".png").getPath());
        Drawable d = b == null ? androidx.appcompat.content.res.AppCompatResources.getDrawable(this, R.drawable.ic_apps) : new BitmapDrawable(getResources(), b);
        if (d != null) d.setBounds(0, 0, px, px);
        return d;
    }

    private void render() {
        String q = search.getText().toString().trim().toLowerCase(Locale.ROOT);
        shown.clear();
        // names starting with the query first, then the rest that contain it
        for (String[] app : apps) if (app[1].toLowerCase(Locale.ROOT).startsWith(q)) shown.add(app);
        for (String[] app : apps) {
            String n = app[1].toLowerCase(Locale.ROOT);
            if (!n.startsWith(q) && (n.contains(q) || app[0].toLowerCase(Locale.ROOT).contains(q))) shown.add(app);
        }
        list.removeAllViews();
        LayoutInflater inf = LayoutInflater.from(this);
        for (String[] app : shown) {
            View row = inf.inflate(R.layout.item_file, list, false);
            TextView name = row.findViewById(R.id.name), meta = row.findViewById(R.id.meta);
            name.setText(app[1]);
            name.setCompoundDrawablesRelative(icon(app[0]), null, null, null);
            // meta lines up with the name text after the 28 dp logo and its 12 dp gap
            ((LinearLayout.LayoutParams) meta.getLayoutParams()).setMarginStart(Math.round(40 * getResources().getDisplayMetrics().density));
            meta.setText(app[0]);
            row.setOnClickListener(v -> launch(app, row));
            list.addView(row);
        }
        if (shown.isEmpty() && !apps.isEmpty()) say(getString(R.string.no_results));
        else if (!apps.isEmpty()) say(null);
    }

    private void launch(String[] app, View row) {
        if (row != null) row.performHapticFeedback(HapticFeedbackConstants.CONFIRM);
        String body;
        try {
            body = new JSONObject().put("id", app[0]).toString();
        } catch (JSONException e) {
            return;
        }
        deck.send("app.launch", body, (ok, err, data) -> say(ok ? getString(R.string.app_started, app[1]) : err));
    }

    private void say(String s) {
        message.setVisibility(s == null ? View.GONE : View.VISIBLE);
        message.setText(s);
    }
}

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

import java.util.ArrayList;
import java.util.List;
import java.util.Locale;

/** The computer's installed apps, searchable; a tap starts one there. */
public class AppsActivity extends AppCompatActivity {
    private Deck deck;
    private LinearLayout list;
    private TextView message;
    private EditText search;
    private final List<String[]> apps = new ArrayList<>(); // [id, name]
    private final List<String[]> shown = new ArrayList<>();

    @Override
    protected void onCreate(Bundle s) {
        super.onCreate(s);
        setContentView(R.layout.activity_apps);
        deck = Deck.get(this);
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
            } catch (JSONException e) {
                say(e.getMessage());
            }
        });
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
            meta.setText(app[0]);
            meta.setPaddingRelative(0, meta.getPaddingTop(), 0, meta.getPaddingBottom()); // no icon in front of the name here
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

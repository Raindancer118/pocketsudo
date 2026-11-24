package de.raindancer118.pocketdeck;

import android.content.res.Configuration;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.view.Gravity;
import android.view.HapticFeedbackConstants;
import android.view.View;
import android.view.inputmethod.EditorInfo;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.TextView;

import androidx.appcompat.app.AppCompatActivity;

import org.json.JSONException;
import org.json.JSONObject;

import java.util.ArrayList;
import java.util.HashMap;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;

/**
 * A German QWERTZ keyboard. Modifiers latch for the next key; "Kombi" collects
 * any number of keys and sends them together when tapped again.
 */
public class KeyboardActivity extends AppCompatActivity {
    /** label, key name for the agent, width weight */
    private static final Object[][][] ROWS = {
            {{"Esc", "esc", 1.2}, {"F1", "f1", 1.0}, {"F2", "f2", 1.0}, {"F3", "f3", 1.0}, {"F4", "f4", 1.0},
                    {"F5", "f5", 1.0}, {"F6", "f6", 1.0}, {"F7", "f7", 1.0}, {"F8", "f8", 1.0}, {"F9", "f9", 1.0},
                    {"F10", "f10", 1.0}, {"F11", "f11", 1.0}, {"F12", "f12", 1.0}, {"Entf", "delete", 1.2}},
            {{"^", "asciicircum", 1.0}, {"1", "1", 1.0}, {"2", "2", 1.0}, {"3", "3", 1.0}, {"4", "4", 1.0},
                    {"5", "5", 1.0}, {"6", "6", 1.0}, {"7", "7", 1.0}, {"8", "8", 1.0}, {"9", "9", 1.0},
                    {"0", "0", 1.0}, {"ß", "ssharp", 1.0}, {"´", "dead_acute", 1.0}, {"⌫", "backspace", 1.8}},
            {{"Tab", "tab", 1.5}, {"q", "q", 1.0}, {"w", "w", 1.0}, {"e", "e", 1.0}, {"r", "r", 1.0}, {"t", "t", 1.0},
                    {"z", "z", 1.0}, {"u", "u", 1.0}, {"i", "i", 1.0}, {"o", "o", 1.0}, {"p", "p", 1.0},
                    {"ü", "udiaeresis", 1.0}, {"+", "plus", 1.0}, {"↵", "enter", 1.3}},
            {{"Caps", "capslock", 1.8}, {"a", "a", 1.0}, {"s", "s", 1.0}, {"d", "d", 1.0}, {"f", "f", 1.0},
                    {"g", "g", 1.0}, {"h", "h", 1.0}, {"j", "j", 1.0}, {"k", "k", 1.0}, {"l", "l", 1.0},
                    {"ö", "odiaeresis", 1.0}, {"ä", "adiaeresis", 1.0}, {"#", "numbersign", 1.0}},
            {{"⇧", "shift", 1.3}, {"<", "less", 1.0}, {"y", "y", 1.0}, {"x", "x", 1.0}, {"c", "c", 1.0},
                    {"v", "v", 1.0}, {"b", "b", 1.0}, {"n", "n", 1.0}, {"m", "m", 1.0}, {",", "comma", 1.0},
                    {".", "period", 1.0}, {"-", "minus", 1.0}, {"↑", "up", 1.0}, {"⇧", "shift", 1.3}},
            {{"Strg", "ctrl", 1.3}, {"❖", "super", 1.2}, {"Alt", "alt", 1.2}, {"", "space", 5.0},
                    {"AltGr", "altgr", 1.3}, {"←", "left", 1.0}, {"↓", "down", 1.0}, {"→", "right", 1.0}},
    };
    private static final Map<String, String> MOD_LABEL = Map.of(
            "ctrl", "Strg", "alt", "Alt", "shift", "Shift", "super", "Super", "altgr", "AltGr");

    private Deck deck;
    private final Handler main = new Handler(Looper.getMainLooper());
    private final Set<String> latched = new LinkedHashSet<>();
    private final List<String> combo = new ArrayList<>();
    private final Map<String, List<TextView>> views = new HashMap<>();
    private boolean comboMode;
    private View closed, opened;
    private TextView session, message, comboButton, comboPreview;
    private EditText text;
    private final Runnable ticker = new Runnable() {
        @Override
        public void run() {
            render();
            main.postDelayed(this, 20_000);
        }
    };

    @Override
    protected void onCreate(Bundle s) {
        super.onCreate(s);
        setContentView(R.layout.activity_keyboard);
        deck = Deck.get(this);
        closed = findViewById(R.id.closed);
        opened = findViewById(R.id.opened);
        session = findViewById(R.id.session);
        message = findViewById(R.id.message);
        text = findViewById(R.id.text);
        comboButton = findViewById(R.id.combo);
        comboPreview = findViewById(R.id.combo_preview);
        findViewById(R.id.back).setOnClickListener(v -> finish());
        findViewById(R.id.open).setOnClickListener(v -> Session.open(this, (ok, err) -> {
            if (!ok) say(err);
            render();
        }));
        findViewById(R.id.send).setOnClickListener(v -> typeText());
        text.setOnEditorActionListener((v, id, e) -> {
            if (id == EditorInfo.IME_ACTION_SEND) {
                typeText();
                return true;
            }
            return false;
        });
        comboButton.setOnClickListener(v -> toggleCombo());
        buildKeyboard(findViewById(R.id.keys));
    }

    @Override
    protected void onResume() {
        super.onResume();
        deck.startPolling();
        main.post(ticker);
    }

    @Override
    protected void onPause() {
        main.removeCallbacks(ticker);
        super.onPause();
    }

    private void render() {
        long left = deck.sessionUntil - System.currentTimeMillis();
        boolean open = left > 0;
        closed.setVisibility(open ? View.GONE : View.VISIBLE);
        opened.setVisibility(open ? View.VISIBLE : View.GONE);
        session.setText(open ? getString(R.string.session_left, (int) Math.ceil(left / 60000.0)) : "");
        session.setCompoundDrawablesRelativeWithIntrinsicBounds(open ? R.drawable.dot_live : 0, 0, 0, 0);
    }

    // ---- keyboard ----

    private void buildKeyboard(LinearLayout root) {
        boolean landscape = getResources().getConfiguration().orientation == Configuration.ORIENTATION_LANDSCAPE;
        int h = dp(landscape ? 40 : 46), gap = dp(3);
        for (int r = 0; r < ROWS.length; r++) {
            LinearLayout row = new LinearLayout(this);
            row.setOrientation(LinearLayout.HORIZONTAL);
            LinearLayout.LayoutParams rlp = new LinearLayout.LayoutParams(-1, r == 0 ? dp(landscape ? 30 : 36) : h);
            rlp.bottomMargin = gap;
            for (Object[] k : ROWS[r]) {
                String label = (String) k[0], name = (String) k[1];
                TextView t = new TextView(this, null, 0, R.style.Text);
                t.setText(label);
                t.setGravity(Gravity.CENTER);
                t.setTextSize(r == 0 || label.length() > 2 ? 11 : 15);
                t.setMaxLines(1);
                t.setBackgroundResource(R.drawable.bg_key);
                t.setContentDescription(name);
                LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(0, -1, ((Double) k[2]).floatValue());
                lp.setMarginEnd(gap);
                t.setOnClickListener(v -> tap(name));
                views.computeIfAbsent(name, x -> new ArrayList<>()).add(t);
                row.addView(t, lp);
            }
            root.addView(row, rlp);
        }
    }

    private static boolean isModifier(String name) {
        return MOD_LABEL.containsKey(name);
    }

    private void tap(String name) {
        comboButton.performHapticFeedback(HapticFeedbackConstants.KEYBOARD_TAP);
        if (comboMode) {
            if (!combo.remove(name)) combo.add(name);
            refresh();
            return;
        }
        if (isModifier(name)) {
            if (!latched.remove(name)) latched.add(name);
            refresh();
            return;
        }
        StringBuilder c = new StringBuilder();
        for (String m : latched) c.append(m).append('+');
        send(c.append(name).toString());
        latched.clear();
        refresh();
    }

    private void toggleCombo() {
        if (comboMode && !combo.isEmpty()) sendCombo();
        comboMode = !comboMode;
        combo.clear();
        latched.clear();
        refresh();
    }

    /** Modifiers first, then the last normal key (or a lone modifier like Super). */
    private void sendCombo() {
        List<String> mods = new ArrayList<>();
        String key = null;
        for (String k : combo) {
            if (isModifier(k)) mods.add(k);
            else key = k;
        }
        if (key == null && !mods.isEmpty()) key = mods.remove(mods.size() - 1);
        if (key == null) return;
        StringBuilder c = new StringBuilder();
        for (String m : mods) c.append(m).append('+');
        send(c.append(key).toString());
    }

    private void refresh() {
        Set<String> on = new LinkedHashSet<>(comboMode ? combo : latched);
        for (Map.Entry<String, List<TextView>> e : views.entrySet()) {
            boolean lit = on.contains(e.getKey());
            for (TextView t : e.getValue()) {
                t.setBackgroundResource(lit ? R.drawable.bg_key_on : R.drawable.bg_key);
                t.setTextColor(getColor(lit ? R.color.on_ink : R.color.text1));
            }
        }
        comboButton.setBackgroundResource(comboMode ? R.drawable.bg_key_on : R.drawable.bg_key);
        comboButton.setTextColor(getColor(comboMode ? R.color.on_ink : R.color.text1));
        if (comboMode) {
            StringBuilder p = new StringBuilder();
            for (String k : combo) {
                if (p.length() > 0) p.append(" + ");
                p.append(MOD_LABEL.getOrDefault(k, label(k)));
            }
            comboPreview.setText(p.length() == 0 ? getString(R.string.combo_hint) : p);
        } else {
            comboPreview.setText("");
        }
    }

    private String label(String name) {
        List<TextView> v = views.get(name);
        return v == null || v.isEmpty() ? name : v.get(0).getText().toString();
    }

    private void send(String c) {
        deck.send("key", json("combo", c), (ok, err, data) -> {
            if (!ok) {
                say(err);
                if (Session.isSessionError(err)) deck.sessionUntil = 0;
                render();
            }
        });
    }

    private void typeText() {
        String t = text.getText().toString();
        if (t.isEmpty()) return;
        deck.send("type", json("text", t), (ok, err, data) -> {
            if (ok) {
                text.setText("");
                say(null);
            } else {
                say(err);
            }
        });
    }

    private String json(String k, String v) {
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

    private int dp(int v) {
        return Math.round(v * getResources().getDisplayMetrics().density);
    }
}

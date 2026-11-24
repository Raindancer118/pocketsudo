package de.raindancer118.pocketdeck;

import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.view.Gravity;
import android.view.HapticFeedbackConstants;
import android.view.View;
import android.view.inputmethod.EditorInfo;
import android.widget.EditText;
import android.widget.HorizontalScrollView;
import android.widget.LinearLayout;
import android.widget.TextView;

import androidx.appcompat.app.AppCompatActivity;

import org.json.JSONException;
import org.json.JSONObject;

import java.util.LinkedHashSet;
import java.util.Set;


/** Virtual keyboard: text, sticky modifiers, special keys and common shortcuts. */
public class KeyboardActivity extends AppCompatActivity {
    private static final String[][] SPECIAL = {
            {"Esc", "esc"}, {"Tab", "tab"}, {"⌫", "backspace"}, {"Entf", "delete"}, {"↵", "enter"},
            {"←", "left"}, {"↑", "up"}, {"↓", "down"}, {"→", "right"}, {"␣", "space"},
            {"Pos1", "home"}, {"Ende", "end"}, {"Bild↑", "pageup"}, {"Bild↓", "pagedown"}, {"Druck", "print"},
    };
    private static final String[][] COMBOS = {
            {"Super", "super"}, {"Strg+Alt+T", "ctrl+alt+t"}, {"Alt+Tab", "alt+tab"}, {"Strg+C", "ctrl+c"},
            {"Strg+V", "ctrl+v"}, {"Strg+Z", "ctrl+z"}, {"Strg+S", "ctrl+s"}, {"Strg+W", "ctrl+w"},
            {"Super+Q", "super+q"}, {"Super+Enter", "super+enter"},
    };
    private static final String[][] MODS = {{"Strg", "ctrl"}, {"Alt", "alt"}, {"Shift", "shift"}, {"Super", "super"}};

    private Deck deck;
    private final Set<String> held = new LinkedHashSet<>();
    private final Handler main = new Handler(Looper.getMainLooper());
    private View closed, opened;
    private TextView session, message;
    private EditText text;
    private LinearLayout keys;
    private final java.util.Map<String, TextView> modViews = new java.util.HashMap<>();
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
        keys = findViewById(R.id.keys);
        findViewById(R.id.back).setOnClickListener(v -> finish());
        findViewById(R.id.open).setOnClickListener(v -> openSession());
        findViewById(R.id.send).setOnClickListener(v -> typeText());
        text.setOnEditorActionListener((v, id, e) -> {
            if (id == EditorInfo.IME_ACTION_SEND) {
                typeText();
                return true;
            }
            return false;
        });

        section(R.string.modifiers);
        LinearLayout mods = row();
        for (String[] m : MODS) {
            TextView t = key(m[0], 1f);
            t.setOnClickListener(v -> toggle(m[1]));
            modViews.put(m[1], t);
            mods.addView(t);
        }
        section(R.string.keys);
        for (int i = 0; i < SPECIAL.length; i += 5) {
            LinearLayout r = row();
            for (int j = i; j < i + 5; j++) {
                String[] k = SPECIAL[j];
                TextView t = key(k[0], 1f);
                t.setOnClickListener(v -> press(k[1]));
                r.addView(t);
            }
        }
        LinearLayout fkeys = scrollRow();
        for (int f = 1; f <= 12; f++) {
            String name = "f" + f;
            TextView t = key("F" + f, 0f);
            t.setOnClickListener(v -> press(name));
            fkeys.addView(t);
        }
        section(R.string.combos);
        LinearLayout combos = scrollRow();
        for (String[] c : COMBOS) {
            TextView t = key(c[0], 0f);
            t.setOnClickListener(v -> send(c[1]));
            combos.addView(t);
        }
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

    private void openSession() {
        Session.open(this, (ok, err) -> {
            if (!ok) say(err);
            render();
        });
    }

    private void toggle(String mod) {
        if (!held.remove(mod)) held.add(mod);
        refreshMods();
    }

    private void refreshMods() {
        for (java.util.Map.Entry<String, TextView> e : modViews.entrySet()) {
            boolean on = held.contains(e.getKey());
            e.getValue().setBackgroundResource(on ? R.drawable.bg_key_on : R.drawable.bg_secondary);
            e.getValue().setTextColor(getColor(on ? R.color.on_ink : R.color.text1));
        }
    }

    /** A key with whatever modifiers are held; modifiers release after one use. */
    private void press(String keyName) {
        StringBuilder combo = new StringBuilder();
        for (String m : held) combo.append(m).append('+');
        combo.append(keyName);
        send(combo.toString());
        if (!held.isEmpty()) {
            held.clear();
            refreshMods();
        }
    }

    private void send(String combo) {
        keys.performHapticFeedback(HapticFeedbackConstants.KEYBOARD_TAP);
        deck.send("key", json("combo", combo), (ok, err, data) -> {
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
        if (!held.isEmpty() && t.length() == 1) {
            // a single character with modifiers held is a shortcut, e.g. Strg + c
            press(t.toLowerCase(java.util.Locale.ROOT));
            text.setText("");
            return;
        }
        deck.send("type", json("text", t), (ok, err, data) -> {
            if (ok) {
                text.setText("");
                say(getString(R.string.sent));
            } else {
                say(err);
            }
        });
    }

    // ---- view helpers ----

    private void section(int label) {
        TextView t = new TextView(this, null, 0, R.style.Text_Small);
        t.setText(label);
        LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(-1, -2);
        lp.topMargin = dp(24);
        lp.bottomMargin = dp(8);
        keys.addView(t, lp);
    }

    private LinearLayout row() {
        LinearLayout r = new LinearLayout(this);
        r.setOrientation(LinearLayout.HORIZONTAL);
        LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(-1, -2);
        lp.bottomMargin = dp(8);
        keys.addView(r, lp);
        return r;
    }

    private LinearLayout scrollRow() {
        HorizontalScrollView sv = new HorizontalScrollView(this);
        sv.setHorizontalScrollBarEnabled(false);
        LinearLayout r = new LinearLayout(this);
        r.setOrientation(LinearLayout.HORIZONTAL);
        sv.addView(r);
        LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(-1, -2);
        lp.bottomMargin = dp(8);
        keys.addView(sv, lp);
        return r;
    }

    private TextView key(String label, float weight) {
        TextView t = new TextView(this, null, 0, R.style.Text);
        t.setText(label);
        t.setGravity(Gravity.CENTER);
        t.setTextSize(14);
        t.setBackgroundResource(R.drawable.bg_secondary);
        t.setMinWidth(weight == 0 ? dp(64) : 0);
        t.setPadding(dp(12), 0, dp(12), 0);
        t.setStateListAnimator(android.animation.AnimatorInflater.loadStateListAnimator(this, R.animator.press));
        LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(weight == 0 ? -2 : 0, dp(44), weight);
        lp.setMarginEnd(dp(6));
        t.setLayoutParams(lp);
        return t;
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

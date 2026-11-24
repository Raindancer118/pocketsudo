package de.raindancer118.pocketdeck;

import android.app.AlertDialog;
import android.content.Intent;
import android.net.Uri;
import android.nfc.NdefMessage;
import android.nfc.NdefRecord;
import android.nfc.NfcAdapter;
import android.nfc.Tag;
import android.nfc.tech.Ndef;
import android.nfc.tech.NdefFormatable;
import android.os.Bundle;
import android.text.InputType;
import android.view.LayoutInflater;
import android.view.View;
import android.widget.EditText;
import android.widget.ImageButton;
import android.widget.LinearLayout;
import android.widget.TextView;

import androidx.appcompat.app.AppCompatActivity;
import androidx.core.content.pm.ShortcutInfoCompat;
import androidx.core.content.pm.ShortcutManagerCompat;
import androidx.core.graphics.drawable.IconCompat;

import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

import de.raindancer118.pocketcore.Ui;

/** Builds a shortcut from steps; pins it to the home screen or an NFC sticker. */
public class ShortcutEditActivity extends AppCompatActivity {
    private static final String[] ICONS = {"zap", "code", "moon", "screen", "lock"};

    private Shortcuts store;
    private Shortcuts.Shortcut sc;
    private EditText name;
    private LinearLayout steps, icons;
    private TextView message;

    @Override
    protected void onCreate(Bundle s) {
        super.onCreate(s);
        setContentView(R.layout.activity_shortcut_edit);
        store = new Shortcuts(this);
        String id = getIntent().getStringExtra("id");
        sc = id == null ? null : store.get(id);
        boolean isNew = sc == null;
        if (isNew) sc = new Shortcuts.Shortcut();
        name = findViewById(R.id.name);
        steps = findViewById(R.id.steps);
        icons = findViewById(R.id.icons);
        message = findViewById(R.id.message);
        name.setText(sc.name);
        Ui.link(findViewById(R.id.add_step), findViewById(R.id.pin), findViewById(R.id.nfc), findViewById(R.id.delete));
        findViewById(R.id.back).setOnClickListener(v -> saveAndFinish());
        findViewById(R.id.save).setOnClickListener(v -> saveAndFinish());
        findViewById(R.id.add_step).setOnClickListener(v -> chooseStep());
        findViewById(R.id.run).setOnClickListener(v -> {
            collect();
            say(getString(R.string.shortcut_running, sc.name));
            ShortcutRunner.run(this, sc, (ok, err, d) -> say(ok ? getString(R.string.shortcut_done, sc.name) : err));
        });
        findViewById(R.id.pin).setOnClickListener(v -> pin());
        findViewById(R.id.nfc).setOnClickListener(v -> writeNfc());
        findViewById(R.id.delete).setVisibility(isNew ? View.GONE : View.VISIBLE);
        findViewById(R.id.delete).setOnClickListener(v -> Ui.confirm(this, getString(R.string.delete_title, sc.name),
                getString(R.string.delete_body), getString(R.string.delete), true, () -> {
                    store.delete(sc.id);
                    finish();
                }));
        renderIcons();
        renderSteps();
    }

    @Override
    protected void onPause() {
        NfcAdapter nfc = NfcAdapter.getDefaultAdapter(this);
        if (nfc != null) nfc.disableReaderMode(this);
        super.onPause();
    }

    private void collect() {
        sc.name = name.getText().toString().trim();
    }

    private void saveAndFinish() {
        collect();
        if (!sc.name.isEmpty() || !sc.steps.isEmpty()) {
            if (sc.name.isEmpty()) sc.name = getString(R.string.unnamed);
            store.save(sc);
        }
        finish();
    }

    // ---- icons & steps ----

    private void renderIcons() {
        icons.removeAllViews();
        for (String ic : ICONS) {
            ImageButton b = new ImageButton(this);
            b.setImageResource(MainActivity.iconRes(ic));
            b.setBackgroundResource(ic.equals(sc.icon) ? R.drawable.bg_secondary : R.drawable.bg_link);
            b.setContentDescription(ic);
            b.setOnClickListener(v -> {
                sc.icon = ic;
                renderIcons();
            });
            LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(dp(44), dp(44));
            lp.setMarginEnd(dp(6));
            icons.addView(b, lp);
        }
    }

    private void renderSteps() {
        steps.removeAllViews();
        LayoutInflater inf = LayoutInflater.from(this);
        for (int i = 0; i < sc.steps.size(); i++) {
            Shortcuts.Step st = sc.steps.get(i);
            Shortcuts.StepType t = Shortcuts.find(st.type);
            View row = inf.inflate(R.layout.item_step, steps, false);
            ((TextView) row.findViewById(R.id.num)).setText(String.valueOf(i + 1));
            ((TextView) row.findViewById(R.id.label)).setText(t == null ? st.type : getString(t.label));
            ((TextView) row.findViewById(R.id.value)).setText(st.display);
            int idx = i;
            row.findViewById(R.id.up).setVisibility(i == 0 ? View.INVISIBLE : View.VISIBLE);
            row.findViewById(R.id.down).setVisibility(i == sc.steps.size() - 1 ? View.INVISIBLE : View.VISIBLE);
            row.findViewById(R.id.up).setOnClickListener(v -> move(idx, -1));
            row.findViewById(R.id.down).setOnClickListener(v -> move(idx, 1));
            row.findViewById(R.id.remove).setOnClickListener(v -> {
                sc.steps.remove(idx);
                renderSteps();
            });
            steps.addView(row);
        }
        findViewById(R.id.finger_hint).setVisibility(sc.needsFinger() ? View.VISIBLE : View.GONE);
    }

    private void move(int i, int d) {
        int j = i + d;
        if (j < 0 || j >= sc.steps.size()) return;
        Shortcuts.Step tmp = sc.steps.get(i);
        sc.steps.set(i, sc.steps.get(j));
        sc.steps.set(j, tmp);
        renderSteps();
    }

    private void chooseStep() {
        String[] labels = new String[Shortcuts.CATALOG.length];
        for (int i = 0; i < labels.length; i++) labels[i] = getString(Shortcuts.CATALOG[i].label);
        new AlertDialog.Builder(this).setTitle(R.string.choose_step)
                .setItems(labels, (d, which) -> askValue(Shortcuts.CATALOG[which])).show();
    }

    private void askValue(Shortcuts.StepType t) {
        switch (t.param) {
            case NONE:
                add(t, null, "");
                break;
            case ONOFF:
                String[] onoff = {getString(R.string.on), getString(R.string.off)};
                new AlertDialog.Builder(this).setTitle(t.label).setItems(onoff, (d, w) -> add(t, w == 0, onoff[w])).show();
                break;
            case MEDIA:
                String[] actions = {"play_pause", "play", "pause", "next", "previous"};
                String[] names = {getString(R.string.media_play_pause), getString(R.string.media_play),
                        getString(R.string.media_pause), getString(R.string.media_next), getString(R.string.media_previous)};
                new AlertDialog.Builder(this).setTitle(t.label).setItems(names, (d, w) -> add(t, actions[w], names[w])).show();
                break;
            case APP:
                pickApp(t);
                break;
            default:
                EditText in = new EditText(this);
                in.setInputType(t.param == Shortcuts.Param.NUMBER ? InputType.TYPE_CLASS_NUMBER : InputType.TYPE_CLASS_TEXT);
                in.setHint(hintFor(t));
                int pad = dp(20);
                in.setPadding(pad, pad / 2, pad, pad / 2);
                new AlertDialog.Builder(this).setTitle(t.label).setView(in)
                        .setPositiveButton(R.string.save, (d, w) -> {
                            String v = in.getText().toString().trim();
                            if (v.isEmpty()) return;
                            if (t.param == Shortcuts.Param.NUMBER) {
                                try {
                                    int n = Integer.parseInt(v);
                                    add(t, n, t.argKey.equals("percent") ? n + " %" : v);
                                } catch (NumberFormatException ignored) {
                                }
                            } else {
                                add(t, v, v);
                            }
                        })
                        .setNegativeButton(R.string.cancel, null).show();
        }
    }

    private String hintFor(Shortcuts.StepType t) {
        switch (t.type) {
            case "workspace":
                return getString(R.string.value_hint_workspace);
            case "volume.set":
            case "brightness.set":
                return getString(R.string.value_hint_percent);
            case "open.url":
                return getString(R.string.value_hint_url);
            case "key":
                return getString(R.string.value_hint_key);
            case "command":
                return getString(R.string.value_hint_cmd);
            default:
                return getString(R.string.value_hint_text);
        }
    }

    /** Lists the computer's installed apps (desktop entries). */
    private void pickApp(Shortcuts.StepType t) {
        say(getString(R.string.loading_apps));
        Deck deck = Deck.get(this);
        deck.startPolling();
        deck.send("apps.list", "{}", (ok, err, data) -> {
            if (!ok) {
                say(err);
                return;
            }
            say(null);
            try {
                JSONArray a = new JSONArray(data);
                String[] ids = new String[a.length()], names = new String[a.length()];
                for (int i = 0; i < a.length(); i++) {
                    JSONObject o = a.getJSONObject(i);
                    ids[i] = o.getString("id");
                    names[i] = o.getString("name");
                }
                new AlertDialog.Builder(this).setTitle(t.label).setItems(names, (d, w) -> add(t, ids[w], names[w])).show();
            } catch (JSONException e) {
                say(e.getMessage());
            }
        });
    }

    private void add(Shortcuts.StepType t, Object value, String display) {
        sc.steps.add(Shortcuts.step(t.type, t.argKey, value, display));
        renderSteps();
    }

    // ---- triggers ----

    private Uri runUri() {
        return Uri.parse("pocketdeck://run?id=" + sc.id);
    }

    private void pin() {
        collect();
        store.save(sc);
        ShortcutInfoCompat info = new ShortcutInfoCompat.Builder(this, sc.id)
                .setShortLabel(sc.name.isEmpty() ? getString(R.string.unnamed) : sc.name)
                .setIcon(IconCompat.createWithResource(this, R.mipmap.ic_launcher))
                .setIntent(new Intent(Intent.ACTION_VIEW, runUri()).setClass(this, RunShortcutActivity.class))
                .build();
        ShortcutManagerCompat.requestPinShortcut(this, info, null);
    }

    private void writeNfc() {
        collect();
        store.save(sc);
        NfcAdapter nfc = NfcAdapter.getDefaultAdapter(this);
        if (nfc == null || !nfc.isEnabled()) {
            say(getString(R.string.nfc_off));
            return;
        }
        say(getString(R.string.nfc_hold));
        NdefMessage msg = new NdefMessage(NdefRecord.createUri(runUri()));
        nfc.enableReaderMode(this, tag -> {
            String result = write(tag, msg);
            runOnUiThread(() -> {
                say(result);
                nfc.disableReaderMode(this);
            });
        }, NfcAdapter.FLAG_READER_NFC_A | NfcAdapter.FLAG_READER_NFC_B | NfcAdapter.FLAG_READER_NFC_F
                | NfcAdapter.FLAG_READER_NFC_V | NfcAdapter.FLAG_READER_SKIP_NDEF_CHECK, null);
    }

    private String write(Tag tag, NdefMessage msg) {
        try {
            Ndef ndef = Ndef.get(tag);
            if (ndef != null) {
                ndef.connect();
                ndef.writeNdefMessage(msg);
                ndef.close();
                return getString(R.string.nfc_done);
            }
            NdefFormatable f = NdefFormatable.get(tag);
            if (f != null) {
                f.connect();
                f.format(msg);
                f.close();
                return getString(R.string.nfc_done);
            }
            return getString(R.string.nfc_off);
        } catch (Exception e) {
            return String.valueOf(e.getMessage());
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

package de.raindancer118.pocketdeck;

import android.app.AlertDialog;
import android.content.Intent;
import android.graphics.drawable.Drawable;
import android.text.SpannableString;
import android.text.Spanned;
import android.text.style.AbsoluteSizeSpan;
import android.text.style.ForegroundColorSpan;
import android.view.HapticFeedbackConstants;
import android.widget.TextView;

import androidx.appcompat.app.AppCompatActivity;
import androidx.appcompat.content.res.AppCompatResources;

import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

import de.raindancer118.pocketcore.Ui;

/**
 * The quick-settings tiles on the start screen: tap switches, hold opens the
 * details (WLAN networks, Bluetooth devices). Filled tile = on.
 */
final class QuickSettings {
    interface Say {
        void say(String text);
    }

    private static final String[] POWER = {"power-saver", "balanced", "performance"};

    private final AppCompatActivity a;
    private final Deck deck;
    private final Say say;
    private final TextView wifi, bt, dnd, night, power, audio;
    private JSONObject last = new JSONObject();

    QuickSettings(AppCompatActivity a, Deck deck, Say say) {
        this.a = a;
        this.deck = deck;
        this.say = say;
        wifi = a.findViewById(R.id.qs_wifi);
        bt = a.findViewById(R.id.qs_bt);
        dnd = a.findViewById(R.id.qs_dnd);
        night = a.findViewById(R.id.qs_night);
        power = a.findViewById(R.id.qs_power);
        audio = a.findViewById(R.id.qs_audio);

        wifi.setOnClickListener(v -> setWifi(!radioOn("wifi"), false));
        wifi.setOnLongClickListener(v -> details("wifi"));
        bt.setOnClickListener(v -> toggle("bt.set", !radioOn("bluetooth")));
        bt.setOnLongClickListener(v -> details("bt"));
        dnd.setOnClickListener(v -> toggle("dnd", !last.optBoolean("dnd")));
        night.setOnClickListener(v -> toggle("night.set", !last.optBoolean("night_light")));
        power.setOnClickListener(v -> nextPower());
        audio.setOnClickListener(v -> chooseOutput());
        render(last);
    }

    private boolean radioOn(String key) {
        JSONObject o = last.optJSONObject(key);
        return o != null && o.optBoolean("on");
    }

    void refresh() {
        deck.send("settings", "{}", (ok, err, data) -> {
            if (!ok || data == null) return;
            try {
                last = new JSONObject(data);
                render(last);
            } catch (JSONException ignored) {
            }
        });
    }

    private void render(JSONObject s) {
        JSONObject w = s.optJSONObject("wifi"), b = s.optJSONObject("bluetooth");
        boolean wOn = w != null && w.optBoolean("on"), bOn = b != null && b.optBoolean("on");
        String ssid = w == null ? "" : w.optString("ssid"), dev = b == null ? "" : b.optString("device");
        tile(wifi, R.drawable.ic_wifi, R.string.qs_wifi, wOn, !wOn ? a.getString(R.string.qs_off) : ssid.isEmpty() ? a.getString(R.string.qs_no_network) : ssid);
        tile(bt, R.drawable.ic_bluetooth, R.string.qs_bt, bOn, !bOn ? a.getString(R.string.qs_off) : dev.isEmpty() ? a.getString(R.string.qs_no_device) : dev);
        tile(dnd, R.drawable.ic_bell_off, R.string.qs_dnd, s.optBoolean("dnd"), onOff(s.optBoolean("dnd")));
        tile(night, R.drawable.ic_night, R.string.qs_night, s.optBoolean("night_light"), onOff(s.optBoolean("night_light")));
        String p = s.optString("power");
        tile(power, R.drawable.ic_gauge, R.string.qs_power, "performance".equals(p), powerLabel(p));
        String out = s.optString("output");
        tile(audio, R.drawable.ic_speaker, R.string.qs_audio, false, out.isEmpty() ? "–" : out);
    }

    private String onOff(boolean on) {
        return a.getString(on ? R.string.qs_on : R.string.qs_off);
    }

    private String powerLabel(String p) {
        switch (p) {
            case "power-saver":
                return a.getString(R.string.power_power_saver);
            case "balanced":
                return a.getString(R.string.power_balanced);
            case "performance":
                return a.getString(R.string.power_performance);
            default:
                return "–";
        }
    }

    /** Label on the first line, state below it, smaller and quieter. */
    private void tile(TextView t, int icon, int label, boolean on, String sub) {
        String l = a.getString(label);
        SpannableString text = new SpannableString(l + "\n" + sub);
        int color = a.getColor(on ? R.color.on_ink : R.color.text2);
        text.setSpan(new AbsoluteSizeSpan(11, true), l.length() + 1, text.length(), Spanned.SPAN_EXCLUSIVE_EXCLUSIVE);
        text.setSpan(new ForegroundColorSpan(color), l.length() + 1, text.length(), Spanned.SPAN_EXCLUSIVE_EXCLUSIVE);
        t.setText(text);
        t.setTextColor(a.getColor(on ? R.color.on_ink : R.color.text1));
        t.setBackgroundResource(on ? R.drawable.bg_key_on : R.drawable.bg_secondary);
        Drawable d = AppCompatResources.getDrawable(a, icon);
        if (d != null) {
            d = d.mutate();
            d.setTint(a.getColor(on ? R.color.on_ink : R.color.text1));
        }
        t.setCompoundDrawablesRelativeWithIntrinsicBounds(null, d, null, null);
        t.setContentDescription(l + ", " + sub);
    }

    private boolean details(String page) {
        wifi.performHapticFeedback(HapticFeedbackConstants.LONG_PRESS);
        a.startActivity(new Intent(a, SettingsActivity.class).putExtra("page", page));
        return true;
    }

    private void toggle(String type, boolean on) {
        send(type, json("on", on));
    }

    /** WLAN off can cut the notebook off; the agent says so and the phone asks once. */
    private void setWifi(boolean on, boolean confirm) {
        JSONObject body = json("on", on);
        try {
            body.put("confirm", confirm);
        } catch (JSONException ignored) {
        }
        deck.send("wifi.set", body.toString(), (ok, err, data) -> {
            if (!ok && err != null && err.contains("unreachable")) {
                Ui.confirm(a, a.getString(R.string.wifi_cut_title), a.getString(R.string.wifi_cut_body),
                        a.getString(R.string.wifi_cut_action), true, () -> setWifi(false, true));
                return;
            }
            if (!ok) say.say(err);
            refreshSoon();
        });
    }

    private void nextPower() {
        String cur = last.optString("power");
        int i = 0;
        for (int k = 0; k < POWER.length; k++) if (POWER[k].equals(cur)) i = k;
        String next = POWER[(i + 1) % POWER.length];
        JSONObject body = new JSONObject();
        try {
            body.put("profile", next);
        } catch (JSONException ignored) {
        }
        send("power.set", body);
    }

    private void chooseOutput() {
        deck.send("audio.outputs", "{}", (ok, err, data) -> {
            if (!ok) {
                say.say(err);
                return;
            }
            try {
                JSONArray outs = new JSONArray(data);
                String[] names = new String[outs.length()], labels = new String[outs.length()];
                int checked = -1;
                for (int i = 0; i < outs.length(); i++) {
                    JSONObject o = outs.getJSONObject(i);
                    names[i] = o.getString("name");
                    labels[i] = o.optString("description", names[i]);
                    if (o.optBoolean("default")) checked = i;
                }
                new AlertDialog.Builder(a).setTitle(R.string.audio_output_title)
                        .setSingleChoiceItems(labels, checked, (d, w) -> {
                            d.dismiss();
                            JSONObject body = new JSONObject();
                            try {
                                body.put("name", names[w]);
                            } catch (JSONException ignored) {
                            }
                            send("audio.output", body);
                        }).show();
            } catch (JSONException e) {
                say.say(e.getMessage());
            }
        });
    }

    private void send(String type, JSONObject body) {
        deck.send(type, body.toString(), (ok, err, data) -> {
            if (!ok) say.say(err);
            refreshSoon();
        });
    }

    // radios take a moment to report their new state
    private void refreshSoon() {
        wifi.postDelayed(this::refresh, 600);
        wifi.postDelayed(this::refresh, 2500);
    }

    private static JSONObject json(String key, boolean value) {
        JSONObject o = new JSONObject();
        try {
            o.put(key, value);
        } catch (JSONException ignored) {
        }
        return o;
    }
}

package de.raindancer118.pocketdeck;

import android.app.AlertDialog;
import android.os.Bundle;
import android.text.InputType;
import android.view.LayoutInflater;
import android.view.View;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.TextView;

import androidx.appcompat.app.AppCompatActivity;
import androidx.appcompat.widget.SwitchCompat;

import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

import de.raindancer118.pocketcore.Ui;

/** WLAN networks or Bluetooth devices of the computer ("page" = wifi | bt). */
public class SettingsActivity extends AppCompatActivity {
    private Deck deck;
    private boolean wifiPage;
    private LinearLayout list;
    private TextView message;
    private SwitchCompat power;
    private boolean updating; // setting the switch from state must not send a command

    @Override
    protected void onCreate(Bundle s) {
        super.onCreate(s);
        setContentView(R.layout.activity_settings);
        deck = Deck.get(this);
        wifiPage = !"bt".equals(getIntent().getStringExtra("page"));
        list = findViewById(R.id.list);
        message = findViewById(R.id.message);
        power = findViewById(R.id.power);
        ((TextView) findViewById(R.id.title)).setText(wifiPage ? R.string.wifi_title : R.string.bt_title);
        findViewById(R.id.back).setOnClickListener(v -> finish());
        power.setOnCheckedChangeListener((b, on) -> {
            if (!updating) setPower(on, false);
        });
    }

    @Override
    protected void onResume() {
        super.onResume();
        deck.startPolling();
        load();
    }

    private void load() {
        deck.send("settings", "{}", (ok, err, data) -> {
            if (!ok) {
                say(err);
                return;
            }
            try {
                JSONObject st = new JSONObject(data).getJSONObject(wifiPage ? "wifi" : "bluetooth");
                updating = true;
                power.setChecked(st.optBoolean("on"));
                updating = false;
                if (st.optBoolean("on")) loadList();
                else list.removeAllViews();
            } catch (JSONException e) {
                say(e.getMessage());
            }
        });
    }

    private void loadList() {
        if (wifiPage) say(getString(R.string.wifi_scanning));
        deck.send(wifiPage ? "wifi.list" : "bt.list", "{}", (ok, err, data) -> {
            if (!ok) {
                say(err);
                return;
            }
            try {
                JSONArray a = new JSONArray(data);
                list.removeAllViews();
                for (int i = 0; i < a.length(); i++) {
                    JSONObject o = a.getJSONObject(i);
                    if (wifiPage) addNetwork(o);
                    else addDevice(o);
                }
                say(a.length() > 0 ? null : getString(wifiPage ? R.string.wifi_none : R.string.bt_none));
            } catch (JSONException e) {
                say(e.getMessage());
            }
        });
    }

    private View row(String name, String meta, int icon, boolean active) {
        View row = LayoutInflater.from(this).inflate(R.layout.item_file, list, false);
        TextView n = row.findViewById(R.id.name), m = row.findViewById(R.id.meta);
        n.setText(name);
        n.setCompoundDrawablesRelativeWithIntrinsicBounds(icon, 0, 0, 0);
        m.setText(meta);
        if (active) {
            m.setTextColor(getColor(R.color.text1));
            m.setCompoundDrawablesRelativeWithIntrinsicBounds(R.drawable.dot_live, 0, 0, 0);
            m.setCompoundDrawablePadding(Math.round(6 * getResources().getDisplayMetrics().density));
        }
        list.addView(row);
        return row;
    }

    // ---- WLAN ----

    private void addNetwork(JSONObject o) {
        String ssid = o.optString("ssid");
        boolean active = o.optBoolean("active"), secure = o.optBoolean("secure"), known = o.optBoolean("known");
        StringBuilder meta = new StringBuilder();
        if (active) meta.append(getString(R.string.wifi_connected, ssid));
        else meta.append(o.optInt("signal")).append(" %");
        if (!active && (known || !secure)) meta.append(" · ").append(getString(known ? R.string.wifi_saved : R.string.wifi_open));
        View row = row(ssid, meta.toString(), secure ? R.drawable.ic_lock_small : R.drawable.ic_wifi, active);
        row.setOnClickListener(v -> {
            if (active) {
                Ui.confirm(this, getString(R.string.wifi_disconnect_title, ssid), "", getString(R.string.wifi_disconnect), false,
                        () -> disconnect(false));
            } else if (secure && !known) {
                askPassword(ssid);
            } else {
                connect(ssid, null);
            }
        });
    }

    private void askPassword(String ssid) {
        EditText in = new EditText(this);
        in.setInputType(InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_VARIATION_PASSWORD);
        in.setHint(R.string.wifi_password_hint);
        int pad = Math.round(20 * getResources().getDisplayMetrics().density);
        in.setPadding(pad, pad / 2, pad, pad / 2);
        new AlertDialog.Builder(this).setTitle(getString(R.string.wifi_password_title, ssid)).setView(in)
                .setPositiveButton(R.string.connect, (d, w) -> connect(ssid, in.getText().toString()))
                .setNegativeButton(R.string.cancel, null).show();
    }

    private void connect(String ssid, String password) {
        say(getString(R.string.wifi_connecting, ssid));
        JSONObject body = new JSONObject();
        try {
            body.put("ssid", ssid);
            if (password != null && !password.isEmpty()) body.put("password", password);
        } catch (JSONException ignored) {
        }
        deck.send("wifi.connect", body.toString(), (ok, err, data) -> {
            say(ok ? getString(R.string.wifi_connected, ssid) : err);
            loadList();
        });
    }

    private void disconnect(boolean confirm) {
        deck.send("wifi.disconnect", "{\"confirm\":" + confirm + "}", (ok, err, data) -> {
            if (!ok && err != null && err.contains("unreachable")) {
                Ui.confirm(this, getString(R.string.wifi_cut_title), getString(R.string.wifi_cut_body),
                        getString(R.string.wifi_cut_action), true, () -> disconnect(true));
                return;
            }
            say(ok ? null : err);
            loadList();
        });
    }

    // ---- Bluetooth ----

    private void addDevice(JSONObject o) {
        String mac = o.optString("mac"), name = o.optString("name");
        boolean connected = o.optBoolean("connected");
        View row = row(name, getString(connected ? R.string.bt_connected : R.string.bt_paired), R.drawable.ic_bluetooth, connected);
        row.setOnClickListener(v -> {
            if (!connected) say(getString(R.string.bt_connecting, name));
            deck.send(connected ? "bt.disconnect" : "bt.connect", "{\"mac\":\"" + mac + "\"}", (ok, err, data) -> {
                say(ok ? null : err);
                loadList();
            });
        });
    }

    // ---- on / off ----

    private void setPower(boolean on, boolean confirm) {
        String type = wifiPage ? "wifi.set" : "bt.set";
        deck.send(type, "{\"on\":" + on + ",\"confirm\":" + confirm + "}", (ok, err, data) -> {
            if (!ok && err != null && err.contains("unreachable")) {
                Ui.confirm(this, getString(R.string.wifi_cut_title), getString(R.string.wifi_cut_body),
                        getString(R.string.wifi_cut_action), true, () -> setPower(false, true));
                load(); // switch back to the real state until confirmed
                return;
            }
            if (!ok) say(err);
            // radios need a moment before they list anything
            list.postDelayed(this::load, on ? 2500 : 600);
        });
    }

    private void say(String s) {
        message.setVisibility(s == null ? View.GONE : View.VISIBLE);
        message.setText(s);
    }
}

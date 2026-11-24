package de.raindancer118.pocketsudo;

import android.Manifest;
import android.app.NotificationManager;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.os.PowerManager;
import android.provider.Settings;
import android.text.SpannableStringBuilder;
import android.text.Spanned;
import android.text.style.ForegroundColorSpan;
import android.text.style.TypefaceSpan;
import android.view.LayoutInflater;
import android.view.View;
import android.widget.ImageButton;
import android.widget.LinearLayout;
import android.widget.TextView;

import androidx.appcompat.app.AppCompatActivity;
import androidx.core.content.res.ResourcesCompat;

import com.google.mlkit.vision.codescanner.GmsBarcodeScanning;

import java.util.List;

/** Home: the paired notebooks, centred. Everything else shows up only when needed. */
public class MainActivity extends AppCompatActivity {
    private Store store;
    private LinearLayout center, issue;
    private TextView issueText, issueFix, scan, history, pairMore;

    @Override
    protected void onCreate(Bundle state) {
        super.onCreate(state);
        setContentView(R.layout.activity_main);
        store = new Store(this);
        center = findViewById(R.id.center);
        issue = findViewById(R.id.issue);
        issueText = findViewById(R.id.issue_text);
        issueFix = findViewById(R.id.issue_fix);
        scan = findViewById(R.id.scan);
        history = findViewById(R.id.history);
        pairMore = findViewById(R.id.pair_more);
        Ui.link(history, pairMore, issueFix);

        TextView wordmark = findViewById(R.id.wordmark);
        SpannableStringBuilder wm = new SpannableStringBuilder("# " + getString(R.string.app_name));
        wm.setSpan(new ForegroundColorSpan(getColor(R.color.text2)), 0, 1, Spanned.SPAN_EXCLUSIVE_EXCLUSIVE);
        android.graphics.Typeface mono = ResourcesCompat.getFont(this, R.font.mono);
        if (mono != null) wm.setSpan(new TypefaceSpan(mono), 0, 1, Spanned.SPAN_EXCLUSIVE_EXCLUSIVE);
        wordmark.setText(wm);

        ImageButton theme = findViewById(R.id.theme);
        theme.setImageResource(Ui.isDark(this) ? R.drawable.ic_sun : R.drawable.ic_moon);
        theme.setOnClickListener(v -> Ui.toggleTheme(this));

        scan.setOnClickListener(v -> scanCode());
        pairMore.setOnClickListener(v -> scanCode());
        history.setOnClickListener(v -> startActivity(new Intent(this, HistoryActivity.class)));

        if (Build.VERSION.SDK_INT >= 33 && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS)
                != PackageManager.PERMISSION_GRANTED && !store.pairings().isEmpty()) {
            requestPermissions(new String[]{Manifest.permission.POST_NOTIFICATIONS}, 1);
        }
    }

    @Override
    protected void onResume() {
        super.onResume();
        render();
        openPending();
    }

    private void render() {
        List<Store.Pairing> pairings = store.pairings();
        center.removeAllViews();
        boolean empty = pairings.isEmpty();
        scan.setVisibility(empty ? View.VISIBLE : View.GONE);
        history.setVisibility(!empty && !store.history().isEmpty() ? View.VISIBLE : View.GONE);
        pairMore.setVisibility(empty ? View.GONE : View.VISIBLE);

        if (empty) {
            TextView hint = text(R.style.Text_Small, getString(R.string.on_notebook));
            TextView cmd = text(R.style.Text_Command, null);
            cmd.setText(Ui.prompt(this, "sudo pocketsudo pair"));
            LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(-2, -2);
            lp.topMargin = dp(12);
            center.addView(hint);
            center.addView(cmd, lp);
        } else {
            LayoutInflater inf = LayoutInflater.from(this);
            for (Store.Pairing p : pairings) {
                View row = inf.inflate(R.layout.item_host, center, false);
                ((TextView) row.findViewById(R.id.name)).setText(p.hostname);
                TextView st = row.findViewById(R.id.state);
                if (p.gone) {
                    st.setText(R.string.pairing_gone);
                    st.setCompoundDrawablesRelativeWithIntrinsicBounds(R.drawable.dot_off, 0, 0, 0);
                } else {
                    st.setText(p.keyInvalid ? getString(R.string.issue_key, p.hostname) : getString(R.string.ready));
                    st.setCompoundDrawablesRelativeWithIntrinsicBounds(p.keyInvalid ? R.drawable.dot_warn : R.drawable.dot_live, 0, 0, 0);
                }
                row.setOnClickListener(v -> askUnpair(p));
                center.addView(row);
            }
        }
        renderIssue(empty);
    }

    /** At most one problem at a time, the most important first. */
    private void renderIssue(boolean empty) {
        issue.setVisibility(View.GONE);
        if (empty) return;
        NotificationManager nm = getSystemService(NotificationManager.class);
        PowerManager pm = getSystemService(PowerManager.class);
        if (!nm.areNotificationsEnabled()) {
            showIssue(R.string.issue_notifications, R.string.fix_notifications, () -> {
                if (Build.VERSION.SDK_INT >= 33 && shouldShowRequestPermissionRationale(Manifest.permission.POST_NOTIFICATIONS) == false
                        && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
                    requestPermissions(new String[]{Manifest.permission.POST_NOTIFICATIONS}, 1);
                }
                startActivity(new Intent(Settings.ACTION_APP_NOTIFICATION_SETTINGS)
                        .putExtra(Settings.EXTRA_APP_PACKAGE, getPackageName()));
            });
        } else if (dndBlocks(nm)) {
            showIssue(R.string.issue_dnd, R.string.fix_dnd, () -> startActivity(
                    new Intent(Settings.ACTION_CHANNEL_NOTIFICATION_SETTINGS)
                            .putExtra(Settings.EXTRA_APP_PACKAGE, getPackageName())
                            .putExtra(Settings.EXTRA_CHANNEL_ID, App.CHANNEL_REQUESTS)));
        } else if (!Push.ready(this)) {
            showIssue(R.string.issue_push, 0, null);
        } else if (!pm.isIgnoringBatteryOptimizations(getPackageName())) {
            showIssue(R.string.issue_battery, R.string.fix_battery, () -> startActivity(
                    new Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS, Uri.parse("package:" + getPackageName()))));
        } else if (Build.VERSION.SDK_INT >= 34 && !nm.canUseFullScreenIntent()) {
            showIssue(R.string.issue_fullscreen, R.string.fix_fullscreen, () -> startActivity(
                    new Intent(Settings.ACTION_MANAGE_APP_USE_FULL_SCREEN_INTENT, Uri.parse("package:" + getPackageName()))));
        }
    }

    /** Do Not Disturb is on and the requests channel may not break through. */
    private static boolean dndBlocks(NotificationManager nm) {
        if (nm.getCurrentInterruptionFilter() == NotificationManager.INTERRUPTION_FILTER_ALL) return false;
        android.app.NotificationChannel ch = nm.getNotificationChannel(App.CHANNEL_REQUESTS);
        return ch == null || !ch.canBypassDnd();
    }

    private void showIssue(int text, int fix, Runnable action) {
        issue.setVisibility(View.VISIBLE);
        issueText.setText(text);
        issueText.setCompoundDrawablesRelativeWithIntrinsicBounds(R.drawable.dot_warn, 0, 0, 0);
        issueFix.setVisibility(fix == 0 ? View.GONE : View.VISIBLE);
        if (fix != 0) {
            issueFix.setText(fix);
            issueFix.setOnClickListener(v -> action.run());
        }
    }

    /** Without working push the app still shows waiting requests when opened. */
    private void openPending() {
        if (store.pairings().isEmpty()) return;
        App.IO.execute(() -> {
            List<Requests.Pair> pending = Requests.pending(this);
            Requests.sweepNotifications(this, pending);
            runOnUiThread(() -> {
                if (!isFinishing()) render();
            });
            if (pending.isEmpty()) return;
            Requests.Pair first = pending.get(0);
            runOnUiThread(() -> {
                if (!isFinishing()) startActivity(Requests.approvalIntent(this, first.pairing.channel, first.request.id));
            });
        });
    }

    private void askUnpair(Store.Pairing p) {
        Ui.confirm(this, getString(R.string.unpair_title, p.hostname), getString(R.string.unpair_body),
                getString(R.string.unpair), true, () -> App.IO.execute(() -> {
                    try {
                        Relay.of(p).call("DELETE", "/v1/channels/" + p.channel, null, 15000);
                    } catch (Exception ignored) {
                        // best effort: the local side is what stops approvals
                    }
                    Keys.delete(p.keyAlias);
                    store.removePairing(p.channel);
                    runOnUiThread(this::render);
                }));
    }

    private void scanCode() {
        GmsBarcodeScanning.getClient(this).startScan()
                .addOnSuccessListener(b -> {
                    String raw = b.getRawValue();
                    startActivity(new Intent(this, PairActivity.class).setData(Uri.parse(raw == null ? "" : raw)));
                });
    }

    private TextView text(int style, String s) {
        TextView t = new TextView(this, null, 0, style);
        t.setGravity(android.view.Gravity.CENTER);
        if (s != null) t.setText(s);
        return t;
    }

    private int dp(int v) {
        return Math.round(v * getResources().getDisplayMetrics().density);
    }
}

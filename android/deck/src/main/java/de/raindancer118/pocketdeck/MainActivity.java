package de.raindancer118.pocketdeck;

import android.content.ClipData;
import android.content.ClipboardManager;
import android.content.Intent;
import android.graphics.Bitmap;
import android.graphics.BitmapFactory;
import android.graphics.Outline;
import android.net.Uri;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.security.keystore.KeyPermanentlyInvalidatedException;
import android.view.HapticFeedbackConstants;
import android.view.View;
import android.view.ViewOutlineProvider;
import android.widget.ImageButton;
import android.widget.ImageView;
import android.widget.SeekBar;
import android.widget.TextView;

import androidx.annotation.NonNull;
import androidx.appcompat.app.AppCompatActivity;
import androidx.biometric.BiometricManager;
import androidx.biometric.BiometricPrompt;
import androidx.core.content.ContextCompat;

import com.google.mlkit.vision.codescanner.GmsBarcodeScanning;

import org.json.JSONException;
import org.json.JSONObject;

import java.io.InputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.security.GeneralSecurityException;
import java.security.Signature;
import java.util.Base64;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

import de.raindancer118.pocketcore.DeckCrypto;
import de.raindancer118.pocketcore.Keys;
import de.raindancer118.pocketcore.Ui;

public class MainActivity extends AppCompatActivity {
    private static final long REFRESH_MS = 4000;

    private Deck deck;
    private final Handler main = new Handler(Looper.getMainLooper());
    private final ExecutorService io = Executors.newSingleThreadExecutor();
    private View viewStart, viewRemote, viewShortcuts, viewEmpty, tabs, tabLine;
    private TextView host, status, lockLabel, message, tabStart, tabRemote, tabShortcuts, shortcutMessage;
    private TextView playingOn, track, artist, volumeLabel, brightnessLabel, remoteMessage;
    private ImageButton lockButton, play;
    private ImageView cover;
    private SeekBar volume, brightness;
    private static final long HOLD_MS = 700;
    private boolean locked, dragging;
    private int tab;
    private String coverUrl;
    private final Runnable refresher = new Runnable() {
        @Override
        public void run() {
            refresh();
            main.postDelayed(this, REFRESH_MS);
        }
    };

    @Override
    protected void onCreate(Bundle state) {
        super.onCreate(state);
        setContentView(R.layout.activity_main);
        deck = Deck.get(this);
        viewStart = findViewById(R.id.view_start);
        viewRemote = findViewById(R.id.view_remote);
        viewShortcuts = findViewById(R.id.view_shortcuts);
        tabShortcuts = findViewById(R.id.tab_shortcuts);
        shortcutMessage = findViewById(R.id.shortcut_message);
        viewEmpty = findViewById(R.id.view_empty);
        tabs = findViewById(R.id.tabs);
        tabLine = findViewById(R.id.tab_line);
        host = findViewById(R.id.host);
        status = findViewById(R.id.status);
        lockButton = findViewById(R.id.lock_button);
        lockLabel = findViewById(R.id.lock_label);
        message = findViewById(R.id.message);
        tabStart = findViewById(R.id.tab_start);
        tabRemote = findViewById(R.id.tab_remote);
        playingOn = findViewById(R.id.playing_on);
        track = findViewById(R.id.track);
        artist = findViewById(R.id.artist);
        cover = findViewById(R.id.cover);
        play = findViewById(R.id.play);
        volume = findViewById(R.id.volume);
        brightness = findViewById(R.id.brightness);
        volumeLabel = findViewById(R.id.volume_label);
        brightnessLabel = findViewById(R.id.brightness_label);
        remoteMessage = findViewById(R.id.remote_message);

        ImageButton theme = findViewById(R.id.theme);
        theme.setImageResource(Ui.isDark(this) ? R.drawable.ic_sun : R.drawable.ic_moon);
        theme.setOnClickListener(v -> Ui.toggleTheme(this));

        View card = findViewById(R.id.cover_card);
        card.setClipToOutline(true);
        card.setOutlineProvider(new ViewOutlineProvider() {
            @Override
            public void getOutline(View v, Outline o) {
                o.setRoundRect(0, 0, v.getWidth(), v.getHeight(), getResources().getDimension(R.dimen.radius));
            }
        });

        tabStart.setOnClickListener(v -> showTab(0));
        tabShortcuts.setOnClickListener(v -> showTab(1));
        tabRemote.setOnClickListener(v -> showTab(2));
        Ui.link(findViewById(R.id.templates));
        findViewById(R.id.templates).setOnClickListener(v -> {
            Shortcuts store = new Shortcuts(this);
            for (Shortcuts.Shortcut sc : Shortcuts.templates(this)) store.save(sc);
            renderShortcuts();
        });
        lockButton.setOnClickListener(v -> {
            if (locked) unlock();
            else lockNow();
        });
        findViewById(R.id.scan).setOnClickListener(v -> GmsBarcodeScanning.getClient(this).startScan()
                .addOnSuccessListener(b -> startActivity(new Intent(this, PairActivity.class)
                        .setData(Uri.parse(b.getRawValue() == null ? "" : b.getRawValue())))));
        ((TextView) findViewById(R.id.pair_cmd)).setText(Ui.prompt(this, "pocketdeck pair"));

        findViewById(R.id.prev).setOnClickListener(v -> media("previous"));
        play.setOnClickListener(v -> media("play_pause"));
        findViewById(R.id.next).setOnClickListener(v -> media("next"));
        slider(volume, "volume.set", R.string.volume);
        slider(brightness, "brightness.set", R.string.brightness);
        row(R.id.clip_get, R.drawable.ic_clip, this::clipGet);
        row(R.id.clip_send, R.drawable.ic_clip, this::clipSend);
        row(R.id.open_link, R.drawable.ic_link, this::openLink);
        row(R.id.keyboard, R.drawable.ic_keyboard, () -> startActivity(new Intent(this, KeyboardActivity.class)));
        androidx.activity.result.ActivityResultLauncher<String[]> pick = registerForActivityResult(
                new androidx.activity.result.contract.ActivityResultContracts.OpenDocument(), uri -> {
                    if (uri == null) return;
                    say(remoteMessage, getString(R.string.file_sending));
                    Transfer.sendFile(this, uri, (ok, err, data) -> say(remoteMessage, ok ? getString(R.string.file_sent) : err));
                });
        row(R.id.send_file, R.drawable.ic_file, () -> pick.launch(new String[]{"*/*"}));
        row(R.id.files, R.drawable.ic_folder, () -> startActivity(new Intent(this, FilesActivity.class)));
        row(R.id.screenshot, R.drawable.ic_screen, () -> startActivity(new Intent(this, ScreenshotActivity.class)));
    }

    @Override
    protected void onResume() {
        super.onResume();
        boolean paired = deck.pairing() != null;
        viewEmpty.setVisibility(paired ? View.GONE : View.VISIBLE);
        tabs.setVisibility(paired ? View.VISIBLE : View.GONE);
        tabLine.setVisibility(paired ? View.VISIBLE : View.GONE);
        if (!paired) {
            viewStart.setVisibility(View.GONE);
            viewRemote.setVisibility(View.GONE);
            viewShortcuts.setVisibility(View.GONE);
            return;
        }
        host.setText(deck.pairing().hostname);
        showTab(tab);
        renderShortcuts();
        deck.startPolling();
        main.post(refresher);
        Session.ensure(this);
    }

    @Override
    protected void onPause() {
        main.removeCallbacks(refresher);
        deck.stopPolling();
        super.onPause();
    }

    private void showTab(int t) {
        tab = t;
        viewStart.setVisibility(t == 0 ? View.VISIBLE : View.GONE);
        viewShortcuts.setVisibility(t == 1 ? View.VISIBLE : View.GONE);
        viewRemote.setVisibility(t == 2 ? View.VISIBLE : View.GONE);
        styleTab(tabStart, t == 0);
        styleTab(tabShortcuts, t == 1);
        styleTab(tabRemote, t == 2);
    }

    // ---- shortcuts ----

    private void renderShortcuts() {
        android.widget.LinearLayout grid = findViewById(R.id.grid);
        grid.removeAllViews();
        java.util.List<Shortcuts.Shortcut> list = new Shortcuts(this).all();
        findViewById(R.id.templates).setVisibility(list.isEmpty() ? View.VISIBLE : View.GONE);
        java.util.List<View> tiles = new java.util.ArrayList<>();
        for (Shortcuts.Shortcut sc : list) tiles.add(shortcutTile(sc));
        tiles.add(newTile());
        android.widget.LinearLayout row = null;
        for (int i = 0; i < tiles.size(); i++) {
            if (i % 2 == 0) {
                row = new android.widget.LinearLayout(this);
                android.widget.LinearLayout.LayoutParams rlp = new android.widget.LinearLayout.LayoutParams(-1, dp(104));
                rlp.bottomMargin = dp(12);
                grid.addView(row, rlp);
            }
            android.widget.LinearLayout.LayoutParams lp = new android.widget.LinearLayout.LayoutParams(0, -1, 1f);
            if (i % 2 == 0) lp.setMarginEnd(dp(12));
            row.addView(tiles.get(i), lp);
        }
        if (tiles.size() % 2 == 1) row.addView(new View(this), new android.widget.LinearLayout.LayoutParams(0, -1, 1f));
    }

    private View shortcutTile(Shortcuts.Shortcut sc) {
        View v = getLayoutInflater().inflate(R.layout.item_shortcut, (android.view.ViewGroup) findViewById(R.id.grid), false);
        ((android.widget.ImageView) v.findViewById(R.id.icon)).setImageResource(iconRes(sc.icon));
        ((TextView) v.findViewById(R.id.name)).setText(sc.name.isEmpty() ? getString(R.string.unnamed) : sc.name);
        int n = sc.steps.size();
        String meta = getResources().getQuantityString(R.plurals.steps_count, n, n);
        if (sc.needsFinger()) meta += " · ⌁";
        ((TextView) v.findViewById(R.id.meta)).setText(meta);
        // hold to run: the tile fills up linearly, letting go early cancels
        View fill = v.findViewById(R.id.fill);
        fill.setPivotX(0);
        android.animation.ObjectAnimator hold = android.animation.ObjectAnimator.ofFloat(fill, View.SCALE_X, 0f, 1f);
        hold.setDuration(HOLD_MS);
        hold.setInterpolator(new android.view.animation.LinearInterpolator());
        hold.addListener(new android.animation.AnimatorListenerAdapter() {
            private boolean cancelled;

            @Override
            public void onAnimationStart(android.animation.Animator a) {
                cancelled = false;
            }

            @Override
            public void onAnimationCancel(android.animation.Animator a) {
                cancelled = true;
            }

            @Override
            public void onAnimationEnd(android.animation.Animator a) {
                fill.setScaleX(0f);
                if (cancelled) return;
                v.performHapticFeedback(HapticFeedbackConstants.CONFIRM);
                say(shortcutMessage, getString(R.string.shortcut_running, sc.name));
                ShortcutRunner.run(MainActivity.this, sc, (ok, err, d) -> say(shortcutMessage, ok ? getString(R.string.shortcut_done, sc.name) : err));
            }
        });
        v.setOnTouchListener((x, e) -> {
            switch (e.getActionMasked()) {
                case android.view.MotionEvent.ACTION_DOWN:
                    hold.start();
                    break;
                case android.view.MotionEvent.ACTION_UP:
                case android.view.MotionEvent.ACTION_CANCEL:
                    boolean tap = hold.isRunning() && hold.getCurrentPlayTime() < 250;
                    hold.cancel();
                    if (tap && e.getActionMasked() == android.view.MotionEvent.ACTION_UP) {
                        x.performClick();
                    }
                    break;
                default:
            }
            return false;
        });
        v.setOnClickListener(x -> startActivity(new Intent(this, ShortcutEditActivity.class).putExtra("id", sc.id)));
        return v;
    }

    private View newTile() {
        View v = getLayoutInflater().inflate(R.layout.item_shortcut, (android.view.ViewGroup) findViewById(R.id.grid), false);
        ((android.widget.ImageView) v.findViewById(R.id.icon)).setImageResource(R.drawable.ic_plus);
        ((TextView) v.findViewById(R.id.name)).setText(R.string.new_shortcut);
        ((TextView) v.findViewById(R.id.meta)).setText("");
        v.setOnClickListener(x -> startActivity(new Intent(this, ShortcutEditActivity.class)));
        return v;
    }

    static int iconRes(String icon) {
        switch (icon) {
            case "code":
                return R.drawable.ic_code;
            case "moon":
                return R.drawable.ic_moon;
            case "screen":
                return R.drawable.ic_screen;
            case "lock":
                return R.drawable.ic_lock;
            default:
                return R.drawable.ic_zap;
        }
    }

    private int dp(int v) {
        return Math.round(v * getResources().getDisplayMetrics().density);
    }

    private void styleTab(TextView t, boolean on) {
        int c = getColor(on ? R.color.text1 : R.color.text3);
        t.setTextColor(c);
        t.setTypeface(null, on ? android.graphics.Typeface.BOLD : android.graphics.Typeface.NORMAL);
        for (android.graphics.drawable.Drawable d : t.getCompoundDrawablesRelative()) {
            if (d != null) d.setTint(c);
        }
    }

    // ---- state ----

    private void refresh() {
        deck.send("state", "{}", (ok, err, data) -> {
            if (!ok || data == null) {
                status.setText(R.string.offline);
                status.setCompoundDrawablesRelativeWithIntrinsicBounds(R.drawable.dot_off, 0, 0, 0);
                return;
            }
            try {
                apply(new JSONObject(data));
            } catch (JSONException ignored) {
            }
        });
    }

    private void apply(JSONObject st) {
        locked = st.optBoolean("locked");
        StringBuilder line = new StringBuilder(getString(locked ? R.string.locked : R.string.unlocked));
        int bat = st.optInt("battery", -1);
        if (bat >= 0) {
            line.append("  ·  ").append(getString(R.string.battery, bat));
            if (st.optBoolean("charging")) line.append(" ").append(getString(R.string.charging));
        }
        status.setText(line);
        status.setCompoundDrawablesRelativeWithIntrinsicBounds(locked ? R.drawable.dot_warn : R.drawable.dot_live, 0, 0, 0);
        lockButton.setImageResource(locked ? R.drawable.ic_fp_big : R.drawable.ic_lock);
        lockLabel.setText(locked ? R.string.unlock : R.string.lock);
        lockButton.setContentDescription(lockLabel.getText());

        if (!dragging) {
            volume.setProgress(Math.min(100, st.optInt("volume")));
            brightness.setProgress(st.optInt("brightness"));
        }
        volumeLabel.setText(getString(R.string.volume, volume.getProgress()));
        brightnessLabel.setText(getString(R.string.brightness, brightness.getProgress()));

        JSONObject m = st.optJSONObject("media");
        if (m == null) {
            playingOn.setText(R.string.nothing_playing);
            track.setText("");
            artist.setText("");
            setCover(null);
        } else {
            playingOn.setText(getString(R.string.playing_on, st.optString("hostname")));
            track.setText(m.optString("title"));
            String a = m.optString("artist"), p = m.optString("player");
            artist.setText(a.isEmpty() ? p : a + " · " + p);
            play.setImageResource(m.optBoolean("playing") ? R.drawable.ic_pause : R.drawable.ic_play);
            setCover(m.optString("art_url", null));
        }
    }

    /** Remote covers only (https); loaded once per track. */
    private void setCover(String url) {
        if (url == null || url.isEmpty()) {
            coverUrl = null;
            cover.setImageDrawable(null);
            return;
        }
        if (url.equals(coverUrl)) return;
        coverUrl = url;
        io.execute(() -> {
            try {
                HttpURLConnection c = (HttpURLConnection) new URL(url).openConnection();
                c.setConnectTimeout(5000);
                c.setReadTimeout(8000);
                try (InputStream in = c.getInputStream()) {
                    Bitmap b = BitmapFactory.decodeStream(in);
                    runOnUiThread(() -> {
                        if (url.equals(coverUrl)) cover.setImageBitmap(b);
                    });
                }
            } catch (Exception ignored) {
            }
        });
    }

    // ---- lock / unlock ----

    private void lockNow() {
        lockButton.performHapticFeedback(HapticFeedbackConstants.CONFIRM);
        deck.send("lock", "{}", (ok, err, data) -> {
            say(message, ok ? getString(R.string.locked_done) : err);
            refresh();
        });
    }

    private void unlock() {
        DeckStore.Pairing p = deck.pairing();
        if (BiometricManager.from(this).canAuthenticate(BiometricManager.Authenticators.BIOMETRIC_STRONG)
                != BiometricManager.BIOMETRIC_SUCCESS) {
            say(message, getString(R.string.no_biometric));
            return;
        }
        Signature sig;
        try {
            sig = Keys.signer(p.signAlias);
        } catch (KeyPermanentlyInvalidatedException e) {
            say(message, getString(R.string.key_invalid));
            return;
        } catch (GeneralSecurityException e) {
            say(message, e.getMessage());
            return;
        }
        DeckCrypto.Message m = DeckCrypto.newMessage("unlock", "{}");
        BiometricPrompt.PromptInfo info = new BiometricPrompt.PromptInfo.Builder()
                .setTitle(getString(R.string.bio_unlock, p.hostname))
                .setNegativeButtonText(getString(R.string.cancel))
                .setAllowedAuthenticators(BiometricManager.Authenticators.BIOMETRIC_STRONG)
                .setConfirmationRequired(false)
                .build();
        new BiometricPrompt(this, ContextCompat.getMainExecutor(this), new BiometricPrompt.AuthenticationCallback() {
            @Override
            public void onAuthenticationSucceeded(@NonNull BiometricPrompt.AuthenticationResult r) {
                BiometricPrompt.CryptoObject co = r.getCryptoObject();
                if (co == null || co.getSignature() == null) return;
                try {
                    Signature s = co.getSignature();
                    s.update(DeckCrypto.authMessage(m));
                    m.sig = Base64.getEncoder().encodeToString(s.sign());
                } catch (GeneralSecurityException e) {
                    say(message, e.getMessage());
                    return;
                }
                deck.send(m, (ok, err, data) -> {
                    if (ok) {
                        lockButton.performHapticFeedback(HapticFeedbackConstants.CONFIRM);
                        say(message, getString(R.string.unlocked_done));
                    } else {
                        say(message, err);
                    }
                    refresh();
                });
            }
        }).authenticate(info, new BiometricPrompt.CryptoObject(sig));
    }

    // ---- remote ----

    private void media(String action) {
        deck.send("media", "{\"action\":\"" + action + "\"}", (ok, err, data) -> {
            if (!ok) say(remoteMessage, err);
            main.postDelayed(this::refresh, 400);
        });
    }

    private void slider(SeekBar bar, String type, int label) {
        bar.setOnSeekBarChangeListener(new SeekBar.OnSeekBarChangeListener() {
            @Override
            public void onProgressChanged(SeekBar s, int p, boolean user) {
                ((TextView) ((View) s.getParent()).findViewById(s == volume ? R.id.volume_label : R.id.brightness_label))
                        .setText(getString(label, p));
            }

            @Override
            public void onStartTrackingTouch(SeekBar s) {
                dragging = true;
            }

            @Override
            public void onStopTrackingTouch(SeekBar s) {
                dragging = false;
                int p = Math.max(type.startsWith("brightness") ? 1 : 0, s.getProgress());
                deck.send(type, "{\"percent\":" + p + "}", (ok, err, data) -> {
                    if (!ok) say(remoteMessage, err);
                });
            }
        });
    }

    private void row(int id, int icon, Runnable action) {
        TextView t = findViewById(id);
        t.setCompoundDrawablesRelativeWithIntrinsicBounds(0, icon, 0, 0);
        t.setOnClickListener(v -> action.run());
    }

    private String phoneClipboard() {
        ClipboardManager cm = getSystemService(ClipboardManager.class);
        ClipData clip = cm.getPrimaryClip();
        if (clip == null || clip.getItemCount() == 0) return null;
        CharSequence s = clip.getItemAt(0).coerceToText(this);
        return s == null || s.length() == 0 ? null : s.toString();
    }

    private void clipGet() {
        deck.send("clipboard.get", "{}", (ok, err, data) -> {
            if (!ok) {
                say(remoteMessage, err);
                return;
            }
            try {
                String text = new JSONObject(data).getString("text");
                getSystemService(ClipboardManager.class).setPrimaryClip(ClipData.newPlainText("PocketDeck", text));
                say(remoteMessage, getString(R.string.clip_got));
            } catch (JSONException e) {
                say(remoteMessage, e.getMessage());
            }
        });
    }

    private void clipSend() {
        String text = phoneClipboard();
        if (text == null) {
            say(remoteMessage, getString(R.string.clip_empty));
            return;
        }
        deck.send("clipboard.set", body("text", text), (ok, err, data) -> say(remoteMessage, ok ? getString(R.string.clip_sent) : err));
    }

    private void openLink() {
        String text = phoneClipboard();
        if (text == null || !(text.trim().startsWith("http://") || text.trim().startsWith("https://"))) {
            say(remoteMessage, getString(R.string.no_link));
            return;
        }
        deck.send("open.url", body("url", text.trim()), (ok, err, data) -> say(remoteMessage, ok ? getString(R.string.link_opened) : err));
    }

    private static String body(String k, String v) {
        try {
            return new JSONObject().put(k, v).toString();
        } catch (JSONException e) {
            return "{}";
        }
    }

    private void say(TextView t, String text) {
        t.setVisibility(text == null ? View.GONE : View.VISIBLE);
        t.setText(text);
    }
}

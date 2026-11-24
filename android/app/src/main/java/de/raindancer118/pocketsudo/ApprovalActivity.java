package de.raindancer118.pocketsudo;

import android.animation.ObjectAnimator;
import android.animation.ValueAnimator;
import android.content.Intent;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.security.keystore.KeyPermanentlyInvalidatedException;
import android.graphics.drawable.Animatable;
import android.view.HapticFeedbackConstants;
import android.view.View;
import android.view.animation.AnimationUtils;
import android.view.animation.Interpolator;
import android.widget.ImageView;
import android.view.animation.LinearInterpolator;
import android.widget.TextView;

import androidx.annotation.NonNull;
import androidx.appcompat.app.AppCompatActivity;
import androidx.biometric.BiometricManager;
import androidx.biometric.BiometricPrompt;
import androidx.core.content.ContextCompat;

import java.io.IOException;
import java.security.GeneralSecurityException;
import java.security.Signature;

/** The one screen that matters: the waiting terminal line, its code, approve or deny. */
public class ApprovalActivity extends AppCompatActivity implements App.CancelListener {
    public static final String EXTRA_CHANNEL = "channel";
    public static final String EXTRA_ID = "id";
    private static final int WARN_SECONDS = 15;

    private Store store;
    private Store.Pairing pairing;
    private Protocol.Request request;
    private final Handler main = new Handler(Looper.getMainLooper());
    private ObjectAnimator countdown;
    private boolean finished;

    private TextView context, command, remote, code, codeHint, status, deny;
    private View approve, bar;

    @Override
    protected void onCreate(Bundle state) {
        super.onCreate(state);
        setContentView(R.layout.activity_approval);
        store = new Store(this);
        context = findViewById(R.id.context);
        command = findViewById(R.id.command);
        remote = findViewById(R.id.remote);
        code = findViewById(R.id.code);
        codeHint = findViewById(R.id.code_hint);
        status = findViewById(R.id.status);
        approve = findViewById(R.id.approve);
        deny = findViewById(R.id.deny);
        bar = findViewById(R.id.countdown);
        Ui.link(deny);
        approve.setOnClickListener(v -> startApproval());
        deny.setOnClickListener(v -> sendDeny());
        App.addCancelListener(this);
        load(getIntent());
    }

    @Override
    protected void onNewIntent(Intent intent) {
        super.onNewIntent(intent);
        setIntent(intent);
        finished = false;
        deny.setText(R.string.deny);
        deny.setOnClickListener(v -> sendDeny());
        approve.setVisibility(View.VISIBLE);
        approve.setAlpha(1f);
        code.setVisibility(View.VISIBLE);
        code.setAlpha(1f);
        codeHint.setAlpha(1f);
        findViewById(R.id.check).setVisibility(View.INVISIBLE);
        load(intent);
    }

    @Override
    protected void onDestroy() {
        if (request != null) Requests.cancelNotification(this, request.id);
        App.removeCancelListener(this);
        if (countdown != null) countdown.cancel();
        main.removeCallbacksAndMessages(null);
        super.onDestroy();
    }

    private void load(Intent intent) {
        String channel = intent.getStringExtra(EXTRA_CHANNEL), id = intent.getStringExtra(EXTRA_ID);
        pairing = channel == null ? null : store.pairing(channel);
        if (pairing == null || id == null) {
            finish();
            return;
        }
        setBusy(true);
        Protocol.Request cached = App.SEEN.get(id);
        if (cached != null) {
            show(cached);
        } else {
            context.setText(getString(R.string.notif_loading));
        }
        App.IO.execute(() -> {
            try {
                Protocol.Request r = Requests.fetch(pairing, id);
                runOnUiThread(() -> show(r));
            } catch (SecurityException | GeneralSecurityException e) {
                runOnUiThread(() -> end(getString(R.string.bad_signature), R.drawable.dot_warn, null));
            } catch (Relay.HttpException e) {
                runOnUiThread(() -> end(getString(e.code == 404 ? R.string.gone : R.string.send_failed), R.drawable.dot_off, null));
            } catch (IOException e) {
                if (cached == null) runOnUiThread(() -> end(getString(R.string.send_failed), R.drawable.dot_warn, null));
            }
        });
    }

    private void show(Protocol.Request r) {
        if (finished) return;
        request = r;
        App.HANDLED.add(r.id);
        Requests.cancelNotification(this, r.id);
        context.setText(getString(R.string.service_on_host, r.service, r.hostname));
        command.setText(Ui.prompt(this, r.command.isEmpty() ? r.service : r.command));
        if (r.remote) {
            remote.setVisibility(View.VISIBLE);
            remote.setText(r.rhost.isEmpty() ? getString(R.string.ssh) : getString(R.string.ssh_from, r.rhost));
        } else {
            remote.setVisibility(View.GONE);
        }
        code.setText(r.code);
        status.setVisibility(View.GONE);
        setBusy(false);
        startCountdown(r);
    }

    private void startCountdown(Protocol.Request r) {
        if (countdown != null) countdown.cancel();
        long total = Math.max(1, r.expires - r.created), left = r.secondsLeft();
        if (left <= 0) {
            expire();
            return;
        }
        bar.setPivotX(0);
        bar.setScaleX(left / (float) total);
        // constant motion: linear, as the house style asks for progress
        countdown = ObjectAnimator.ofFloat(bar, View.SCALE_X, bar.getScaleX(), 0f);
        countdown.setInterpolator(new LinearInterpolator());
        countdown.setDuration(left * 1000);
        countdown.addUpdateListener((ValueAnimator a) -> {
            if (request != null && request.secondsLeft() <= WARN_SECONDS) {
                bar.setBackgroundColor(getColor(R.color.warn));
            }
        });
        countdown.start();
        bar.setBackgroundColor(getColor(left <= WARN_SECONDS ? R.color.warn : R.color.live));
        main.removeCallbacksAndMessages(null);
        main.postDelayed(this::expire, left * 1000);
    }

    private void expire() {
        if (finished || request == null) return;
        store.record(request, Store.OUT_EXPIRED);
        end(getString(R.string.expired), R.drawable.dot_off, null);
    }

    private void setBusy(boolean busy) {
        approve.setEnabled(!busy);
        deny.setEnabled(!busy);
        approve.setAlpha(busy ? 0.5f : 1f);
    }

    // ---- approve ----

    private void startApproval() {
        if (request == null || finished) return;
        int can = BiometricManager.from(this).canAuthenticate(BiometricManager.Authenticators.BIOMETRIC_STRONG);
        if (can != BiometricManager.BIOMETRIC_SUCCESS) {
            showStatus(getString(R.string.no_biometric), R.drawable.dot_warn);
            return;
        }
        Signature sig;
        try {
            sig = Keys.signer(pairing.keyAlias);
        } catch (KeyPermanentlyInvalidatedException e) {
            pairing.keyInvalid = true;
            store.savePairing(pairing);
            end(getString(R.string.issue_key, pairing.hostname), R.drawable.dot_warn, null);
            return;
        } catch (GeneralSecurityException e) {
            showStatus(e.getMessage(), R.drawable.dot_warn);
            return;
        }
        BiometricPrompt.PromptInfo info = new BiometricPrompt.PromptInfo.Builder()
                .setTitle(getString(R.string.bio_title, request.service, request.hostname))
                .setSubtitle("$ " + request.command)
                .setNegativeButtonText(getString(R.string.cancel))
                .setAllowedAuthenticators(BiometricManager.Authenticators.BIOMETRIC_STRONG)
                .setConfirmationRequired(false)
                .build();
        new BiometricPrompt(this, ContextCompat.getMainExecutor(this), new BiometricPrompt.AuthenticationCallback() {
            @Override
            public void onAuthenticationSucceeded(@NonNull BiometricPrompt.AuthenticationResult result) {
                BiometricPrompt.CryptoObject co = result.getCryptoObject();
                if (co == null || co.getSignature() == null) return;
                sendApproval(co.getSignature());
            }

            @Override
            public void onAuthenticationError(int code, @NonNull CharSequence msg) {
                if (code != BiometricPrompt.ERROR_NEGATIVE_BUTTON && code != BiometricPrompt.ERROR_USER_CANCELED
                        && code != BiometricPrompt.ERROR_CANCELED) {
                    showStatus(msg.toString(), R.drawable.dot_warn);
                }
            }
        }).authenticate(info, new BiometricPrompt.CryptoObject(sig));
    }

    private void sendApproval(Signature unlocked) {
        Protocol.Request r = request;
        byte[] signature;
        try {
            unlocked.update(Protocol.responseMessage(Protocol.APPROVE, r.payload));
            signature = unlocked.sign();
        } catch (GeneralSecurityException e) {
            showStatus(e.getMessage(), R.drawable.dot_warn);
            return;
        }
        setBusy(true);
        showStatus(getString(R.string.sending), 0);
        App.IO.execute(() -> {
            try {
                Requests.approve(pairing, r, signature);
                App.SEEN.remove(r.id);
                store.record(r, Store.OUT_APPROVED);
                runOnUiThread(() -> success(r));
            } catch (Relay.HttpException e) {
                runOnUiThread(() -> end(getString(e.code == 410 ? R.string.decided_elsewhere : R.string.send_failed),
                        R.drawable.dot_off, null));
            } catch (IOException e) {
                runOnUiThread(() -> {
                    setBusy(false);
                    showStatus(getString(R.string.send_failed), R.drawable.dot_warn);
                });
            }
        });
    }

    /**
     * The moment: the prompt turns into a root prompt, the countdown line fills,
     * the code becomes a drawn check. Then the screen gets out of the way.
     */
    private void success(Protocol.Request r) {
        finished = true;
        if (countdown != null) countdown.cancel();
        main.removeCallbacksAndMessages(null);
        View root = findViewById(R.id.root);
        root.performHapticFeedback(HapticFeedbackConstants.CONFIRM);
        boolean motion = ValueAnimator.areAnimatorsEnabled();
        Interpolator ease = AnimationUtils.loadInterpolator(this, R.interpolator.ease_out);
        String cmd = r.command.isEmpty() ? r.service : r.command;

        bar.setBackgroundColor(getColor(R.color.live));
        status.setVisibility(View.GONE);
        ImageView check = findViewById(R.id.check);
        if (!motion) {
            bar.setScaleX(1f);
            command.setText(Ui.prompt(this, "#", cmd));
            code.setVisibility(View.INVISIBLE);
            codeHint.setVisibility(View.INVISIBLE);
            approve.setVisibility(View.INVISIBLE);
            deny.setVisibility(View.INVISIBLE);
            check.setVisibility(View.VISIBLE);
            ((Animatable) check.getDrawable()).start();
            showStatus(getString(R.string.approved), R.drawable.dot_live);
            main.postDelayed(this::finishAndRemoveTask, 1400);
            return;
        }
        bar.animate().scaleX(1f).setDuration(260).setInterpolator(ease).start();
        command.animate().alpha(0f).setDuration(100).withEndAction(() -> {
            command.setText(Ui.prompt(this, "#", cmd));
            command.animate().alpha(1f).setDuration(200).setInterpolator(ease).start();
        }).start();
        for (View v : new View[]{code, codeHint, approve, deny}) {
            v.animate().alpha(0f).setDuration(160).setInterpolator(ease)
                    .withEndAction(() -> v.setVisibility(View.INVISIBLE)).start();
        }
        main.postDelayed(() -> {
            check.setVisibility(View.VISIBLE);
            ((Animatable) check.getDrawable()).start();
            showStatus(getString(R.string.approved), R.drawable.dot_live);
            status.setAlpha(0f);
            status.animate().alpha(1f).setDuration(260).setInterpolator(ease).start();
        }, 140);
        main.postDelayed(this::finishAndRemoveTask, 1700);
    }

    // ---- deny ----

    private void sendDeny() {
        if (request == null || finished) return;
        Protocol.Request r = request;
        setBusy(true);
        App.IO.execute(() -> {
            try {
                Requests.deny(pairing, r.id);
            } catch (IOException ignored) {
                // the notebook times out anyway; a denial must never hang the UI
            }
            App.SEEN.remove(r.id);
            store.record(r, Store.OUT_DENIED);
            runOnUiThread(() -> end(getString(R.string.denied), R.drawable.dot_off, this::finishAndRemoveTask));
        });
    }

    @Override
    public void onCancelled(String requestId) {
        runOnUiThread(() -> {
            if (request != null && request.id.equals(requestId) && !finished) {
                end(getString(R.string.decided_elsewhere), R.drawable.dot_off, this::finishAndRemoveTask);
            }
        });
    }

    private void showStatus(String text, int dot) {
        status.setVisibility(View.VISIBLE);
        status.setText(text);
        status.setCompoundDrawablesRelativeWithIntrinsicBounds(dot, 0, 0, 0);
    }

    /** Final state: the decision stays readable for a moment, then the screen closes. */
    private void end(String text, int dot, Runnable after) {
        finished = true;
        if (countdown != null) countdown.cancel();
        main.removeCallbacksAndMessages(null);
        approve.setVisibility(View.INVISIBLE);
        codeHint.setVisibility(request == null ? View.GONE : View.VISIBLE);
        if (request != null) Requests.cancelNotification(this, request.id);
        showStatus(text, dot);
        // the deny link becomes the way out
        deny.setText(R.string.close);
        deny.setEnabled(true);
        deny.setVisibility(View.VISIBLE);
        deny.setOnClickListener(v -> finishAndRemoveTask());
        if (after != null) main.postDelayed(after, 1200);
    }
}

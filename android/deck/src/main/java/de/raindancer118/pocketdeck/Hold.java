package de.raindancer118.pocketdeck;

import android.animation.Animator;
import android.animation.AnimatorListenerAdapter;
import android.animation.ObjectAnimator;
import android.annotation.SuppressLint;
import android.view.HapticFeedbackConstants;
import android.view.MotionEvent;
import android.view.View;
import android.view.animation.LinearInterpolator;

/**
 * Hold to run: a fill layer grows linearly while the finger stays down, letting
 * go early cancels. A short tap still reaches the view's click listener.
 */
final class Hold {
    static final long HOLD_MS = 700;
    private static final long TAP_MS = 250;

    private Hold() {
    }

    @SuppressLint("ClickableViewAccessibility") // short taps are forwarded via performClick
    static void attach(View v, View fill, Runnable onHeld) {
        fill.setPivotX(0);
        fill.setScaleX(0f);
        ObjectAnimator hold = ObjectAnimator.ofFloat(fill, View.SCALE_X, 0f, 1f);
        hold.setDuration(HOLD_MS);
        hold.setInterpolator(new LinearInterpolator());
        hold.addListener(new AnimatorListenerAdapter() {
            private boolean cancelled;

            @Override
            public void onAnimationStart(Animator a) {
                cancelled = false;
            }

            @Override
            public void onAnimationCancel(Animator a) {
                cancelled = true;
            }

            @Override
            public void onAnimationEnd(Animator a) {
                fill.setScaleX(0f);
                if (cancelled) return;
                v.performHapticFeedback(HapticFeedbackConstants.CONFIRM);
                onHeld.run();
            }
        });
        v.setOnTouchListener((x, e) -> {
            switch (e.getActionMasked()) {
                case MotionEvent.ACTION_DOWN:
                    x.setPressed(true); // we consume the touch, so drive the press state ourselves
                    hold.start();
                    break;
                case MotionEvent.ACTION_UP:
                case MotionEvent.ACTION_CANCEL: // also when the list starts scrolling
                    boolean tap = hold.isRunning() && hold.getCurrentPlayTime() < TAP_MS;
                    hold.cancel();
                    x.setPressed(false);
                    if (tap && e.getActionMasked() == MotionEvent.ACTION_UP) x.performClick();
                    break;
                default:
            }
            return true;
        });
    }
}

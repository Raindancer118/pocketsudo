package de.raindancer118.pocketdeck;

import android.content.Context;
import android.graphics.Matrix;
import android.graphics.RectF;
import android.graphics.drawable.Drawable;
import android.util.AttributeSet;
import android.view.GestureDetector;
import android.view.MotionEvent;
import android.view.ScaleGestureDetector;

import androidx.annotation.NonNull;
import androidx.appcompat.widget.AppCompatImageView;

/**
 * Image view with pinch zoom, panning and double tap. While not zoomed, a
 * horizontal fling is reported so the caller can switch screens.
 */
public class ZoomImageView extends AppCompatImageView {
    public interface OnSwipe {
        void swiped(int direction); // +1 next, -1 previous
    }

    private static final float MAX_SCALE = 8f, DOUBLE_TAP_SCALE = 3f;
    private final Matrix base = new Matrix(), user = new Matrix(), draw = new Matrix();
    private final ScaleGestureDetector scaler;
    private final GestureDetector gestures;
    private OnSwipe onSwipe;

    public ZoomImageView(Context c) {
        this(c, null);
    }

    public ZoomImageView(Context c, AttributeSet a) {
        super(c, a);
        setScaleType(ScaleType.MATRIX);
        scaler = new ScaleGestureDetector(c, new ScaleGestureDetector.SimpleOnScaleGestureListener() {
            @Override
            public boolean onScale(@NonNull ScaleGestureDetector d) {
                float s = scale();
                float f = Math.max(1f / s, Math.min(d.getScaleFactor(), MAX_SCALE / s));
                user.postScale(f, f, d.getFocusX(), d.getFocusY());
                apply();
                return true;
            }
        });
        gestures = new GestureDetector(c, new GestureDetector.SimpleOnGestureListener() {
            @Override
            public boolean onScroll(MotionEvent e1, @NonNull MotionEvent e2, float dx, float dy) {
                if (scale() <= 1.01f) return false;
                user.postTranslate(-dx, -dy);
                apply();
                return true;
            }

            @Override
            public boolean onSingleTapConfirmed(@NonNull MotionEvent e) {
                return performClick();
            }

            @Override
            public boolean onDoubleTap(@NonNull MotionEvent e) {
                if (scale() > 1.01f) user.reset();
                else user.postScale(DOUBLE_TAP_SCALE, DOUBLE_TAP_SCALE, e.getX(), e.getY());
                apply();
                return true;
            }

            @Override
            public boolean onFling(MotionEvent e1, @NonNull MotionEvent e2, float vx, float vy) {
                if (scale() > 1.01f || onSwipe == null || Math.abs(vx) < Math.abs(vy) * 1.5f) return false;
                onSwipe.swiped(vx < 0 ? 1 : -1);
                return true;
            }
        });
    }

    public void setOnSwipe(OnSwipe s) {
        onSwipe = s;
    }

    @Override
    public void setImageDrawable(Drawable d) {
        super.setImageDrawable(d);
        user.reset();
        fit();
    }

    @Override
    protected void onSizeChanged(int w, int h, int ow, int oh) {
        super.onSizeChanged(w, h, ow, oh);
        fit();
    }

    private float scale() {
        float[] v = new float[9];
        user.getValues(v);
        return v[Matrix.MSCALE_X];
    }

    /** Base matrix: fit centre, like ScaleType.FIT_CENTER. */
    private void fit() {
        Drawable d = getDrawable();
        if (d == null || getWidth() == 0) return;
        base.setRectToRect(new RectF(0, 0, d.getIntrinsicWidth(), d.getIntrinsicHeight()),
                new RectF(0, 0, getWidth(), getHeight()), Matrix.ScaleToFit.CENTER);
        apply();
    }

    /** Keeps the zoomed image from being dragged out of the view. */
    private void apply() {
        Drawable d = getDrawable();
        if (d == null) return;
        draw.set(base);
        draw.postConcat(user);
        RectF r = new RectF(0, 0, d.getIntrinsicWidth(), d.getIntrinsicHeight());
        draw.mapRect(r);
        float dx = 0, dy = 0;
        if (r.width() <= getWidth()) dx = (getWidth() - r.width()) / 2 - r.left;
        else if (r.left > 0) dx = -r.left;
        else if (r.right < getWidth()) dx = getWidth() - r.right;
        if (r.height() <= getHeight()) dy = (getHeight() - r.height()) / 2 - r.top;
        else if (r.top > 0) dy = -r.top;
        else if (r.bottom < getHeight()) dy = getHeight() - r.bottom;
        user.postTranslate(dx, dy);
        draw.set(base);
        draw.postConcat(user);
        setImageMatrix(draw);
    }

    @Override
    public boolean onTouchEvent(MotionEvent e) {
        scaler.onTouchEvent(e);
        gestures.onTouchEvent(e);
        if (e.getPointerCount() > 1 || scale() > 1.01f) getParent().requestDisallowInterceptTouchEvent(true);
        return true;
    }

    @Override
    public boolean performClick() {
        return super.performClick();
    }
}

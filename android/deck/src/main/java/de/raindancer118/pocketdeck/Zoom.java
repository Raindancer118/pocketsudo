package de.raindancer118.pocketdeck;

/**
 * Pinch zoom of the stream picture. The picture view keeps its fitted layout
 * and is scaled around its top-left corner (pivot 0,0) plus a translation;
 * this class keeps that transform clamped and maps touches back to 0..1.
 */
final class Zoom {
    static final float MAX = 5f;

    private float cw, ch, left, top, w, h;
    private float s = 1, tx, ty;

    /** Container size and the picture's fitted bounds inside it. */
    void layout(float containerW, float containerH, float left, float top, float width, float height) {
        cw = containerW;
        ch = containerH;
        this.left = left;
        this.top = top;
        w = width;
        h = height;
        clamp();
    }

    void zoomBy(float factor, float fx, float fy) {
        if (w <= 0 || h <= 0) return;
        float ns = Math.max(1f, Math.min(MAX, s * factor));
        float u = (fx - left - tx) / s, v = (fy - top - ty) / s;
        tx = fx - left - u * ns;
        ty = fy - top - v * ns;
        s = ns;
        clamp();
    }

    void panBy(float dx, float dy) {
        tx += dx;
        ty += dy;
        clamp();
    }

    void reset() {
        s = 1;
        tx = 0;
        ty = 0;
    }

    float scale() {
        return s;
    }

    float tx() {
        return tx;
    }

    float ty() {
        return ty;
    }

    boolean zoomed() {
        return s > 1.01f;
    }

    /** Touch in container coordinates → {x, y} in 0..1 of the picture, or null outside it. */
    double[] toPicture(float x, float y) {
        if (w <= 0 || h <= 0) return null;
        double px = (x - left - tx) / (w * (double) s), py = (y - top - ty) / (h * (double) s);
        if (px < 0 || px > 1 || py < 0 || py > 1) return null;
        return new double[]{px, py};
    }

    private void clamp() {
        tx = clampAxis(tx, cw, left, w * s);
        ty = clampAxis(ty, ch, top, h * s);
    }

    // a picture smaller than the screen stays centred, a larger one may not leave a gap at either edge
    private static float clampAxis(float t, float container, float start, float size) {
        if (size <= container) return (container - size) / 2 - start;
        return Math.max(container - start - size, Math.min(-start, t));
    }
}

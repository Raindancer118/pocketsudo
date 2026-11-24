package de.raindancer118.pocketdeck;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertNull;

import org.junit.Test;

public class ZoomTest {
    // 1000x500 screen, picture 1000x400 centred: top 50
    private Zoom zoom() {
        Zoom z = new Zoom();
        z.layout(1000, 500, 0, 50, 1000, 400);
        return z;
    }

    @Test
    public void unzoomedMapsPictureBounds() {
        Zoom z = zoom();
        assertEquals(0.5, z.toPicture(500, 250)[0], 1e-9);
        assertEquals(0.5, z.toPicture(500, 250)[1], 1e-9);
        assertNull("black bar is outside the picture", z.toPicture(500, 20));
    }

    @Test
    public void pinchKeepsFocusPointUnderFingers() {
        Zoom z = zoom();
        double[] before = z.toPicture(200, 150);
        z.zoomBy(2, 200, 150);
        double[] after = z.toPicture(200, 150);
        assertEquals(before[0], after[0], 1e-9);
        assertEquals(before[1], after[1], 1e-9);
        assertEquals(2, z.scale(), 1e-9);
    }

    @Test
    public void panIsClampedToPicture() {
        Zoom z = zoom();
        z.zoomBy(2, 0, 50);
        z.panBy(5000, 5000);
        // left/top edge of the picture stays at the screen edge, no empty space
        assertEquals(0, z.toPicture(0, 0)[0], 1e-9);
        assertEquals(0, z.toPicture(0, 0)[1], 1e-9);
        z.panBy(-5000, -5000);
        assertEquals(1, z.toPicture(1000, 500)[0], 1e-9);
        assertEquals(1, z.toPicture(1000, 500)[1], 1e-9);
    }

    @Test
    public void scaleStaysBetweenOneAndMax() {
        Zoom z = zoom();
        z.zoomBy(0.2f, 500, 250);
        assertEquals(1, z.scale(), 1e-9);
        assertEquals(0.5, z.toPicture(500, 250)[0], 1e-9);
        z.zoomBy(100, 500, 250);
        assertEquals(Zoom.MAX, z.scale(), 1e-9);
    }

    @Test
    public void resetReturnsToFit() {
        Zoom z = zoom();
        z.zoomBy(3, 100, 100);
        z.reset();
        assertEquals(1, z.scale(), 1e-9);
        assertEquals(0, z.tx(), 1e-9);
        assertEquals(0, z.ty(), 1e-9);
    }
}

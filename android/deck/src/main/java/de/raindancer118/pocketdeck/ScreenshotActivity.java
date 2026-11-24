package de.raindancer118.pocketdeck;

import android.content.Intent;
import android.graphics.Bitmap;
import android.graphics.BitmapFactory;
import android.net.Uri;
import android.os.Bundle;
import android.view.View;
import android.widget.ImageView;
import android.widget.TextView;

import androidx.appcompat.app.AppCompatActivity;

/** Takes a screenshot on the computer and shows it; it is also saved to the gallery. */
public class ScreenshotActivity extends AppCompatActivity {
    private TextView status;
    private ImageView image;
    private Uri saved;

    @Override
    protected void onCreate(Bundle s) {
        super.onCreate(s);
        setContentView(R.layout.activity_screenshot);
        status = findViewById(R.id.status);
        image = findViewById(R.id.image);
        findViewById(R.id.back).setOnClickListener(v -> finish());
        View share = findViewById(R.id.share);
        share.setOnClickListener(v -> {
            if (saved != null) startActivity(Intent.createChooser(new Intent(Intent.ACTION_SEND)
                    .setType("image/jpeg").putExtra(Intent.EXTRA_STREAM, saved)
                    .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION), null));
        });
        take();
    }

    private void take() {
        Deck deck = Deck.get(this);
        deck.startPolling();
        status.setText(R.string.screenshot_taking);
        deck.send("screenshot", "{}", (ok, err, data) -> {
            if (!ok) {
                status.setText(err != null && err.contains("fingerprint") ? getString(R.string.needs_session) : err);
                return;
            }
            deck.io().execute(() -> {
                try {
                    byte[] jpeg = deck.download(Transfer.blobOf(data));
                    Bitmap b = BitmapFactory.decodeByteArray(jpeg, 0, jpeg.length);
                    saved = Transfer.saveScreenshot(this, jpeg, deck.pairing().hostname);
                    runOnUiThread(() -> {
                        image.setImageBitmap(b);
                        status.setText(R.string.screenshot_saved);
                        findViewById(R.id.share).setVisibility(View.VISIBLE);
                    });
                } catch (Exception e) {
                    runOnUiThread(() -> status.setText(e.getMessage()));
                }
            });
        });
    }
}

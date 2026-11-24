package de.raindancer118.pocketdeck;

import android.content.Intent;
import android.graphics.Bitmap;
import android.graphics.BitmapFactory;
import android.net.Uri;
import android.os.Bundle;
import android.view.Gravity;
import android.view.View;
import android.widget.LinearLayout;
import android.widget.TextView;

import androidx.appcompat.app.AppCompatActivity;

import org.json.JSONArray;
import org.json.JSONObject;

import java.util.ArrayList;
import java.util.List;

/** One screenshot per monitor, zoomable, swipe or tap a chip to switch; all saved to the gallery. */
public class ScreenshotActivity extends AppCompatActivity {
    private TextView status;
    private ZoomImageView image;
    private LinearLayout screens;
    private final List<Bitmap> shots = new ArrayList<>();
    private final List<Uri> saved = new ArrayList<>();
    private final List<TextView> chips = new ArrayList<>();
    private int current;

    @Override
    protected void onCreate(Bundle s) {
        super.onCreate(s);
        setContentView(R.layout.activity_screenshot);
        status = findViewById(R.id.status);
        image = findViewById(R.id.image);
        screens = findViewById(R.id.screens);
        findViewById(R.id.back).setOnClickListener(v -> finish());
        findViewById(R.id.share).setOnClickListener(v -> {
            if (current < saved.size()) startActivity(Intent.createChooser(new Intent(Intent.ACTION_SEND)
                    .setType("image/jpeg").putExtra(Intent.EXTRA_STREAM, saved.get(current))
                    .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION), null));
        });
        image.setOnSwipe(dir -> show(current + dir));
        take();
    }

    private void take() {
        Deck deck = Deck.get(this);
        deck.startPolling();
        status.setText(R.string.screenshot_taking);
        deck.send("screenshot", "{}", (ok, err, data) -> {
            if (!ok) {
                status.setText(Session.isSessionError(err) ? getString(R.string.needs_session) : err);
                return;
            }
            deck.io().execute(() -> {
                try {
                    JSONArray list = new JSONObject(data).getJSONArray("screens");
                    for (int i = 0; i < list.length(); i++) {
                        JSONObject sc = list.getJSONObject(i);
                        byte[] jpeg = deck.download(sc.getString("blob"));
                        Bitmap b = BitmapFactory.decodeByteArray(jpeg, 0, jpeg.length);
                        Uri uri = Transfer.saveScreenshot(this, jpeg, deck.pairing().hostname + "-" + sc.getString("name"));
                        String name = sc.getString("name");
                        runOnUiThread(() -> add(name, b, uri));
                    }
                    runOnUiThread(() -> status.setText(R.string.screenshot_saved));
                } catch (Exception e) {
                    runOnUiThread(() -> status.setText(e.getMessage()));
                }
            });
        });
    }

    private void add(String name, Bitmap b, Uri uri) {
        int index = shots.size();
        shots.add(b);
        saved.add(uri);
        TextView chip = new TextView(this, null, 0, R.style.Text);
        chip.setText(name.isEmpty() ? getString(R.string.screenshot) : name);
        chip.setTextSize(13);
        chip.setGravity(Gravity.CENTER);
        chip.setPadding(dp(14), 0, dp(14), 0);
        LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(-2, dp(34));
        lp.setMarginEnd(dp(8));
        chip.setOnClickListener(v -> show(index));
        chips.add(chip);
        screens.addView(chip, lp);
        if (index == 0) show(0);
        else style();
        findViewById(R.id.share).setVisibility(View.VISIBLE);
    }

    private void show(int i) {
        if (i < 0 || i >= shots.size()) return;
        current = i;
        image.setImageBitmap(shots.get(i));
        style();
    }

    private void style() {
        for (int i = 0; i < chips.size(); i++) {
            boolean on = i == current;
            chips.get(i).setBackgroundResource(on ? R.drawable.bg_key_on : R.drawable.bg_key);
            chips.get(i).setTextColor(getColor(on ? R.color.on_ink : R.color.text1));
        }
    }

    private int dp(int v) {
        return Math.round(v * getResources().getDisplayMetrics().density);
    }
}

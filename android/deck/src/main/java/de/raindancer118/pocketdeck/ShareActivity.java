package de.raindancer118.pocketdeck;

import android.content.Intent;
import android.net.Uri;
import android.os.Bundle;
import android.widget.Toast;

import androidx.appcompat.app.AppCompatActivity;

import org.json.JSONException;
import org.json.JSONObject;

/** "Share → PocketDeck": text goes to the computer's clipboard, files to its downloads. */
public class ShareActivity extends AppCompatActivity {
    @Override
    protected void onCreate(Bundle s) {
        super.onCreate(s);
        Deck deck = Deck.get(this);
        Intent in = getIntent();
        if (deck.pairing() == null || !Intent.ACTION_SEND.equals(in.getAction())) {
            done(getString(R.string.not_paired));
            return;
        }
        deck.startPolling();
        Uri stream = androidx.core.content.IntentCompat.getParcelableExtra(in, Intent.EXTRA_STREAM, Uri.class);
        String text = in.getStringExtra(Intent.EXTRA_TEXT);
        if (stream != null) {
            Transfer.sendFile(this, stream, (ok, err, data) -> done(ok ? getString(R.string.file_sent) : err));
        } else if (text != null && !text.isEmpty()) {
            try {
                deck.send("clipboard.set", new JSONObject().put("text", text).toString(),
                        (ok, err, data) -> done(ok ? getString(R.string.clip_sent) : err));
            } catch (JSONException e) {
                done(e.getMessage());
            }
        } else {
            done(null);
        }
    }

    private void done(String msg) {
        if (msg != null) Toast.makeText(this, msg, Toast.LENGTH_SHORT).show();
        Deck.get(this).stopPolling();
        finish();
    }
}

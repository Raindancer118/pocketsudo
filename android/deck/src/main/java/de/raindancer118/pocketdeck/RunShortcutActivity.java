package de.raindancer118.pocketdeck;

import android.net.Uri;
import android.os.Bundle;
import android.widget.Toast;

import androidx.fragment.app.FragmentActivity;

/** Entry for home-screen icons and NFC stickers: pocketdeck://run?id=… */
public class RunShortcutActivity extends FragmentActivity {
    @Override
    protected void onCreate(Bundle s) {
        super.onCreate(s);
        Uri data = getIntent().getData();
        String id = data == null ? null : data.getQueryParameter("id");
        Shortcuts.Shortcut sc = id == null ? null : new Shortcuts(this).get(id);
        if (sc == null || Deck.get(this).pairing() == null) {
            Toast.makeText(this, R.string.shortcut_missing, Toast.LENGTH_SHORT).show();
            finish();
            return;
        }
        ShortcutRunner.run(this, sc, (ok, err, d) -> {
            Toast.makeText(this, ok ? getString(R.string.shortcut_done, sc.name) : err, Toast.LENGTH_SHORT).show();
            Deck.get(this).stopPolling();
            finish();
        });
    }
}

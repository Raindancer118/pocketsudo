package de.raindancer118.pocketdeck;

import androidx.fragment.app.FragmentActivity;

import org.json.JSONException;

import de.raindancer118.pocketcore.DeckCrypto;

/** Runs a shortcut on the computer; asks for the finger if a step needs it. */
final class ShortcutRunner {
    private ShortcutRunner() {}

    static void run(FragmentActivity a, Shortcuts.Shortcut sc, Deck.Callback cb) {
        Deck deck = Deck.get(a);
        deck.startPolling();
        String chain;
        try {
            chain = sc.chainJson();
        } catch (JSONException e) {
            cb.done(false, e.getMessage(), null);
            return;
        }
        DeckCrypto.Message m = DeckCrypto.newMessage("run", chain);
        if (!sc.needsFinger()) {
            deck.send(m, cb);
            return;
        }
        BioSign.sign(a, m, a.getString(R.string.bio_run, sc.name), new BioSign.Result() {
            @Override
            public void signed(DeckCrypto.Message signed) {
                deck.send(signed, cb);
            }

            @Override
            public void failed(String why) {
                cb.done(false, why, null);
            }
        });
    }
}

package de.raindancer118.pocketdeck;

import androidx.fragment.app.FragmentActivity;

import org.json.JSONException;
import org.json.JSONObject;

import de.raindancer118.pocketcore.DeckCrypto;

/** Opens the 15-minute remote-control session with one fingerprint. */
final class Session {
    interface Done {
        void done(boolean ok, String error);
    }

    private Session() {}

    static boolean active(Deck deck) {
        return deck.sessionUntil > System.currentTimeMillis();
    }

    static boolean isSessionError(String err) {
        return err != null && err.contains("remote control");
    }

    static void open(FragmentActivity a, Done done) {
        Deck deck = Deck.get(a);
        DeckStore.Pairing p = deck.pairing();
        if (p == null) return;
        BioSign.sign(a, DeckCrypto.newMessage("session.open", "{}"), a.getString(R.string.bio_session, p.hostname),
                new BioSign.Result() {
                    @Override
                    public void signed(DeckCrypto.Message m) {
                        deck.send(m, (ok, err, data) -> {
                            if (ok) {
                                try {
                                    deck.sessionUntil = new JSONObject(data).getLong("until") * 1000;
                                } catch (JSONException e) {
                                    deck.sessionUntil = System.currentTimeMillis() + 15 * 60_000;
                                }
                            }
                            done.done(ok, err);
                        });
                    }

                    @Override
                    public void failed(String why) {
                        done.done(false, why);
                    }
                });
    }
}

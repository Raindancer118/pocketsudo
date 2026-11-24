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

    /**
     * On app start: reuse a session the computer still has, otherwise ask for
     * the finger once. Cancelling is fine – everyday controls work without it.
     */
    static void ensure(FragmentActivity a) {
        Deck deck = Deck.get(a);
        if (active(deck) || deck.askedSession || deck.pairing() == null) return;
        deck.askedSession = true;
        deck.send("session.status", "{}", (ok, err, data) -> {
            if (!ok) return; // computer offline: nothing to unlock
            try {
                long until = new JSONObject(data).getLong("until") * 1000;
                if (until > System.currentTimeMillis()) {
                    deck.sessionUntil = until;
                    return;
                }
            } catch (JSONException ignored) {
            }
            if (!a.isFinishing()) open(a, (o, e) -> {
            });
        });
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

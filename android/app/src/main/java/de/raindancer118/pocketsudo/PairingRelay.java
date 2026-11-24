package de.raindancer118.pocketsudo;

import de.raindancer118.pocketcore.Relay;

final class PairingRelay {
    private PairingRelay() {}

    static Relay of(Store.Pairing p) {
        return new Relay(p.relay, p.phoneToken);
    }
}

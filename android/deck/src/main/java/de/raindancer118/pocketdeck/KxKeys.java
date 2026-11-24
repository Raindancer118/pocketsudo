package de.raindancer118.pocketdeck;

import android.security.keystore.KeyGenParameterSpec;
import android.security.keystore.KeyProperties;

import java.security.GeneralSecurityException;
import java.security.KeyPairGenerator;
import java.security.KeyStore;
import java.security.PrivateKey;
import java.security.spec.ECGenParameterSpec;
import java.util.Base64;

import de.raindancer118.pocketcore.DeckCrypto;

/**
 * Key-agreement key in the Android Keystore. No biometric here on purpose:
 * everyday actions (volume, media) must not ask for a finger; unlock and
 * shell commands carry an extra signature from the biometric key instead.
 */
final class KxKeys {
    private KxKeys() {}

    static String create(String alias) throws GeneralSecurityException {
        KeyPairGenerator g = KeyPairGenerator.getInstance(KeyProperties.KEY_ALGORITHM_EC, "AndroidKeyStore");
        g.initialize(new KeyGenParameterSpec.Builder(alias, KeyProperties.PURPOSE_AGREE_KEY)
                .setAlgorithmParameterSpec(new ECGenParameterSpec("secp256r1")).build());
        return Base64.getEncoder().encodeToString(g.generateKeyPair().getPublic().getEncoded());
    }

    static byte[] deriveKey(DeckStore.Pairing p) throws GeneralSecurityException {
        try {
            KeyStore ks = KeyStore.getInstance("AndroidKeyStore");
            ks.load(null);
            PrivateKey k = (PrivateKey) ks.getKey(p.kxAlias, null);
            if (k == null) throw new GeneralSecurityException("key-agreement key missing – pair again");
            return DeckCrypto.deriveKey(k, p.hostKx, p.channel, "AndroidKeyStore");
        } catch (java.io.IOException e) {
            throw new GeneralSecurityException(e);
        }
    }

    static void delete(String alias) {
        try {
            KeyStore ks = KeyStore.getInstance("AndroidKeyStore");
            ks.load(null);
            ks.deleteEntry(alias);
        } catch (Exception ignored) {
        }
    }
}

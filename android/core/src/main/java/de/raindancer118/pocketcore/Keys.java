package de.raindancer118.pocketcore;

import android.security.keystore.KeyGenParameterSpec;
import android.security.keystore.KeyPermanentlyInvalidatedException;
import android.security.keystore.KeyProperties;

import java.security.GeneralSecurityException;
import java.security.KeyPairGenerator;
import java.security.KeyStore;
import java.security.PrivateKey;
import java.security.Signature;
import java.security.spec.ECGenParameterSpec;
import java.util.Base64;

/**
 * One EC P-256 key per pairing, inside the Android Keystore. Every single use
 * needs a strong biometric, and enrolling a new finger destroys the key, so a
 * finger added by someone else can never approve anything.
 */
public final class Keys {
    private static final String STORE = "AndroidKeyStore";

    private Keys() {}

    /** Creates the key and returns the base64 SPKI public key. */
    public static String create(String alias) throws GeneralSecurityException {
        try {
            return generate(alias, true);
        } catch (GeneralSecurityException | RuntimeException e) {
            // StrongBoxUnavailableException, or a plain ProviderException on some devices
            delete(alias);
            return generate(alias, false);
        }
    }

    private static String generate(String alias, boolean strongBox) throws GeneralSecurityException {
        KeyGenParameterSpec.Builder b = new KeyGenParameterSpec.Builder(alias, KeyProperties.PURPOSE_SIGN)
                .setAlgorithmParameterSpec(new ECGenParameterSpec("secp256r1"))
                .setDigests(KeyProperties.DIGEST_SHA256)
                .setUserAuthenticationRequired(true)
                .setUserAuthenticationParameters(0, KeyProperties.AUTH_BIOMETRIC_STRONG)
                .setInvalidatedByBiometricEnrollment(true);
        if (strongBox) b.setIsStrongBoxBacked(true);
        KeyPairGenerator g = KeyPairGenerator.getInstance(KeyProperties.KEY_ALGORITHM_EC, STORE);
        g.initialize(b.build());
        return Base64.getEncoder().encodeToString(g.generateKeyPair().getPublic().getEncoded());
    }

    /**
     * A Signature ready to be unlocked by BiometricPrompt. Throws
     * KeyPermanentlyInvalidatedException after fingerprints changed.
     */
    public static Signature signer(String alias) throws GeneralSecurityException {
        try {
            KeyStore ks = KeyStore.getInstance(STORE);
            ks.load(null);
            PrivateKey k = (PrivateKey) ks.getKey(alias, null);
            if (k == null) throw new KeyPermanentlyInvalidatedException("key missing");
            Signature s = Signature.getInstance("SHA256withECDSA");
            s.initSign(k);
            return s;
        } catch (java.io.IOException e) {
            throw new GeneralSecurityException(e);
        }
    }

    public static void delete(String alias) {
        try {
            KeyStore ks = KeyStore.getInstance(STORE);
            ks.load(null);
            ks.deleteEntry(alias);
        } catch (Exception ignored) {
        }
    }
}

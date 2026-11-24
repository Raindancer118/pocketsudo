package de.raindancer118.pocketdeck;

import android.security.keystore.KeyPermanentlyInvalidatedException;

import androidx.annotation.NonNull;
import androidx.biometric.BiometricManager;
import androidx.biometric.BiometricPrompt;
import androidx.core.content.ContextCompat;
import androidx.fragment.app.FragmentActivity;

import java.security.GeneralSecurityException;
import java.security.Signature;
import java.util.Base64;

import de.raindancer118.pocketcore.DeckCrypto;
import de.raindancer118.pocketcore.Keys;

/** Signs a deck message with the biometric key (unlock, remote session, commands). */
final class BioSign {
    interface Result {
        void signed(DeckCrypto.Message m);

        default void failed(String why) {
        }
    }

    private BioSign() {}

    static void sign(FragmentActivity a, DeckCrypto.Message m, String title, Result res) {
        DeckStore.Pairing p = Deck.get(a).pairing();
        if (p == null) return;
        if (BiometricManager.from(a).canAuthenticate(BiometricManager.Authenticators.BIOMETRIC_STRONG)
                != BiometricManager.BIOMETRIC_SUCCESS) {
            res.failed(a.getString(R.string.no_biometric));
            return;
        }
        Signature sig;
        try {
            sig = Keys.signer(p.signAlias);
        } catch (KeyPermanentlyInvalidatedException e) {
            res.failed(a.getString(R.string.key_invalid));
            return;
        } catch (GeneralSecurityException e) {
            res.failed(e.getMessage());
            return;
        }
        BiometricPrompt.PromptInfo info = new BiometricPrompt.PromptInfo.Builder()
                .setTitle(title)
                .setNegativeButtonText(a.getString(R.string.cancel))
                .setAllowedAuthenticators(BiometricManager.Authenticators.BIOMETRIC_STRONG)
                .setConfirmationRequired(false)
                .build();
        new BiometricPrompt(a, ContextCompat.getMainExecutor(a), new BiometricPrompt.AuthenticationCallback() {
            @Override
            public void onAuthenticationSucceeded(@NonNull BiometricPrompt.AuthenticationResult r) {
                BiometricPrompt.CryptoObject co = r.getCryptoObject();
                if (co == null || co.getSignature() == null) return;
                try {
                    Signature s = co.getSignature();
                    s.update(DeckCrypto.authMessage(m));
                    m.sig = Base64.getEncoder().encodeToString(s.sign());
                    res.signed(m);
                } catch (GeneralSecurityException e) {
                    res.failed(e.getMessage());
                }
            }
        }).authenticate(info, new BiometricPrompt.CryptoObject(sig));
    }
}

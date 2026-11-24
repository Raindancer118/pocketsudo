package de.raindancer118.pocketcore;

import static org.junit.Assert.assertArrayEquals;
import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertNotEquals;
import static org.junit.Assert.assertTrue;
import static org.junit.Assert.fail;

import org.json.JSONObject;
import org.junit.Before;
import org.junit.Test;

import java.io.InputStream;
import java.nio.charset.StandardCharsets;
import java.security.KeyFactory;
import java.security.PrivateKey;
import java.security.Signature;
import java.security.spec.PKCS8EncodedKeySpec;
import java.util.Base64;

/** Java side of PocketDeck crypto, checked against deck_vectors.json from Go. */
public class DeckCryptoTest {
    private JSONObject v;

    @Before
    public void load() throws Exception {
        try (InputStream in = getClass().getClassLoader().getResourceAsStream("deck_vectors.json")) {
            v = new JSONObject(new String(in.readAllBytes(), StandardCharsets.UTF_8));
        }
    }

    private byte[] key() throws Exception {
        PrivateKey phone = KeyFactory.getInstance("EC").generatePrivate(
                new PKCS8EncodedKeySpec(Base64.getDecoder().decode(v.getString("phone_kx_pkcs8"))));
        return DeckCrypto.deriveKey(phone, v.getString("host_kx_pub"), v.getString("channel"), null);
    }

    @Test
    public void derivesTheSameKeyAsGo() throws Exception {
        assertArrayEquals(Base64.getDecoder().decode(v.getString("key_b64")), key());
    }

    @Test
    public void opensBoxesSealedByGo() throws Exception {
        DeckCrypto.Message m = DeckCrypto.openMessage(key(), v.getString("box"), 1_800_000_000L * 1000, null);
        assertEquals("volume.set", m.type);
        assertEquals("abc123", m.id);
        assertEquals(40, new JSONObject(m.body).getInt("percent"));
    }

    @Test
    public void sealOpenRoundTripAndTampering() throws Exception {
        byte[] k = key();
        long now = System.currentTimeMillis();
        String box = DeckCrypto.sealMessage(k, DeckCrypto.newMessage("state", "{}"), now);
        assertEquals("state", DeckCrypto.openMessage(k, box, now, null).type);
        assertNotEquals(box, DeckCrypto.sealMessage(k, DeckCrypto.newMessage("state", "{}"), now));
        byte[] raw = Base64.getDecoder().decode(box);
        raw[raw.length - 1] ^= 1;
        try {
            DeckCrypto.openMessage(k, Base64.getEncoder().encodeToString(raw), now, null);
            fail("tampered box opened");
        } catch (java.security.GeneralSecurityException expected) {
        }
    }

    @Test
    public void rejectsStaleAndReplayedMessages() throws Exception {
        byte[] k = key();
        long now = System.currentTimeMillis();
        String box = DeckCrypto.sealMessage(k, DeckCrypto.newMessage("state", "{}"), now - 10 * 60_000);
        try {
            DeckCrypto.openMessage(k, box, now, null);
            fail("stale message accepted");
        } catch (java.security.GeneralSecurityException expected) {
        }
        java.util.Set<String> seen = new java.util.HashSet<>();
        String fresh = DeckCrypto.sealMessage(k, DeckCrypto.newMessage("state", "{}"), now);
        DeckCrypto.openMessage(k, fresh, now, seen);
        try {
            DeckCrypto.openMessage(k, fresh, now, seen);
            fail("replay accepted");
        } catch (java.security.GeneralSecurityException expected) {
        }
    }

    @Test
    public void authMessageMatchesGoAndVerifies() throws Exception {
        DeckCrypto.Message m = new DeckCrypto.Message("abc123", "volume.set", 1_800_000_000L, "{\"percent\":40}");
        assertArrayEquals(v.getString("auth_msg").getBytes(StandardCharsets.UTF_8), DeckCrypto.authMessage(m));
        Signature s = Signature.getInstance("SHA256withECDSA");
        s.initVerify(Protocol.decodePublicKey(v.getString("auth_pub")));
        s.update(DeckCrypto.authMessage(m));
        assertTrue(s.verify(Base64.getDecoder().decode(v.getString("auth_sig"))));
    }

    @Test
    public void pairMacV2CoversKx() throws Exception {
        byte[] secret = "0123456789abcdef0123456789abcdef".getBytes(StandardCharsets.UTF_8);
        String a = DeckCrypto.pairMacV2(secret, "c", "PUB", "KX", "dev");
        assertNotEquals(a, DeckCrypto.pairMacV2(secret, "c", "PUB", "EVIL", "dev"));
    }
}

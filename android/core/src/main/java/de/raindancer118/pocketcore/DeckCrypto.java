package de.raindancer118.pocketcore;

import org.json.JSONException;
import org.json.JSONObject;

import java.nio.charset.StandardCharsets;
import java.security.GeneralSecurityException;
import java.security.MessageDigest;
import java.security.PrivateKey;
import java.security.SecureRandom;
import java.util.Base64;
import java.util.Set;

import javax.crypto.Cipher;
import javax.crypto.KeyAgreement;
import javax.crypto.Mac;
import javax.crypto.spec.GCMParameterSpec;
import javax.crypto.spec.SecretKeySpec;

/** Mirror of protocol/deck.go. Keep in sync (deck_vectors.json). */
public final class DeckCrypto {
    private static final String INFO = "pocketdeck-v1";
    private static final String AUTH_PREFIX = "pocketdeck-v1-auth\n";
    private static final String PAIR_V2_PREFIX = "pocketsudo-v2-pair\n";
    public static final long MAX_SKEW_MS = 5 * 60_000;
    private static final SecureRandom RNG = new SecureRandom();

    private DeckCrypto() {}

    public static final class Message {
        public final String id, type, body;
        public final long ts;
        public String ref, sig;

        public Message(String id, String type, long ts, String body) {
            this.id = id;
            this.type = type;
            this.ts = ts;
            this.body = body == null ? "{}" : body;
        }
    }

    public static Message newMessage(String type, String body) {
        byte[] b = new byte[16];
        RNG.nextBytes(b);
        return new Message(Protocol.hex(b), type, System.currentTimeMillis() / 1000, body);
    }

    /**
     * ECDH with our key (software or AndroidKeyStore), then
     * HKDF-SHA256(salt = channel, info = pocketdeck-v1). provider may be null.
     */
    public static byte[] deriveKey(PrivateKey own, String peerPubB64, String channel, String provider)
            throws GeneralSecurityException {
        KeyAgreement ka = provider == null ? KeyAgreement.getInstance("ECDH") : KeyAgreement.getInstance("ECDH", provider);
        ka.init(own);
        ka.doPhase(Protocol.decodePublicKey(peerPubB64), true);
        return hkdf(ka.generateSecret(), channel.getBytes(StandardCharsets.UTF_8), INFO.getBytes(StandardCharsets.UTF_8), 32);
    }

    static byte[] hkdf(byte[] ikm, byte[] salt, byte[] info, int len) throws GeneralSecurityException {
        Mac mac = Mac.getInstance("HmacSHA256");
        mac.init(new SecretKeySpec(salt, "HmacSHA256"));
        byte[] prk = mac.doFinal(ikm);
        mac.init(new SecretKeySpec(prk, "HmacSHA256"));
        byte[] out = new byte[len];
        byte[] t = new byte[0];
        int pos = 0;
        for (byte i = 1; pos < len; i++) {
            mac.update(t);
            mac.update(info);
            mac.update(i);
            t = mac.doFinal();
            int n = Math.min(t.length, len - pos);
            System.arraycopy(t, 0, out, pos, n);
            pos += n;
        }
        return out;
    }

    public static byte[] sealBytes(byte[] key, byte[] plain) throws GeneralSecurityException {
        byte[] nonce = new byte[12];
        RNG.nextBytes(nonce);
        Cipher c = Cipher.getInstance("AES/GCM/NoPadding");
        c.init(Cipher.ENCRYPT_MODE, new SecretKeySpec(key, "AES"), new GCMParameterSpec(128, nonce));
        byte[] ct = c.doFinal(plain);
        byte[] out = new byte[nonce.length + ct.length];
        System.arraycopy(nonce, 0, out, 0, nonce.length);
        System.arraycopy(ct, 0, out, nonce.length, ct.length);
        return out;
    }

    public static byte[] openBytes(byte[] key, byte[] box) throws GeneralSecurityException {
        if (box.length < 12 + 16) throw new GeneralSecurityException("box too short");
        Cipher c = Cipher.getInstance("AES/GCM/NoPadding");
        c.init(Cipher.DECRYPT_MODE, new SecretKeySpec(key, "AES"), new GCMParameterSpec(128, box, 0, 12));
        return c.doFinal(box, 12, box.length - 12);
    }

    /**
     * Builds the JSON by hand so the body bytes are exactly what we hashed for
     * the signature. Unsigned messages get stamped with nowMs; signed ones keep
     * the ts they were signed with.
     */
    public static String sealMessage(byte[] key, Message m, long nowMs) throws GeneralSecurityException {
        long ts = m.sig != null ? m.ts : nowMs / 1000;
        StringBuilder sb = new StringBuilder("{\"id\":").append(JSONObject.quote(m.id))
                .append(",\"type\":").append(JSONObject.quote(m.type))
                .append(",\"ts\":").append(ts)
                .append(",\"body\":").append(m.body);
        if (m.ref != null) sb.append(",\"ref\":").append(JSONObject.quote(m.ref));
        if (m.sig != null) sb.append(",\"sig\":").append(JSONObject.quote(m.sig));
        sb.append('}');
        return Base64.getEncoder().encodeToString(sealBytes(key, sb.toString().getBytes(StandardCharsets.UTF_8)));
    }

    public static Message openMessage(byte[] key, String box, long nowMs, Set<String> seen) throws GeneralSecurityException {
        byte[] plain = openBytes(key, Base64.getDecoder().decode(box));
        try {
            JSONObject o = new JSONObject(new String(plain, StandardCharsets.UTF_8));
            Object body = o.opt("body");
            Message m = new Message(o.getString("id"), o.getString("type"), o.getLong("ts"),
                    body == null ? "{}" : body.toString());
            m.ref = o.optString("ref", null);
            m.sig = o.optString("sig", null);
            if (Math.abs(m.ts * 1000 - nowMs) > MAX_SKEW_MS) throw new GeneralSecurityException("timestamp outside the window");
            if (seen != null && !seen.add(m.id)) throw new GeneralSecurityException("replayed message");
            return m;
        } catch (JSONException e) {
            throw new GeneralSecurityException("malformed message", e);
        }
    }

    public static byte[] authMessage(Message m) throws GeneralSecurityException {
        byte[] sum = MessageDigest.getInstance("SHA-256").digest(m.body.getBytes(StandardCharsets.UTF_8));
        return (AUTH_PREFIX + m.id + "\n" + m.type + "\n" + m.ts + "\n" + Protocol.hex(sum)).getBytes(StandardCharsets.UTF_8);
    }

    public static String pairMacV2(byte[] secret, String channel, String phonePub, String phoneKx, String device)
            throws GeneralSecurityException {
        Mac m = Mac.getInstance("HmacSHA256");
        m.init(new SecretKeySpec(secret, "HmacSHA256"));
        byte[] out = m.doFinal((PAIR_V2_PREFIX + channel + "\n" + phonePub + "\n" + phoneKx + "\n" + device)
                .getBytes(StandardCharsets.UTF_8));
        return Base64.getEncoder().encodeToString(out);
    }
}

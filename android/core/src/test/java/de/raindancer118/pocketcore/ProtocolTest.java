package de.raindancer118.pocketcore;

import static org.junit.Assert.assertArrayEquals;
import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;

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

/** Checks the Java side against signatures produced by the Go code (vectors.json). */
public class ProtocolTest {
    private JSONObject v;

    @Before
    public void load() throws Exception {
        try (InputStream in = getClass().getClassLoader().getResourceAsStream("vectors.json")) {
            v = new JSONObject(new String(in.readAllBytes(), StandardCharsets.UTF_8));
        }
    }

    @Test
    public void hostSignatureVerifies() throws Exception {
        Protocol.Request r = Protocol.verifyRequest(v.getString("host_pub"), v.getString("payload"), v.getString("sig"));
        assertEquals("pacman -Syu", r.command);
        assertEquals("tom", r.user);
        assertEquals("42", r.code);
    }

    @Test(expected = SecurityException.class)
    public void tamperedPayloadIsRejected() throws Exception {
        String payload = new String(Base64.getDecoder().decode(v.getString("payload")), StandardCharsets.UTF_8)
                .replace("pacman -Syu", "rm -rf /home");
        Protocol.verifyRequest(v.getString("host_pub"),
                Base64.getEncoder().encodeToString(payload.getBytes(StandardCharsets.UTF_8)), v.getString("sig"));
    }

    @Test
    public void responseMessageMatchesGo() throws Exception {
        byte[] msg = Protocol.responseMessage("approve", v.getString("payload"));
        assertArrayEquals(v.getString("approve_msg").getBytes(StandardCharsets.UTF_8), msg);
    }

    @Test
    public void javaSignatureIsWhatGoExpects() throws Exception {
        PrivateKey phone = KeyFactory.getInstance("EC").generatePrivate(
                new PKCS8EncodedKeySpec(Base64.getDecoder().decode(v.getString("phone_priv_pkcs8"))));
        Signature s = Signature.getInstance("SHA256withECDSA");
        s.initSign(phone);
        s.update(Protocol.responseMessage("approve", v.getString("payload")));
        String sig = Base64.getEncoder().encodeToString(s.sign());
        // ECDSA is randomized, so check it the way Go does: verify with the public key.
        Signature ver = Signature.getInstance("SHA256withECDSA");
        ver.initVerify(Protocol.decodePublicKey(v.getString("phone_pub")));
        ver.update(v.getString("approve_msg").getBytes(StandardCharsets.UTF_8));
        assertTrue(ver.verify(Base64.getDecoder().decode(sig)));
    }

    @Test
    public void pairingValuesMatchGo() throws Exception {
        byte[] secret = Base64.getUrlDecoder().decode(v.getString("pair_secret"));
        assertEquals(v.getString("pair_mac"), Protocol.pairMac(secret, v.getString("pair_channel"),
                v.getString("phone_pub"), v.getString("pair_device")));
        assertEquals(v.getString("verification_code"),
                Protocol.verificationCode(v.getString("host_pub"), v.getString("phone_pub")));
        assertEquals(v.getString("host_fp"), Protocol.fingerprint(v.getString("host_pub")));
    }

    @Test
    public void pairUriIsParsed() {
        Protocol.PairUri u = Protocol.PairUri.parse(
                "pocketsudo://pair?c=abc&f=fp&n=eclipse&r=https%3A%2F%2Frelay.example&s=c2VjcmV0");
        assertEquals("https://relay.example", u.relay);
        assertEquals("abc", u.channel);
        assertEquals("eclipse", u.hostname);
        assertArrayEquals("secret".getBytes(StandardCharsets.UTF_8), u.secret);
        assertFalse(Protocol.PairUri.isPairUri("https://example.com"));
    }

    @Test
    public void promptContextShortensHome() {
        assertEquals("tom@eclipse:~/Projekte", Protocol.promptContext("tom", "eclipse", "/home/tom/Projekte"));
        assertEquals("tom@eclipse:~", Protocol.promptContext("tom", "eclipse", "/home/tom"));
        assertEquals("tom@eclipse:/etc", Protocol.promptContext("tom", "eclipse", "/etc"));
        assertEquals("tom@eclipse", Protocol.promptContext("tom", "eclipse", ""));
    }

    @Test
    public void groupCodeSplitsSixDigits() {
        assertEquals("482 913", Protocol.groupCode("482913"));
    }
}

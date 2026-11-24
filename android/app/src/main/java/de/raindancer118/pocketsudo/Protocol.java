package de.raindancer118.pocketsudo;

import org.json.JSONException;
import org.json.JSONObject;

import java.nio.ByteBuffer;
import java.nio.charset.StandardCharsets;
import java.security.GeneralSecurityException;
import java.security.KeyFactory;
import java.security.MessageDigest;
import java.security.PublicKey;
import java.security.Signature;
import java.security.spec.X509EncodedKeySpec;
import java.util.Base64;
import java.util.HashMap;
import java.util.Locale;
import java.util.Map;

import javax.crypto.Mac;
import javax.crypto.spec.SecretKeySpec;

/** Mirror of internal/protocol on the Go side. Keep both in sync (vectors.json). */
public final class Protocol {
    static final String REQUEST_PREFIX = "pocketsudo-v1-request\n";
    static final String RESPONSE_PREFIX = "pocketsudo-v1-response\n";
    static final String PAIR_PREFIX = "pocketsudo-v1-pair\n";
    public static final String APPROVE = "approve";
    public static final String DENY = "deny";

    private Protocol() {}

    public static final class Request {
        public String id, channel, hostname, user, service, tty, rhost, command, cwd, code;
        public boolean remote;
        public long created, expires;
        /** Base64 payload exactly as signed; needed to build the response. */
        public String payload;

        static Request fromJson(JSONObject o, String payload) {
            Request r = new Request();
            r.id = o.optString("id");
            r.channel = o.optString("channel");
            r.hostname = o.optString("hostname");
            r.user = o.optString("user");
            r.service = o.optString("service");
            r.tty = o.optString("tty");
            r.rhost = o.optString("rhost");
            r.remote = o.optBoolean("remote");
            r.command = o.optString("command");
            r.cwd = o.optString("cwd");
            r.code = o.optString("code");
            r.created = o.optLong("created");
            r.expires = o.optLong("expires");
            r.payload = payload;
            return r;
        }

        public long secondsLeft() {
            return expires - System.currentTimeMillis() / 1000;
        }
    }

    public static PublicKey decodePublicKey(String b64) throws GeneralSecurityException {
        return KeyFactory.getInstance("EC").generatePublic(new X509EncodedKeySpec(Base64.getDecoder().decode(b64)));
    }

    /** Verifies the host signature; throws SecurityException if it does not match. */
    public static Request verifyRequest(String hostPub, String payloadB64, String sigB64) throws GeneralSecurityException, JSONException {
        byte[] payload = Base64.getDecoder().decode(payloadB64);
        Signature s = Signature.getInstance("SHA256withECDSA");
        s.initVerify(decodePublicKey(hostPub));
        s.update(REQUEST_PREFIX.getBytes(StandardCharsets.UTF_8));
        s.update(payload);
        if (!s.verify(Base64.getDecoder().decode(sigB64))) {
            throw new SecurityException("bad host signature");
        }
        return Request.fromJson(new JSONObject(new String(payload, StandardCharsets.UTF_8)), payloadB64);
    }

    public static byte[] responseMessage(String decision, String payloadB64) throws GeneralSecurityException {
        byte[] payload = Base64.getDecoder().decode(payloadB64);
        String hash = hex(MessageDigest.getInstance("SHA-256").digest(payload));
        return (RESPONSE_PREFIX + decision + "\n" + hash).getBytes(StandardCharsets.UTF_8);
    }

    public static String pairMac(byte[] secret, String channel, String phonePub, String device) throws GeneralSecurityException {
        Mac m = Mac.getInstance("HmacSHA256");
        m.init(new SecretKeySpec(secret, "HmacSHA256"));
        byte[] out = m.doFinal((PAIR_PREFIX + channel + "\n" + phonePub + "\n" + device).getBytes(StandardCharsets.UTF_8));
        return Base64.getEncoder().encodeToString(out);
    }

    public static String verificationCode(String hostPub, String phonePub) throws GeneralSecurityException {
        byte[] sum = MessageDigest.getInstance("SHA-256").digest((hostPub + "\n" + phonePub).getBytes(StandardCharsets.UTF_8));
        long n = ByteBuffer.wrap(sum, 0, 4).getInt() & 0xffffffffL;
        return String.format(Locale.ROOT, "%06d", n % 1_000_000);
    }

    public static String fingerprint(String pubB64) throws GeneralSecurityException {
        byte[] sum = MessageDigest.getInstance("SHA-256").digest(Base64.getDecoder().decode(pubB64));
        return Base64.getUrlEncoder().withoutPadding().encodeToString(sum);
    }

    public static String groupCode(String code) {
        return code.length() == 6 ? code.substring(0, 3) + " " + code.substring(3) : code;
    }

    /** "tom@eclipse:~/Projekte" like a shell prompt. */
    public static String promptContext(String user, String host, String cwd) {
        String base = user + "@" + host;
        if (cwd == null || cwd.isEmpty()) return base;
        String home = "/home/" + user;
        if (cwd.equals(home)) cwd = "~";
        else if (cwd.startsWith(home + "/")) cwd = "~" + cwd.substring(home.length());
        return base + ":" + cwd;
    }

    static String hex(byte[] b) {
        StringBuilder sb = new StringBuilder(b.length * 2);
        for (byte x : b) sb.append(String.format(Locale.ROOT, "%02x", x));
        return sb.toString();
    }

    /** pocketsudo://pair?r=relay&c=channel&s=secret&f=hostfp&n=hostname */
    public static final class PairUri {
        public String relay, channel, fingerprint, hostname;
        public byte[] secret;

        public static boolean isPairUri(String s) {
            return s != null && s.startsWith("pocketsudo://pair?");
        }

        public static PairUri parse(String s) {
            if (!isPairUri(s)) throw new IllegalArgumentException("not a PocketSudo code");
            Map<String, String> q = query(s.substring(s.indexOf('?') + 1));
            PairUri u = new PairUri();
            u.relay = q.get("r");
            u.channel = q.get("c");
            u.fingerprint = q.get("f");
            u.hostname = q.getOrDefault("n", "");
            String sec = q.get("s");
            if (u.relay == null || u.channel == null || u.fingerprint == null || sec == null) {
                throw new IllegalArgumentException("incomplete PocketSudo code");
            }
            u.secret = Base64.getUrlDecoder().decode(sec);
            return u;
        }

        // Plain parser so the unit tests do not depend on android.net.Uri.
        private static Map<String, String> query(String q) {
            Map<String, String> m = new HashMap<>();
            for (String part : q.split("&")) {
                int i = part.indexOf('=');
                if (i <= 0) continue;
                m.put(decode(part.substring(0, i)), decode(part.substring(i + 1)));
            }
            return m;
        }

        private static String decode(String s) {
            try {
                return java.net.URLDecoder.decode(s, "UTF-8");
            } catch (java.io.UnsupportedEncodingException e) {
                throw new IllegalStateException(e);
            }
        }
    }
}

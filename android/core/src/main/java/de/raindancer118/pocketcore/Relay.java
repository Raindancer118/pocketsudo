package de.raindancer118.pocketcore;

import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.charset.StandardCharsets;

/** Blocking JSON client for the relay; call it off the main thread. */
public final class Relay {
    public static final class HttpException extends IOException {
        public final int code;

        HttpException(int code, String msg) {
            super("relay " + code + ": " + msg);
            this.code = code;
        }
    }

    /** Set once by the app, e.g. "pocketsudo-android/0.2.0". */
    public static volatile String userAgent = "pocket-android";

    private final String base;
    private final String token;

    public Relay(String base, String token) {
        this.base = base.endsWith("/") ? base.substring(0, base.length() - 1) : base;
        this.token = token;
    }

    /** Returns the parsed body, JSONObject.NULL for 204. */
    public Object call(String method, String path, JSONObject body, int readTimeoutMs) throws IOException {
        HttpURLConnection c = (HttpURLConnection) new URL(base + path).openConnection();
        try {
            c.setRequestMethod(method);
            c.setConnectTimeout(8000);
            c.setReadTimeout(readTimeoutMs);
            c.setRequestProperty("Accept", "application/json");
            c.setRequestProperty("User-Agent", userAgent);
            if (token != null && !token.isEmpty()) c.setRequestProperty("Authorization", "Bearer " + token);
            if (body != null) {
                c.setDoOutput(true);
                c.setRequestProperty("Content-Type", "application/json");
                try (OutputStream out = c.getOutputStream()) {
                    out.write(body.toString().getBytes(StandardCharsets.UTF_8));
                }
            }
            int code = c.getResponseCode();
            if (code == 204) return JSONObject.NULL;
            String text = read(code >= 400 ? c.getErrorStream() : c.getInputStream());
            if (code >= 300) {
                String msg = text;
                try {
                    msg = new JSONObject(text).optString("error", text);
                } catch (JSONException ignored) {
                }
                throw new HttpException(code, msg);
            }
            String t = text.trim();
            if (t.startsWith("[")) return new JSONArray(t);
            return new JSONObject(t);
        } catch (JSONException e) {
            throw new IOException("bad JSON from relay", e);
        } finally {
            c.disconnect();
        }
    }

    public JSONObject get(String path) throws IOException {
        return (JSONObject) call("GET", path, null, 15000);
    }

    public JSONObject getLong(String path) throws IOException {
        return (JSONObject) call("GET", path, null, 40000);
    }

    public Object post(String path, JSONObject body) throws IOException {
        return call("POST", path, body, 15000);
    }

    /** PUT raw bytes (sealed files); returns the blob id. */
    public String putBlob(String path, byte[] data) throws IOException {
        HttpURLConnection c = (HttpURLConnection) new URL(base + path).openConnection();
        try {
            c.setRequestMethod("PUT");
            c.setConnectTimeout(8000);
            c.setReadTimeout(120_000);
            c.setDoOutput(true);
            c.setFixedLengthStreamingMode(data.length);
            c.setRequestProperty("Content-Type", "application/octet-stream");
            c.setRequestProperty("User-Agent", userAgent);
            if (token != null) c.setRequestProperty("Authorization", "Bearer " + token);
            try (OutputStream out = c.getOutputStream()) {
                out.write(data);
            }
            int code = c.getResponseCode();
            String text = read(code >= 400 ? c.getErrorStream() : c.getInputStream());
            if (code >= 300) throw new HttpException(code, text);
            return new JSONObject(text).getString("id");
        } catch (JSONException e) {
            throw new IOException(e);
        } finally {
            c.disconnect();
        }
    }

    public byte[] getBlob(String path) throws IOException {
        HttpURLConnection c = (HttpURLConnection) new URL(base + path).openConnection();
        try {
            c.setConnectTimeout(8000);
            c.setReadTimeout(120_000);
            c.setRequestProperty("User-Agent", userAgent);
            if (token != null) c.setRequestProperty("Authorization", "Bearer " + token);
            int code = c.getResponseCode();
            if (code >= 300) throw new HttpException(code, read(c.getErrorStream()));
            try (InputStream in = c.getInputStream(); ByteArrayOutputStream out = new ByteArrayOutputStream()) {
                byte[] buf = new byte[64 << 10];
                int n;
                while ((n = in.read(buf)) > 0) {
                    out.write(buf, 0, n);
                    if (out.size() > 32 << 20) throw new IOException("file too large");
                }
                return out.toByteArray();
            }
        } finally {
            c.disconnect();
        }
    }

    private static String read(InputStream in) throws IOException {
        if (in == null) return "";
        try (InputStream s = in; ByteArrayOutputStream out = new ByteArrayOutputStream()) {
            byte[] buf = new byte[4096];
            int n;
            while ((n = s.read(buf)) > 0 && out.size() < 1 << 20) out.write(buf, 0, n);
            return out.toString(StandardCharsets.UTF_8.name());
        }
    }
}

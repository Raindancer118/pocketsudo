package de.raindancer118.pocketdeck;

import java.util.regex.Matcher;
import java.util.regex.Pattern;

/** Small SDP edits libwebrtc offers no API for. */
final class SdpTweaks {
    private static final Pattern OPUS = Pattern.compile("(?m)^a=rtpmap:(\\d+) opus/48000/2");

    private SdpTweaks() {
    }

    /** Asks for stereo Opus; without it the computer's sound is played as a mono voice call. */
    static String opusStereo(String sdp) {
        Matcher m = OPUS.matcher(sdp);
        if (!m.find()) return sdp;
        Matcher f = Pattern.compile("(?m)^a=fmtp:" + m.group(1) + " [^\\r\\n]*").matcher(sdp);
        if (!f.find()) return sdp;
        if (f.group().contains(";stereo=1") || f.group().contains(" stereo=1")) return sdp;
        return sdp.substring(0, f.end()) + ";stereo=1;sprop-stereo=1" + sdp.substring(f.end());
    }
}

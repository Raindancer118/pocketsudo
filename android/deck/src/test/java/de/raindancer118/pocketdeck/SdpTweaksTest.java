package de.raindancer118.pocketdeck;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertTrue;

import org.junit.Test;

public class SdpTweaksTest {
    @Test
    public void opusGetsStereo() {
        String offer = "m=audio 9 UDP/TLS/RTP/SAVPF 111 63\r\na=rtpmap:111 opus/48000/2\r\na=rtcp-fb:111 transport-cc\r\n"
                + "a=fmtp:111 minptime=10;useinbandfec=1\r\na=rtpmap:63 red/48000/2\r\n";
        String out = SdpTweaks.opusStereo(offer);
        assertTrue(out, out.contains("a=fmtp:111 minptime=10;useinbandfec=1;stereo=1;sprop-stereo=1\r\n"));
        assertEquals("only once", out, SdpTweaks.opusStereo(out));
    }

    @Test
    public void withoutOpusUnchanged() {
        String video = "m=video 9 UDP/TLS/RTP/SAVPF 96\r\na=rtpmap:96 H264/90000\r\n";
        assertEquals(video, SdpTweaks.opusStereo(video));
    }
}

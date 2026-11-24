package de.raindancer118.pocketdeck;

import java.util.HashMap;
import java.util.Locale;
import java.util.Map;

/** Picks the row icon for a file by its extension. */
final class FileTypes {
    private static final Map<String, Integer> BY_EXT = new HashMap<>();

    private static void map(int icon, String... exts) {
        for (String e : exts) BY_EXT.put(e, icon);
    }

    static {
        map(R.drawable.ic_file_image, "png", "jpg", "jpeg", "gif", "webp", "svg", "bmp", "heic", "avif", "ico", "tiff", "xcf", "psd", "kra");
        map(R.drawable.ic_file_video, "mp4", "mkv", "webm", "mov", "avi", "m4v", "flv", "wmv");
        map(R.drawable.ic_file_audio, "mp3", "flac", "ogg", "opus", "wav", "m4a", "aac", "mid", "midi");
        map(R.drawable.ic_file_archive, "zip", "tar", "gz", "tgz", "xz", "zst", "bz2", "7z", "rar", "jar", "deb", "rpm", "apk", "iso", "img", "aar");
        map(R.drawable.ic_file_code, "java", "kt", "go", "rs", "py", "js", "ts", "tsx", "jsx", "c", "h", "cpp", "hpp", "cs", "rb", "php",
                "html", "htm", "css", "scss", "json", "xml", "yml", "yaml", "toml", "gradle", "lua", "swift", "vue", "svelte", "sql", "ini", "conf", "lock");
        map(R.drawable.ic_file_terminal, "sh", "bash", "zsh", "fish", "ps1", "bat", "cmd", "exe", "appimage", "run", "bin");
        map(R.drawable.ic_file_table, "csv", "tsv", "xlsx", "xls", "ods", "numbers");
        map(R.drawable.ic_file_db, "db", "sqlite", "sqlite3", "mdb", "accdb", "dump");
        map(R.drawable.ic_file_text, "md", "txt", "pdf", "doc", "docx", "odt", "rtf", "tex", "epub", "log", "org", "rst", "pptx", "odp", "key");
    }

    private FileTypes() {
    }

    static int icon(String name) {
        int dot = name.lastIndexOf('.');
        if (dot <= 0 || dot == name.length() - 1) return R.drawable.ic_file;
        Integer i = BY_EXT.get(name.substring(dot + 1).toLowerCase(Locale.ROOT));
        return i == null ? R.drawable.ic_file : i;
    }
}

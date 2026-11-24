package de.raindancer118.pocketdeck;

import static org.junit.Assert.assertEquals;

import org.junit.Test;

public class FileTypesTest {
    @Test
    public void byExtension() {
        assertEquals(R.drawable.ic_file_text, FileTypes.icon("AGENTS.md"));
        assertEquals(R.drawable.ic_file_db, FileTypes.icon("economy.db"));
        assertEquals(R.drawable.ic_file_code, FileTypes.icon("mc-dat-viewer.HTML"));
        assertEquals(R.drawable.ic_file_terminal, FileTypes.icon("system-optimize.sh"));
        assertEquals(R.drawable.ic_file_archive, FileTypes.icon("backup.tar.gz"));
    }

    @Test
    public void noOrHiddenExtensionIsPlainFile() {
        assertEquals(R.drawable.ic_file, FileTypes.icon("Makefile"));
        assertEquals(R.drawable.ic_file, FileTypes.icon(".bashrc"));
        assertEquals(R.drawable.ic_file, FileTypes.icon("weird."));
    }
}

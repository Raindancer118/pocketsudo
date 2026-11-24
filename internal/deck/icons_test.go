package deck

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/Raindancer118/pocketsudo/protocol"
)

func writePNG(t *testing.T, path string, size int) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	img.Set(1, 1, color.RGBA{255, 0, 0, 255})
	var b bytes.Buffer
	png.Encode(&b, img)
	os.WriteFile(path, b.Bytes(), 0o644)
}

func TestAppIcons(t *testing.T) {
	r := newRig(t)
	data := t.TempDir()
	r.agent.dataDirs = []string{data}
	r.agent.iconCache = t.TempDir()
	os.MkdirAll(filepath.Join(data, "applications"), 0o755)
	desktop := func(id, icon string) {
		os.WriteFile(filepath.Join(data, "applications", id+".desktop"),
			[]byte("[Desktop Entry]\nType=Application\nName="+id+"\nIcon="+icon+"\n"), 0o644)
	}
	desktop("themed", "themed-icon")             // hicolor theme lookup
	desktop("abs", filepath.Join(data, "a.png")) // absolute path
	desktop("pix", "pixicon")                    // pixmaps fallback
	desktop("none", "does-not-exist")            // no logo: simply missing
	writePNG(t, filepath.Join(data, "icons/hicolor/48x48/apps/themed-icon.png"), 48)
	writePNG(t, filepath.Join(data, "icons/hicolor/96x96/apps/themed-icon.png"), 96)
	writePNG(t, filepath.Join(data, "a.png"), 64)
	writePNG(t, filepath.Join(data, "pixmaps/pixicon.png"), 32)

	res := result(t, r.send("apps.icons", nil, false))
	var out struct {
		Blob, Hash string
		Count      int
	}
	json.Unmarshal(res.Data, &out)
	if !res.OK || out.Blob == "" || out.Hash == "" || out.Count != 3 {
		t.Fatalf("apps.icons: %+v", res)
	}
	var raw []byte
	r.phone.Raw(context.Background(), "GET", "/v1/channels/"+r.channel+"/blobs/"+out.Blob, nil, &raw)
	plain, err := protocol.OpenBytes(r.key, raw)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(plain), int64(len(plain)))
	if err != nil {
		t.Fatal(err)
	}
	sizes := map[string]int{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		cfg, err := png.DecodeConfig(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("%s is no png: %v", f.Name, err)
		}
		sizes[f.Name] = cfg.Width
	}
	if len(sizes) != 3 || sizes["themed.png"] != 96 || sizes["abs.png"] != 64 || sizes["pix.png"] != 32 {
		t.Fatalf("icons: %v", sizes)
	}

	// nothing changed: the phone keeps its copy, no new upload
	res = result(t, r.send("apps.icons", map[string]string{"have": out.Hash}, false))
	var again struct{ Blob, Hash string }
	json.Unmarshal(res.Data, &again)
	if !res.OK || again.Blob != "" || again.Hash != out.Hash {
		t.Fatalf("unchanged icons uploaded again: %+v", again)
	}
}

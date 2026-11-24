package deck

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Raindancer118/pocketsudo/protocol"
)

type sentFile struct {
	Blob, Name string
	Size       int
}

// received reads the next "file" event from the phone's inbox and fetches its blob.
func (r *rig) received() (sentFile, []byte) {
	r.t.Helper()
	m := r.awaitType("file")
	var f sentFile
	json.Unmarshal(m.Body, &f)
	var raw []byte
	r.phone.Raw(context.Background(), "GET", "/v1/channels/"+r.channel+"/blobs/"+f.Blob, nil, &raw)
	plain, err := protocol.OpenBytes(r.key, raw)
	if err != nil {
		r.t.Fatalf("blob for %s: %v", f.Name, err)
	}
	return f, plain
}

func TestSendFilesToPhone(t *testing.T) {
	r := newRig(t)
	dir := t.TempDir()
	write := func(rel, body string) string {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
		return p
	}
	a := write("Rechnung.pdf", "%PDF-1.7 rechnung")
	b := write("notiz.txt", "hallo handy")
	folder := filepath.Join(dir, "Urlaub")
	write("Urlaub/1.jpg", "jpg1")
	write("Urlaub/sub/2.jpg", "jpg2")

	// single files: one message each, original name and bytes
	if _, err := r.agent.SendFiles(context.Background(), []string{a, b}, false); err != nil {
		t.Fatal(err)
	}
	f1, d1 := r.received()
	f2, d2 := r.received()
	if f1.Name != "Rechnung.pdf" || string(d1) != "%PDF-1.7 rechnung" || f1.Size != len(d1) || f2.Name != "notiz.txt" || string(d2) != "hallo handy" {
		t.Fatalf("files: %+v %+v", f1, f2)
	}

	// a folder always travels as zip, with its inner structure
	if _, err := r.agent.SendFiles(context.Background(), []string{folder}, false); err != nil {
		t.Fatal(err)
	}
	fz, dz := r.received()
	zr, err := zip.NewReader(bytes.NewReader(dz), int64(len(dz)))
	if err != nil || fz.Name != "Urlaub.zip" {
		t.Fatalf("folder zip %q: %v", fz.Name, err)
	}
	var names []string
	for _, e := range zr.File {
		names = append(names, e.Name)
	}
	if strings.Join(names, ",") != "Urlaub/1.jpg,Urlaub/sub/2.jpg" {
		t.Fatalf("zip entries %v", names)
	}

	// --zip: everything in one archive
	if _, err := r.agent.SendFiles(context.Background(), []string{a, b}, true); err != nil {
		t.Fatal(err)
	}
	fa, da := r.received()
	if !strings.HasSuffix(fa.Name, ".zip") {
		t.Fatalf("zip name %q", fa.Name)
	}
	if zr, _ := zip.NewReader(bytes.NewReader(da), int64(len(da))); zr == nil || len(zr.File) != 2 {
		t.Fatal("zip of two files")
	}
}

func TestSendLimits(t *testing.T) {
	r := newRig(t)
	dir := t.TempDir()
	big := filepath.Join(dir, "film.mkv")
	os.WriteFile(big, bytes.Repeat([]byte{1}, 2048), 0o644)
	r.agent.maxSend = 1024
	_, err := r.agent.SendFiles(context.Background(), []string{big}, false)
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized file: %v", err)
	}
	if _, err := r.agent.SendFiles(context.Background(), []string{filepath.Join(dir, "gibtsnicht")}, false); err == nil {
		t.Fatal("missing file accepted")
	}

	// many single files would flood the relay's file slots: they go as one zip
	r.agent.maxSend = 1 << 20
	var many []string
	for i := 0; i < maxSingleSends+1; i++ {
		p := filepath.Join(dir, "f"+itoa(int64(i))+".txt")
		os.WriteFile(p, []byte("x"), 0o644)
		many = append(many, p)
	}
	names, err := r.agent.SendFiles(context.Background(), many, false)
	if err != nil || len(names) != 1 || !strings.HasSuffix(names[0], ".zip") {
		t.Fatalf("many files: %v %v", names, err)
	}
}

func TestSendImageCarriesPreview(t *testing.T) {
	r := newRig(t)
	dir := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 1600, 900))
	for x := 0; x < 1600; x++ {
		img.Set(x, 450, color.RGBA{200, 30, 30, 255})
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	photo := filepath.Join(dir, "Urlaub.png")
	os.WriteFile(photo, b.Bytes(), 0o644)
	text := filepath.Join(dir, "notiz.txt")
	os.WriteFile(text, []byte("kein bild"), 0o644)

	if _, err := r.agent.SendFiles(context.Background(), []string{photo, text}, false); err != nil {
		t.Fatal(err)
	}
	var withThumb struct{ Name, Thumb string }
	json.Unmarshal(r.awaitType("file").Body, &withThumb)
	raw, err := base64.StdEncoding.DecodeString(withThumb.Thumb)
	if err != nil || len(raw) == 0 {
		t.Fatalf("no preview for %s: %v", withThumb.Name, err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width != thumbPx || cfg.Height != thumbPx*900/1600 || len(withThumb.Thumb) > 60<<10 {
		t.Fatalf("preview %dx%d, %d bytes: %v", cfg.Width, cfg.Height, len(withThumb.Thumb), err)
	}
	var plain struct{ Name, Thumb string }
	json.Unmarshal(r.awaitType("file").Body, &plain)
	if plain.Name != "notiz.txt" || plain.Thumb != "" {
		t.Fatalf("text file got a preview: %+v", plain)
	}
}

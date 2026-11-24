package deck

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// iconPx is the edge of the PNGs sent to the phone (a 40 dp row icon at ~2.8x).
const iconPx = 96

func defaultIconCache() string {
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "pocketdeck", "icons")
	}
	return filepath.Join(os.TempDir(), "pocketdeck-icons")
}

// appsIcons sends every app logo it can find as one zip of <app id>.png.
// The phone passes the hash of its copy; when nothing changed it gets no blob.
func (a *Agent) appsIcons(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	var p struct{ Have string }
	if len(body) > 0 {
		decode(body, &p)
	}
	idx := a.icons()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	h := sha256.New()
	h.Write([]byte("zip-v2")) // bump when the zip format changes, so phones fetch again
	count := 0
	for _, app := range installedApps(a.dataDirs) {
		img := a.appIcon(ctx, app, idx)
		if img == nil {
			continue
		}
		h.Write([]byte(app.ID))
		h.Write(img)
		// Deflate even though PNG is compressed: Go writes Stored entries with a data
		// descriptor, which Java's ZipInputStream on the phone cannot read
		w, err := zw.Create(app.ID + ".png")
		if err != nil {
			return nil, err
		}
		w.Write(img)
		count++
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	hash := hex.EncodeToString(h.Sum(nil))[:16]
	if p.Have == hash {
		return ok(map[string]string{"hash": hash})
	}
	id, err := a.upload(ctx, buf.Bytes())
	if err != nil {
		return nil, err
	}
	return ok(map[string]any{"hash": hash, "blob": id, "count": count})
}

// icons returns the icon index, rebuilt every 10 minutes (new apps, theme change).
func (a *Agent) icons() *iconIndex {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.iconIdx == nil || time.Since(a.iconIdxAt) > 10*time.Minute {
		a.iconIdx = buildIconIndex(a.dataDirs, iconThemes(a.dataDirs))
		a.iconIdxAt = time.Now()
	}
	return a.iconIdx
}

// appIcon returns the app's logo as a PNG of at most iconPx, or nil.
func (a *Agent) appIcon(ctx context.Context, app App, idx *iconIndex) []byte {
	src := idx.find(app.Icon)
	if src == "" {
		return nil
	}
	st, err := os.Stat(src)
	if err != nil {
		return nil
	}
	// cache key: source path + mtime, so updated logos are picked up
	sum := sha256.Sum256([]byte(src + "|" + st.ModTime().String()))
	cached := filepath.Join(a.iconCache, hex.EncodeToString(sum[:8])+".png")
	if b, err := os.ReadFile(cached); err == nil {
		return b
	}
	var out []byte
	if strings.HasSuffix(src, ".png") {
		b, err := os.ReadFile(src)
		if err != nil {
			return nil
		}
		if cfg, err := png.DecodeConfig(bytes.NewReader(b)); err == nil && cfg.Width <= iconPx*4/3 {
			out = b
		}
	}
	if out == nil {
		// SVG, XPM or a large PNG: render to iconPx
		os.MkdirAll(a.iconCache, 0o755)
		tmp := cached + ".tmp"
		var err error
		if strings.HasSuffix(src, ".svg") || strings.HasSuffix(src, ".svgz") {
			_, err = a.sys.Run(ctx, "rsvg-convert", "-w", "96", "-h", "96", "-a", "-o", tmp, src)
		} else {
			_, err = a.sys.Run(ctx, "magick", src, "-resize", "96x96", "png:"+tmp)
		}
		b, rerr := os.ReadFile(tmp)
		os.Remove(tmp)
		if err != nil || rerr != nil || len(b) == 0 {
			return nil
		}
		out = b
	}
	os.MkdirAll(a.iconCache, 0o755)
	os.WriteFile(cached, out, 0o644)
	return out
}

// sizes in order of preference: close to iconPx first, then larger, then smaller
var iconSizes = []string{"96", "128", "64", "256", "scalable", "512", "48", "72", "32", "24", "22", "16"}

var iconExts = []string{".png", ".svg", ".svgz", ".xpm"}

// iconIndex maps icon name -> best file, per theme in lookup order. Walking each
// theme once is much faster than probing size/category combinations per app.
type iconIndex struct {
	themes []map[string]iconHit
	pixmap map[string]string
}

type iconHit struct {
	path string
	rank int
}

func sizeRank(rel string) int {
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		short, _, _ := strings.Cut(part, "x")
		short, _, _ = strings.Cut(short, "@") // 48x48@2
		for i, s := range iconSizes {
			if short == s {
				return i
			}
		}
	}
	return len(iconSizes)
}

func iconExt(name string) (string, bool) {
	for _, e := range iconExts {
		if strings.HasSuffix(name, e) {
			return strings.TrimSuffix(name, e), true
		}
	}
	return "", false
}

func buildIconIndex(dataDirs, themes []string) *iconIndex {
	idx := &iconIndex{pixmap: map[string]string{}}
	for _, theme := range themes {
		m := map[string]iconHit{}
		for _, base := range dataDirs {
			root := filepath.Join(base, "icons", theme)
			filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				name, ok := iconExt(d.Name())
				if !ok {
					return nil
				}
				rel, _ := filepath.Rel(root, path)
				r := sizeRank(rel)
				if old, seen := m[name]; !seen || r < old.rank {
					m[name] = iconHit{path, r}
				}
				return nil
			})
		}
		idx.themes = append(idx.themes, m)
	}
	for _, base := range dataDirs {
		entries, _ := os.ReadDir(filepath.Join(base, "pixmaps"))
		for _, e := range entries {
			if name, ok := iconExt(e.Name()); ok && idx.pixmap[name] == "" {
				idx.pixmap[name] = filepath.Join(base, "pixmaps", e.Name())
			}
		}
	}
	return idx
}

// find resolves an Icon= value: absolute path, then the themes in order, then pixmaps.
func (idx *iconIndex) find(name string) string {
	if name == "" {
		return ""
	}
	if filepath.IsAbs(name) {
		if fileExists(name) {
			return name
		}
		return ""
	}
	for _, m := range idx.themes {
		if h, ok := m[name]; ok {
			return h.path
		}
	}
	return idx.pixmap[name]
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// iconThemes: the configured GTK icon theme and what it inherits, then hicolor.
func iconThemes(dataDirs []string) []string {
	var themes []string
	seen := map[string]bool{}
	var add func(string)
	add = func(t string) {
		if t == "" || seen[t] || len(themes) > 8 {
			return
		}
		seen[t] = true
		themes = append(themes, t)
		for _, base := range dataDirs {
			if parents := iniValue(filepath.Join(base, "icons", t, "index.theme"), "Inherits"); parents != "" {
				for _, p := range strings.Split(parents, ",") {
					add(strings.TrimSpace(p))
				}
				break
			}
		}
	}
	if d, err := os.UserConfigDir(); err == nil {
		add(iniValue(filepath.Join(d, "gtk-3.0", "settings.ini"), "gtk-icon-theme-name"))
		add(iniValue(filepath.Join(d, "kdeglobals"), "Theme"))
	}
	// last resorts for generic names the configured theme lacks
	for _, t := range []string{"hicolor", "Adwaita", "AdwaitaLegacy", "Papirus"} {
		add(t)
	}
	return themes
}

func iniValue(path, key string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, found := strings.Cut(sc.Text(), "=")
		if found && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

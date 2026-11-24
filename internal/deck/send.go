package deck

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// maxSingleSends: more files than this go as one zip; the relay keeps at most
// 20 files per pairing at a time, and the phone should not get a flood of downloads.
const maxSingleSends = 8

// SendFiles pushes files from this computer to the phone ("pocketdeck send").
// Files go one by one, folders as a zip each; zipAll (or many files) makes one
// zip of everything. Each arrives encrypted in the relay's file store (15 min)
// with a push, and the phone downloads it by itself.
func (a *Agent) SendFiles(ctx context.Context, paths []string, zipAll bool) ([]string, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("no files given")
	}
	abs := make([]string, 0, len(paths))
	for _, p := range paths {
		ap, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(ap); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		abs = append(abs, ap)
	}
	if zipAll || len(abs) > maxSingleSends {
		name := fmt.Sprintf("PocketDeck-%s.zip", time.Now().Format("2006-01-02-150405"))
		if len(abs) == 1 {
			name = filepath.Base(abs[0]) + ".zip"
		}
		data, err := zipPaths(ctx, abs, a.maxSend)
		if err != nil {
			return nil, err
		}
		return []string{name}, a.sendBlob(ctx, name, data)
	}
	var sent []string
	for _, p := range abs {
		st, _ := os.Stat(p)
		name := filepath.Base(p)
		var data []byte
		var err error
		if st.IsDir() {
			name += ".zip"
			data, err = zipPaths(ctx, []string{p}, a.maxSend)
		} else {
			if st.Size() > a.maxSend {
				return sent, fmt.Errorf("%s is too large (%d MB, max %d MB)", name, st.Size()>>20, a.maxSend>>20)
			}
			data, err = os.ReadFile(p)
		}
		if err != nil {
			return sent, fmt.Errorf("%s: %w", name, err)
		}
		if err := a.sendBlob(ctx, name, data); err != nil {
			return sent, err
		}
		sent = append(sent, name)
	}
	return sent, nil
}

func (a *Agent) sendBlob(ctx context.Context, name string, data []byte) error {
	id, err := a.upload(ctx, data)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return a.notifyErr(ctx, "file", map[string]any{"blob": id, "name": name, "size": len(data)})
}

package host

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// freezer is a TCP proxy that can stop forwarding without closing anything:
// what a dead WLAN link looks like after suspend or a network change.
type freezer struct {
	ln     net.Listener
	frozen atomic.Bool
}

func newFreezer(t *testing.T, target string) *freezer {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &freezer{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			up, err := net.Dial("tcp", target)
			if err != nil {
				c.Close()
				continue
			}
			pipe := func(dst, src net.Conn) {
				buf := make([]byte, 32<<10)
				for {
					n, err := src.Read(buf)
					if err != nil {
						return
					}
					for f.frozen.Load() {
						time.Sleep(50 * time.Millisecond) // swallow nothing, deliver nothing
					}
					dst.Write(buf[:n])
				}
			}
			go pipe(up, c)
			go pipe(c, up)
		}
	}()
	return f
}

func TestDeadConnectionDoesNotHangForever(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	f := newFreezer(t, srv.Listener.Addr().String())

	c := NewClient("https://"+f.ln.Addr().String(), "")
	trustTestServer(c, srv)
	c.HTTP.Timeout = 3 * time.Second
	if _, err := c.Do(context.Background(), "GET", "/", nil, nil); err != nil {
		t.Fatalf("first request: %v", err)
	}

	f.frozen.Store(true)
	done := make(chan error, 1)
	go func() {
		_, err := c.Do(context.Background(), "GET", "/", nil, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("request through a dead connection succeeded")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("request hangs on a dead connection (the agent stopped answering for 50 min like this)")
	}
	_ = io.Discard
}

// trustTestServer lets the client's own transport accept the test server's certificate.
func trustTestServer(c *Client, srv *httptest.Server) {
	cfg := srv.Client().Transport.(*http.Transport).TLSClientConfig
	if tr, ok := c.HTTP.Transport.(*http.Transport); ok {
		tr.TLSClientConfig = cfg
		return
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = cfg
	c.HTTP.Transport = tr
}

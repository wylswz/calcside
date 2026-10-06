package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"calcside/cmd/calcside/internal/artifact"
	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/sandbox"
	"calcside/internal/types"
)

const maxPreviewCacheBytes = 32 << 20
const maxPreviewSnapshots = 128

type previewSnapshot struct {
	id, userID, instanceID string
	epoch                  int64
	ticket                 [32]byte
	body                   []byte
	interactive            bool
	expires                time.Time
	timer                  *time.Timer
}

type ArtifactPreviews struct {
	base          *url.URL
	consoleOrigin string
	allowScripts  bool
	sandbox       *sandbox.Service
	now           func() time.Time
	slots         chan struct{}
	mu            sync.Mutex
	snapshots     map[string]*previewSnapshot
	bytes         int
	closed        bool
}

func NewArtifactPreviews(cfg artifact.Config, sbx *sandbox.Service) (*ArtifactPreviews, error) {
	base, console, err := cfg.Origins()
	if err != nil {
		return nil, err
	}
	return &ArtifactPreviews{base: base, consoleOrigin: console, allowScripts: cfg.AllowScripts, sandbox: sbx, now: time.Now, slots: make(chan struct{}, 4), snapshots: map[string]*previewSnapshot{}}, nil
}

func (p *ArtifactPreviews) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	for id := range p.snapshots {
		p.removeLocked(id)
	}
}

func (p *ArtifactPreviews) removeLocked(id string) {
	if snapshot := p.snapshots[id]; snapshot != nil {
		p.bytes -= len(snapshot.body)
		if snapshot.timer != nil {
			snapshot.timer.Stop()
		}
		delete(p.snapshots, id)
	}
}

func (p *ArtifactPreviews) create(userID, instanceID string, epoch int64, body []byte, interactive bool, instanceExpiry time.Time) (string, time.Time, error) {
	var idBytes [20]byte
	var ticketBytes [32]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return "", time.Time{}, err
	}
	if _, err := rand.Read(ticketBytes[:]); err != nil {
		return "", time.Time{}, err
	}
	id := hex.EncodeToString(idBytes[:])
	ticket := base64.RawURLEncoding.EncodeToString(ticketBytes[:])
	now := p.now()
	expiry := now.Add(artifact.PreviewTTLSeconds * time.Second)
	if instanceExpiry.Before(expiry) {
		expiry = instanceExpiry
	}
	if !expiry.After(now) {
		return "", time.Time{}, service.Errf(types.ErrCodeNotRunning, "instance expired")
	}
	snapshot := &previewSnapshot{id: id, userID: userID, instanceID: instanceID, epoch: epoch, ticket: sha256.Sum256([]byte(ticket)), body: body, interactive: interactive, expires: expiry}
	p.mu.Lock()
	defer p.mu.Unlock()
	own, bytes, count := 0, p.bytes, len(p.snapshots)
	var replace string
	for oldID, old := range p.snapshots {
		if !old.expires.After(now) {
			p.removeLocked(oldID)
			bytes -= len(old.body)
			count--
			continue
		}
		if old.userID == userID {
			own++
			if old.instanceID == instanceID {
				replace = oldID
				bytes -= len(old.body)
				count--
				own--
			}
		}
	}
	if p.closed || count >= maxPreviewSnapshots || own >= 16 || len(body) > maxPreviewCacheBytes-bytes {
		return "", time.Time{}, service.Errf(types.ErrCodeTooMany, "preview capacity reached; close stale instances or wait for previews to expire")
	}
	p.removeLocked(replace)
	p.snapshots[id] = snapshot
	p.bytes += len(body)
	snapshot.timer = time.AfterFunc(expiry.Sub(now), func() { p.mu.Lock(); defer p.mu.Unlock(); p.removeLocked(id) })
	u := *p.base
	u.Host = id + "." + p.base.Host
	u.Path = "/"
	u.RawQuery = url.Values{"ticket": []string{ticket}}.Encode()
	return u.String(), expiry, nil
}

func (p *ArtifactPreviews) matchesHost(host string) bool {
	if p == nil || p.base == nil {
		return false
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	return host == p.base.Hostname() || strings.HasSuffix(host, "."+p.base.Hostname())
}

func (p *ArtifactPreviews) Wrap(next http.Handler) http.Handler {
	if p == nil || p.base == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !p.matchesHost(r.Host) {
			next.ServeHTTP(w, r)
			return
		}
		p.serve(w, r)
	})
}

func artifactHeaders(h http.Header) {
	h.Set("Cache-Control", "private, no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
}

func (p *ArtifactPreviews) serve(w http.ResponseWriter, r *http.Request) {
	artifactHeaders(w.Header())
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox; frame-ancestors "+p.consoleOrigin)
	unavailable := func() { http.Error(w, "preview unavailable", http.StatusNotFound) }
	if r.Method != http.MethodGet && r.Method != http.MethodHead || r.URL.Path != "/" {
		unavailable()
		return
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	id := strings.TrimSuffix(host, "."+p.base.Hostname())
	if len(id) != 40 || strings.Contains(id, ".") {
		unavailable()
		return
	}
	ticket := r.URL.Query().Get("ticket")
	if len(ticket) != 43 {
		unavailable()
		return
	}
	digest := sha256.Sum256([]byte(ticket))
	p.mu.Lock()
	snapshot := p.snapshots[id]
	valid := snapshot != nil && snapshot.expires.After(p.now()) && subtle.ConstantTimeCompare(digest[:], snapshot.ticket[:]) == 1
	p.mu.Unlock()
	if !valid {
		unavailable()
		return
	}
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		http.Error(w, "preview temporarily unavailable", http.StatusTooManyRequests)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := p.sandbox.ArtifactAlive(ctx, snapshot.userID, snapshot.instanceID, snapshot.epoch); err != nil {
		p.mu.Lock()
		p.removeLocked(id)
		p.mu.Unlock()
		unavailable()
		return
	}
	p.mu.Lock()
	valid = p.snapshots[id] == snapshot && snapshot.expires.After(p.now())
	p.mu.Unlock()
	if !valid {
		unavailable()
		return
	}
	sandboxValue, script := "sandbox", "script-src 'none'"
	if snapshot.interactive {
		sandboxValue += " allow-scripts"
		script = "script-src 'unsafe-inline' data:"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", sandboxValue+"; default-src 'none'; "+script+"; style-src 'unsafe-inline'; img-src data:; font-src 'none'; connect-src 'none'; frame-src 'none'; object-src 'none'; worker-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors "+p.consoleOrigin)
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=(), serial=(), clipboard-read=(), clipboard-write=(), display-capture=(), fullscreen=(), storage-access=()")
	w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	if r.Method == http.MethodGet {
		_, _ = w.Write(snapshot.body)
	} else {
		w.WriteHeader(http.StatusOK)
	}
}

func (p *ArtifactPreviews) limit() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !strings.HasSuffix(c.Request.URL.Path, "/artifacts/preview") && !strings.HasSuffix(c.Request.URL.Path, "/artifacts/export") {
			c.Next()
			return
		}
		select {
		case p.slots <- struct{}{}:
			defer func() { <-p.slots }()
			c.Next()
		default:
			artifactHeaders(c.Writer.Header())
			writeErr(c.Writer, http.StatusTooManyRequests, types.ErrCodeTooMany, "too many concurrent artifact operations")
			c.Abort()
		}
	}
}

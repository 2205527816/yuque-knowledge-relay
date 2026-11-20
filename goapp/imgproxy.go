package main

import (
	"context"
	"crypto/sha1"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const imgPlaceholder = `<svg xmlns="http://www.w3.org/2000/svg" width="640" height="200"><rect width="100%" height="100%" fill="#eee"/><text x="50%" y="50%" fill="#999" font-size="16" text-anchor="middle">图片加载失败</text></svg>`

// unsafeIP 拒绝环回/私网/链路本地/保留/多播地址，防 SSRF。
func unsafeIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() ||
		ip.IsInterfaceLocalMulticast()
}

// safeClient 每次解析域名后只连已验证的公网 IP，并在 TLS 里保留原 SNI。
func safeClient(host string) (*http.Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	var chosen net.IP
	for _, item := range ips {
		if !unsafeIP(item.IP) {
			chosen = item.IP
			break
		}
	}
	if chosen == nil {
		return nil, fmt.Errorf("目标地址不允许（内网/环回）")
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			_, port, err := net.SplitHostPort(addr)
			if err != nil {
				port = "443"
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(chosen.String(), port))
		},
		TLSClientConfig: &tls.Config{ServerName: host},
	}
	return &http.Client{Timeout: 20 * time.Second, Transport: tr}, nil
}

func cachePath(rawURL string) (string, string) {
	sum := sha1.Sum([]byte(rawURL))
	key := hex.EncodeToString(sum[:])
	dir := filepath.Join(rootDir, "data", "image_cache")
	os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, key+".bin"), filepath.Join(dir, key+".type")
}

func servePlaceholder(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Write([]byte(imgPlaceholder))
}

// imgHandler 代理外链图片，带 Referer、磁盘缓存和 SSRF 防护。
func imgHandler(w http.ResponseWriter, r *http.Request) {
	queueView := r.URL.Query().Get("view") == "queue"
	fallback := func() {
		if queueView {
			http.NotFound(w, r)
		} else {
			servePlaceholder(w)
		}
	}
	raw := r.URL.Query().Get("u")
	referer := r.URL.Query().Get("r")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		fallback()
		return
	}
	bin, typ := cachePath(raw)
	thumb := bin + ".queue-v1.jpg"
	if queueView {
		if image, err := os.ReadFile(thumb); err == nil {
			serveQueueImage(w, image)
			return
		}
	}
	if data, err := os.ReadFile(bin); err == nil {
		if queueView {
			writeQueueImage(w, thumb, data)
			return
		}
		if ct, err2 := os.ReadFile(typ); err2 == nil {
			w.Header().Set("Content-Type", strings.TrimSpace(string(ct)))
			w.Write(data)
			return
		}
	}
	client, err := safeClient(u.Hostname())
	if err != nil {
		fallback()
		return
	}
	req, err := http.NewRequest("GET", raw, nil)
	if err != nil {
		fallback()
		return
	}
	req.Header.Set("User-Agent", browserUA)
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	resp, err := client.Do(req)
	if err != nil {
		fallback()
		return
	}
	defer resp.Body.Close()
	ct := resp.Header.Get("Content-Type")
	if resp.StatusCode >= 400 || !strings.HasPrefix(ct, "image/") {
		fallback()
		return
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 15<<20))
	if err != nil || len(data) == 0 {
		fallback()
		return
	}
	os.WriteFile(bin, data, 0o644)
	os.WriteFile(typ, []byte(ct), 0o644)
	if queueView {
		writeQueueImage(w, thumb, data)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Write(data)
}

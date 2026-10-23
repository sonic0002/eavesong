package ai

import (
	"context"
	"errors"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

func publicIP(ip net.IP) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified() && !ip.Equal(net.ParseIP("169.254.169.254"))
}

// Resolve and connect to the same checked address, preventing DNS rebinding.
func publicDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, errors.New("no public address")
	}
	for _, ip := range ips {
		if !publicIP(ip.IP) {
			return nil, errors.New("linked page must have a public address")
		}
	}
	return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
}
func pageURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || (u.Port() != "" && u.Port() != "443" && u.Port() != "80") {
		return errors.New("linked page must be a public HTTP or HTTPS URL")
	}
	return nil
}

var hiddenHTML = regexp.MustCompile(`(?is)<(script|style|noscript|svg)\b[^>]*>.*?</(?:script|style|noscript|svg)\s*>`)
var tagsHTML = regexp.MustCompile(`(?s)<[^>]*>`)

func pageText(body string) string {
	body = hiddenHTML.ReplaceAllString(body, " ")
	body = tagsHTML.ReplaceAllString(body, " ")
	text := strings.Join(strings.Fields(html.UnescapeString(body)), " ")
	runes := []rune(text)
	if len(runes) > 8000 {
		text = string(runes[:8000])
	}
	return text
}

// ReadPage is opt-in, bounded and credential-free. It reads public text only;
// an unreadable page never stops the ordinary mention-only drafting path.
func ReadPage(ctx context.Context, raw string) (string, error) {
	if err := pageURL(raw); err != nil {
		return "", err
	}
	tr := &http.Transport{DialContext: publicDial, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("too many redirects")
		}
		return pageURL(req.URL.String())
	}}
	req, err := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Eavesong/0.3 (optional reply context)")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", errors.New("linked page unavailable")
	}
	typ := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(typ, "text/html") && !strings.HasPrefix(typ, "text/plain") {
		return "", errors.New("linked page is not text")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if err != nil {
		return "", err
	}
	text := pageText(string(body))
	if strings.HasPrefix(typ, "text/plain") {
		text = string([]rune(string(body))[:min(len([]rune(string(body))), 8000)])
	}
	return resp.Request.URL.String() + "\n" + text, nil
}

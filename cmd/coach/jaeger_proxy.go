package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
)

const (
	jaegerProxyPrefix      = "/jaeger"
	jaegerBaseHTMLNeedle   = `<base href="/" data-inject-target="BASE_URL" />`
	jaegerBaseHTMLFallback = `<base href="/">`
	jaegerBaseHTMLReplace  = `<base href="/jaeger/" data-inject-target="BASE_URL" />`
)

func (s *coachServer) jaegerProxyHandler() http.Handler {
	target, err := url.Parse(s.jaegerProxyURL)
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, fmt.Sprintf("invalid JAEGER_PROXY_URL: %v", err), http.StatusServiceUnavailable)
		})
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	baseDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		baseDirector(req)
		req.URL.Path = proxiedJaegerPath(req.URL.Path)
		if req.URL.RawPath != "" {
			req.URL.RawPath = proxiedJaegerPath(req.URL.RawPath)
		}
		req.Host = target.Host
		req.Header.Del("Accept-Encoding")
	}
	proxy.ModifyResponse = rewriteJaegerBaseHref
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		http.Error(w, fmt.Sprintf("jaeger proxy error: %v", err), http.StatusBadGateway)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == jaegerProxyPrefix {
			targetURL := jaegerProxyPrefix + "/"
			if r.URL.RawQuery != "" {
				targetURL += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, targetURL, http.StatusTemporaryRedirect)
			return
		}
		proxy.ServeHTTP(w, r)
	})
}

func proxiedJaegerPath(path string) string {
	trimmed := strings.TrimPrefix(path, jaegerProxyPrefix)
	if trimmed == "" {
		return "/"
	}
	if strings.HasPrefix(trimmed, "/") {
		return trimmed
	}
	return "/" + trimmed
}

func rewriteJaegerBaseHref(resp *http.Response) error {
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		return nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()

	rewritten := bytes.ReplaceAll(body, []byte(jaegerBaseHTMLNeedle), []byte(jaegerBaseHTMLReplace))
	if bytes.Equal(rewritten, body) {
		rewritten = bytes.ReplaceAll(body, []byte(jaegerBaseHTMLFallback), []byte(jaegerBaseHTMLReplace))
	}

	resp.Body = io.NopCloser(bytes.NewReader(rewritten))
	resp.ContentLength = int64(len(rewritten))
	resp.Header.Set("Content-Length", strconv.Itoa(len(rewritten)))
	resp.Header.Del("Content-Encoding")
	return nil
}

package lifecycle

import (
	"crypto/subtle"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

func executionURL(base, profile, workspace string) string {
	return strings.TrimRight(base, "/") + "/v1/execution/" + url.PathEscape(profile) + "/" + url.PathEscape(workspace)
}

func (c *Controller) gateway() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		profile, workspace := r.PathValue("profile"), r.PathValue("workspaceId")
		state, _, err := c.load(r.Context(), profile, workspace)
		expected := ""
		if err == nil && state.Phase == "running" && state.Handle != nil {
			expected = "Bearer " + c.token(workspace, state.Epoch)
		}
		if expected == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(expected)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		access, err := c.Access(r.Context(), profile, workspace)
		if err != nil || access.Token == "" || "Bearer "+access.Token != expected {
			http.Error(w, "workspace unavailable", 503)
			return
		}
		target, err := url.Parse(access.State.Endpoint)
		if err != nil || target.Host == "" || target.Scheme != "http" {
			http.Error(w, "workspace unavailable", 503)
			return
		}
		path := r.PathValue("path")
		if !strings.HasPrefix(path, "v1/") || strings.Contains(path, "\\") {
			http.Error(w, "invalid execution path", 400)
			return
		}
		for _, segment := range strings.Split(path, "/") {
			if segment == "." || segment == ".." {
				http.Error(w, "invalid execution path", 400)
				return
			}
		}
		proxy := &httputil.ReverseProxy{
			Rewrite: func(p *httputil.ProxyRequest) {
				p.SetURL(target)
				p.Out.URL.Path = "/" + path
				p.Out.URL.RawPath = ""
				p.Out.Host = target.Host
				p.Out.Header.Del("Cookie")
			},
			FlushInterval: -1,
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
				http.Error(w, "execution unavailable", http.StatusBadGateway)
			},
		}
		w.Header().Set("Cache-Control", "no-store")
		proxy.ServeHTTP(w, r)
	})
}

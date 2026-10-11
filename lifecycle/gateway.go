package lifecycle

import (
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
		access, status := c.gatewayAccess(r.Context(), profile, workspace, r.Header.Get("Authorization"))
		if status != http.StatusOK {
			message := "workspace unavailable"
			if status == http.StatusUnauthorized {
				message = "unauthorized"
			}
			http.Error(w, message, status)
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
				// The authenticated central IDE proxy supplies the browser origin.
				// ReverseProxy strips these before Rewrite; code-server needs them.
				if strings.HasPrefix(path, "v1/ide/") {
					for _, name := range []string{"X-Forwarded-Host", "X-Forwarded-Proto"} {
						if value := p.In.Header.Get(name); value != "" {
							p.Out.Header.Set(name, value)
						}
					}
				}
			},
			FlushInterval: -1,
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
				c.invalidateGateway(profile, workspace)
				http.Error(w, "execution unavailable", http.StatusBadGateway)
			},
		}
		w.Header().Set("Cache-Control", "no-store")
		proxy.ServeHTTP(w, r)
	})
}

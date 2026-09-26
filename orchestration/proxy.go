package orchestration

import (
	"encoding/base64"
	"net/http"
	"net/http/httputil"
	"net/url"
)

// Proxy preserves all native HTTP, SSE and PTY endpoints. Resolve must authenticate
// the request and authorize its run before returning worker-owned routing state.
func (e *Engine) Proxy(resolve func(*http.Request) (Request, Prepared, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		run, prepared, err := resolve(r)
		if err != nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		target := &url.URL{Scheme: "http", Host: prepared.Lease.Host + ":4096"}
		p := &httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("opencode:"+e.Password(run.Key))))
		}, Transport: e.Transport, FlushInterval: -1}
		p.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "agent unavailable", http.StatusBadGateway)
		}
		p.ServeHTTP(w, r)
	})
}

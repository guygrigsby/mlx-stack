package router

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// ProxyJSON forwards a JSON request to upstreamBase, rewriting the "model"
// field to upstreamModel. Streams the response body through as-is.
func ProxyJSON(w http.ResponseWriter, r *http.Request, upstreamBase, upstreamModel string) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), 400)
		return err
	}
	rewritten, err := RewriteModel(body, upstreamModel)
	if err != nil {
		http.Error(w, "rewrite: "+err.Error(), 400)
		return err
	}

	u, err := url.Parse(upstreamBase)
	if err != nil {
		http.Error(w, "bad upstream url", 500)
		return err
	}
	u.Path = r.URL.Path
	u.RawQuery = r.URL.RawQuery

	outReq, err := http.NewRequestWithContext(r.Context(), r.Method, u.String(), bytes.NewReader(rewritten))
	if err != nil {
		http.Error(w, "build req: "+err.Error(), 500)
		return err
	}
	copyHeaders(outReq.Header, r.Header)
	outReq.ContentLength = int64(len(rewritten))
	outReq.Header.Set("Content-Length", fmt.Sprintf("%d", len(rewritten)))

	resp, err := http.DefaultTransport.RoundTrip(outReq)
	if err != nil {
		http.Error(w, "upstream: "+err.Error(), 502)
		return err
	}
	defer resp.Body.Close()

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	return nil
}

func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		switch k {
		case "Connection", "Proxy-Connection", "Keep-Alive", "Transfer-Encoding", "Te", "Trailer", "Upgrade":
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

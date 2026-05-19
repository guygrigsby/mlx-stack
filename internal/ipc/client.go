package ipc

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
)

type Client struct {
	sockPath string
	http     *http.Client
}

func New(sockPath string) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			d := net.Dialer{}
			return d.DialContext(ctx, "unix", sockPath)
		},
	}
	return &Client{
		sockPath: sockPath,
		http:     &http.Client{Transport: tr},
	}
}

func (c *Client) Get(ctx context.Context, path string) ([]byte, error) {
	return c.do(ctx, "GET", path, nil)
}

func (c *Client) PostJSON(ctx context.Context, path string, body []byte) ([]byte, error) {
	return c.do(ctx, "POST", path, body)
}

func (c *Client) do(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://mlxd"+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return b, fmt.Errorf("admin %s %s: %d: %s", method, path, resp.StatusCode, b)
	}
	return b, nil
}

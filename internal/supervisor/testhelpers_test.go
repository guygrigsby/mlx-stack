package supervisor

import (
	"net"
	"strconv"
	"strings"
)

func newListener() (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:0")
}

func freePort() (int, error) {
	l, err := newListener()
	if err != nil {
		return 0, err
	}
	defer l.Close()
	_, ps, _ := strings.Cut(l.Addr().String(), ":")
	p, _ := strconv.Atoi(ps)
	return p, nil
}

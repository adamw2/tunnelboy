package cli

import (
	"fmt"
	"strconv"
	"strings"
)

// endpointOverride is a target named on the command line rather than
// discovered. Discovery is a control-plane call in the account that owns the
// resource, so it cannot reach across an account boundary; forwarding a socket
// to a host can. Naming the endpoint is what lets a tunnel land on a resource
// in an account the caller holds no credentials for, with the signing proxy
// still signing as the caller against the real hostname.
type endpointOverride struct {
	Host string
	Port int32
	Name string
}

// parseEndpointOverride reads a host[:port] flag. defaultPort applies when the
// flag carries no port of its own; pass 0 for resources that have no sensible
// default, and the caller is required to supply one.
//
// name is the display label, taken from the command's positional argument when
// there is one; otherwise it is derived from the host.
func parseEndpointOverride(flag, name string, defaultPort int32) (*endpointOverride, error) {
	flag = strings.TrimSpace(flag)
	if flag == "" {
		return nil, nil
	}
	if strings.Contains(flag, "://") || strings.Contains(flag, "/") {
		return nil, fmt.Errorf("--endpoint takes host[:port], not a URL: %q", flag)
	}

	host := flag
	port := defaultPort

	if i := strings.LastIndexByte(flag, ':'); i >= 0 {
		host = flag[:i]
		parsed, err := strconv.Atoi(flag[i+1:])
		if err != nil || parsed < 1 || parsed > 65535 {
			return nil, fmt.Errorf("--endpoint port must be a number from 1 to 65535: %q", flag)
		}
		port = int32(parsed)
	}

	if host == "" {
		return nil, fmt.Errorf("--endpoint needs a host: %q", flag)
	}
	if port == 0 {
		return nil, fmt.Errorf("--endpoint needs a port for this resource, as host:port")
	}
	if name == "" {
		name = endpointLabel(host)
	}

	return &endpointOverride{Host: host, Port: port, Name: name}, nil
}

// endpointLabel is the first DNS label of a host, the closest thing an endpoint
// carries to the name a discovered resource would have had.
func endpointLabel(host string) string {
	if i := strings.IndexByte(host, '.'); i > 0 {
		return host[:i]
	}
	return host
}

// firstArg is the positional argument a connect command was given, if any.
func firstArg(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return ""
}

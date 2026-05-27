package tunnel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

// OpenSearchProxy is a local HTTP proxy that signs requests with AWS SigV4
type OpenSearchProxy struct {
	cfg            aws.Config
	region         string
	profileName    string
	endpoint       string // Target to forward to
	domainEndpoint string // Domain for signing (if different from endpoint)
	localPort      int
	useTunnel      bool
	server         *http.Server
	done           chan struct{}
}

// OpenSearchProxyConfig contains configuration for the proxy
type OpenSearchProxyConfig struct {
	AWSConfig      aws.Config
	Region         string
	ProfileName    string // AWS profile name to use
	Endpoint       string // Target endpoint (can be localhost:port for tunnel or domain for direct)
	DomainEndpoint string // Actual OpenSearch domain endpoint (for signing, optional)
	LocalPort      int
	UseTunnel      bool // If true, endpoint is a local tunnel
}

// NewOpenSearchProxy creates a new signing proxy for OpenSearch
func NewOpenSearchProxy(cfg OpenSearchProxyConfig) (*OpenSearchProxy, error) {
	domainEndpoint := cfg.DomainEndpoint
	if domainEndpoint == "" {
		domainEndpoint = cfg.Endpoint
	}
	
	return &OpenSearchProxy{
		cfg:            cfg.AWSConfig,
		region:         cfg.Region,
		profileName:    cfg.ProfileName,
		endpoint:       cfg.Endpoint,
		domainEndpoint: domainEndpoint,
		localPort:      cfg.LocalPort,
		useTunnel:      cfg.UseTunnel,
		done:           make(chan struct{}),
	}, nil
}

// Start starts the proxy server
func (p *OpenSearchProxy) Start(ctx context.Context) error {
	// Configure TLS settings. When tunneling, the connection terminates at
	// localhost (the SSM port-forward) so the cert can't match the hostname;
	// the underlying traffic is already encrypted inside the SSM session and
	// SNI is overridden to the real domain below.
	tlsConfig := &tls.Config{
		InsecureSkipVerify: p.useTunnel, // #nosec G402 -- localhost endpoint of an SSM-encrypted tunnel
	}
	
	// If using tunnel, override SNI to use the domain name
	if p.useTunnel {
		tlsConfig.ServerName = p.domainEndpoint
	}

	// Create base transport with TLS config
	baseTransport := &http.Transport{
		TLSClientConfig:       tlsConfig,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	// Create AWS SigV4 signer
	signer := v4.NewSigner()

	// Create a custom transport that signs requests
	signingTransport := &signingRoundTripper{
		transport: baseTransport,
		signer:    signer,
		cfg:       p.cfg,
		region:    p.region,
		service:   "es",
		domain:    p.domainEndpoint,
	}

	// Create target URL for the reverse proxy
	targetURL := &url.URL{
		Scheme: "https",
		Host:   p.endpoint, // Will be localhost:port for tunnel, or domain for direct
	}

	// Create reverse proxy
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.Transport = signingTransport

	// Customize the director to set the correct Host header for signing
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		// Set the Host header to the real domain for signing
		req.Host = p.domainEndpoint
		req.Header.Set("Host", p.domainEndpoint)
	}

	// Wrap proxy with CORS handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Handle CORS preflight
		if r.Method == "OPTIONS" {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "*")
			w.WriteHeader(http.StatusOK)
			return
		}

		// Add CORS headers to response
		w.Header().Set("Access-Control-Allow-Origin", "*")

		// Forward to proxy
		proxy.ServeHTTP(w, r)
	})

	p.server = &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", p.localPort),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second, // bound slow-header (Slowloris) clients
	}

	// Start server in goroutine
	errChan := make(chan error, 1)
	go func() {
		if err := p.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errChan <- err
		}
		close(p.done)
	}()

	// Wait for server to be ready with health check
	maxRetries := 10
	for i := 0; i < maxRetries; i++ {
		select {
		case err := <-errChan:
			return fmt.Errorf("proxy server failed to start: %w", err)
		default:
			// Try to connect to the server
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", p.localPort), 100*time.Millisecond)
			if err == nil {
				conn.Close()
				return nil // Server is ready
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	return fmt.Errorf("proxy server did not start within timeout")
}

// Stop stops the proxy server
func (p *OpenSearchProxy) Stop() error {
	if p.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return p.server.Shutdown(ctx)
	}
	return nil
}

// Done returns a channel that's closed when the proxy stops
func (p *OpenSearchProxy) Done() <-chan struct{} {
	return p.done
}

// LocalURL returns the local URL for the proxy
func (p *OpenSearchProxy) LocalURL() string {
	return fmt.Sprintf("http://localhost:%d", p.localPort)
}

// KibanaURL returns the Kibana dashboards URL
func (p *OpenSearchProxy) KibanaURL() string {
	return fmt.Sprintf("http://localhost:%d/_dashboards", p.localPort)
}

// signingRoundTripper is a custom RoundTripper that signs requests with AWS SigV4
type signingRoundTripper struct {
	transport http.RoundTripper
	signer    *v4.Signer
	cfg       aws.Config
	region    string
	service   string
	domain    string
}

// RoundTrip executes a single HTTP transaction, signing the request with SigV4
func (s *signingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Get credentials from the AWS config
	creds, err := s.cfg.Credentials.Retrieve(req.Context())
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve credentials: %w", err)
	}

	// Read and buffer the body for signing
	var bodyBytes []byte
	if req.Body != nil {
		bodyBytes, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read request body: %w", err)
		}
		req.Body.Close()
	}

	// Save original headers
	originalHeaders := make(http.Header)
	for name, values := range req.Header {
		for _, value := range values {
			originalHeaders.Add(name, value)
		}
	}

	// Strip out browser headers that shouldn't be signed
	// Keep only essential headers for signing
	essentialHeaders := make(http.Header)
	essentialHeaders.Set("Host", req.Host)
	
	// Copy only Content-Type and X-Amz-* headers for signing
	for name, values := range req.Header {
		if name == "Content-Type" || isAmzHeader(name) {
			for _, value := range values {
				essentialHeaders.Add(name, value)
			}
		}
	}
	
	// Replace request headers with cleaned set for signing
	req.Header = essentialHeaders

	// Sign the request
	payloadHash := s.createPayloadHash(bodyBytes)
	err = s.signer.SignHTTP(req.Context(), creds, req, payloadHash, s.service, s.region, time.Now())
	if err != nil {
		return nil, fmt.Errorf("failed to sign request: %w", err)
	}

	// Restore original headers plus AWS signature headers
	// Copy AWS signature headers from signed request
	authHeader := req.Header.Get("Authorization")
	amzDate := req.Header.Get("X-Amz-Date")
	amzSecurityToken := req.Header.Get("X-Amz-Security-Token")
	
	// Start with original headers
	req.Header = originalHeaders
	
	// Add AWS signature headers
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("X-Amz-Date", amzDate)
	if amzSecurityToken != "" {
		req.Header.Set("X-Amz-Security-Token", amzSecurityToken)
	}

	// Restore the body for the actual request
	if len(bodyBytes) > 0 {
		req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		req.ContentLength = int64(len(bodyBytes))
	}

	// Execute the request
	return s.transport.RoundTrip(req)
}

// isAmzHeader checks if a header is an AWS-specific header
func isAmzHeader(name string) bool {
	lowerName := strings.ToLower(name)
	return strings.HasPrefix(lowerName, "x-amz-")
}

// createPayloadHash creates the payload hash for signing
func (s *signingRoundTripper) createPayloadHash(body []byte) string {
	hash := sha256.Sum256(body)
	return hex.EncodeToString(hash[:])
}

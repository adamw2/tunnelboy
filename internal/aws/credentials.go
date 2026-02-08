package aws

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/feature/rds/auth"
)

// GenerateRDSAuthToken generates an IAM authentication token for RDS
func GenerateRDSAuthToken(ctx context.Context, cfg aws.Config, endpoint string, port int, region, dbUser string) (string, error) {
	authEndpoint := fmt.Sprintf("%s:%d", endpoint, port)
	
	token, err := auth.BuildAuthToken(ctx, authEndpoint, region, dbUser, cfg.Credentials)
	if err != nil {
		return "", fmt.Errorf("failed to generate RDS auth token: %w", err)
	}

	return token, nil
}

// SignRequest signs an HTTP request with AWS SigV4 for OpenSearch
func SignRequest(ctx context.Context, cfg aws.Config, req *http.Request, service, region string) error {
	// Read body for signing
	var body []byte
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(req.Body)
		if err != nil {
			return fmt.Errorf("failed to read request body: %w", err)
		}
		req.Body = io.NopCloser(io.NewSectionReader(
			&bytesReader{body},
			0,
			int64(len(body)),
		))
	}

	// Calculate payload hash
	hash := sha256.Sum256(body)
	payloadHash := hex.EncodeToString(hash[:])

	// Get credentials
	creds, err := cfg.Credentials.Retrieve(ctx)
	if err != nil {
		return fmt.Errorf("failed to retrieve credentials: %w", err)
	}

	// Sign the request
	signer := v4.NewSigner()
	err = signer.SignHTTP(ctx, creds, req, payloadHash, service, region, time.Now())
	if err != nil {
		return fmt.Errorf("failed to sign request: %w", err)
	}

	return nil
}

// bytesReader implements io.ReaderAt for a byte slice
type bytesReader struct {
	data []byte
}

func (r *bytesReader) ReadAt(p []byte, off int64) (n int, err error) {
	if off >= int64(len(r.data)) {
		return 0, io.EOF
	}
	n = copy(p, r.data[off:])
	return n, nil
}

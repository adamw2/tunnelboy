package aws

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsCredentialError(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("operation error SSM: StartSession, https response error StatusCode: 400, ExpiredTokenException: The security token included in the request is expired"), true},
		{errors.New("failed to refresh cached credentials, the SSO session has expired or is invalid"), true},
		{fmt.Errorf("wrapped: %w", errors.New("InvalidClientTokenId: The security token included in the request is invalid")), true},
		{errors.New("operation error EC2: DescribeInstances, exceeded maximum number of attempts"), false},
		{errors.New("no RDS instances found"), false},
		{errors.New("dial tcp: lookup ssm.us-east-1.amazonaws.com: no such host"), false},
	}

	for _, c := range cases {
		if got := IsCredentialError(c.err); got != c.want {
			t.Errorf("IsCredentialError(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

package providers

import (
	"net/http/httptest"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
)

// Give the identity SDK a transport that trusts this test server only. Unlike
// SSL_CERT_FILE, this also works on Windows and doesn't depend on root caching.
func useAzureTestAuthority(t *testing.T, authority *httptest.Server) {
	t.Helper()
	previous := newAzureClientSecretCredential
	t.Cleanup(func() { newAzureClientSecretCredential = previous })
	newAzureClientSecretCredential = func(tenant, client, secret string, options *azidentity.ClientSecretCredentialOptions) (*azidentity.ClientSecretCredential, error) {
		var opts azidentity.ClientSecretCredentialOptions
		if options != nil {
			opts = *options
		}
		opts.Transport = authority.Client()
		return azidentity.NewClientSecretCredential(tenant, client, secret, &opts)
	}
}

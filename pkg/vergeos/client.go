package vergeos

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	vergeos "github.com/verge-io/govergeos"
	"k8s.io/klog/v2"
)

const (
	envHost      = "VERGEOS_HOST"
	envAPIKey    = "VERGEOS_API_KEY"
	envUsername  = "VERGEOS_USERNAME"
	envPassword  = "VERGEOS_PASSWORD"
	envVerifySSL = "VERGEOS_VERIFY_SSL"
	envTimeout   = "VERGEOS_TIMEOUT"
	envCAFile    = "VERGEOS_CA_FILE"

	defaultClientTimeout = 30 * time.Second
)

// verifySSLDisabled mirrors govergeos' VERGEOS_VERIFY_SSL parsing.
func verifySSLDisabled() bool {
	v := os.Getenv(envVerifySSL)
	return strings.EqualFold(v, "false") || v == "0"
}

// newVergeOSClient builds the API client from the environment (VERGEOS_*) and
// cfg. It fails fast with an error naming the missing variable.
func newVergeOSClient(cfg *cloudConfig) (*vergeos.Client, error) {
	if os.Getenv(envHost) == "" {
		return nil, fmt.Errorf("%s is not set: the VergeOS API URL is required", envHost)
	}
	hasBasic := os.Getenv(envUsername) != "" && os.Getenv(envPassword) != ""
	if !hasBasic && os.Getenv(envAPIKey) == "" {
		return nil, fmt.Errorf("no VergeOS credentials: set %s (or %s and %s)", envAPIKey, envUsername, envPassword)
	}

	opts := []vergeos.ClientOption{vergeos.WithEnvConfig()}

	caFile := cfg.CAFile
	if caFile == "" {
		caFile = os.Getenv(envCAFile)
	}
	switch {
	case verifySSLDisabled():
		klog.Warningf("%s is disabled: TLS certificates are NOT verified, so the VergeOS API key can be intercepted. Configure a CA (caFile / %s) and set %s=true.", envVerifySSL, envCAFile, envVerifySSL)
		if caFile != "" {
			klog.Warningf("CA file %q is ignored because %s is disabled", caFile, envVerifySSL)
		}
	case caFile != "":
		hc, err := httpClientWithCA(caFile)
		if err != nil {
			return nil, err
		}
		opts = append(opts, vergeos.WithHTTPClient(hc))
	}

	client, err := vergeos.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("creating VergeOS client: %w", err)
	}
	return client, nil
}

// httpClientWithCA returns an HTTP client that verifies the server against the
// system roots plus the PEM certificates in caFile.
func httpClientWithCA(caFile string) (*http.Client, error) {
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("reading CA file: %w", err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("CA file %q contains no valid PEM certificates", caFile)
	}

	timeout := defaultClientTimeout
	if v := os.Getenv(envTimeout); v != "" {
		secs, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("invalid %s value %q: %w", envTimeout, v, err)
		}
		timeout = time.Duration(secs) * time.Second
	}

	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig:     &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 20,
			IdleConnTimeout:     90 * time.Second,
		},
	}, nil
}

// VMClient abstracts the govergeos VMs API for testing.
type VMClient interface {
	Get(ctx context.Context, id int) (*vergeos.VM, error)
	List(ctx context.Context, opts ...vergeos.ListOption) ([]vergeos.VM, error)
}

// MachineStatusClient abstracts the govergeos MachineStatus API for testing.
type MachineStatusClient interface {
	Get(ctx context.Context, machineKey int) (*vergeos.MachineStatus, error)
}

// VMNICClient abstracts the govergeos VMNICs API for testing.
// List takes the VM's Machine ID (not its $key).
type VMNICClient interface {
	List(ctx context.Context, machineID int) ([]vergeos.VMNIC, error)
}

// VNetAddressClient abstracts the govergeos VNetAddresses API for testing.
type VNetAddressClient interface {
	GetByIP(ctx context.Context, vnetID int, ip string) (*vergeos.VNetAddress, error)
	ListByNetwork(ctx context.Context, vnetID int, opts ...vergeos.ListOption) ([]vergeos.VNetAddress, error)
	Create(ctx context.Context, req *vergeos.VNetAddressCreateRequest) (*vergeos.VNetAddress, error)
	Update(ctx context.Context, id int, req *vergeos.VNetAddressUpdateRequest) (*vergeos.VNetAddress, error)
	Delete(ctx context.Context, id int) error
}

// VNetRuleClient abstracts the govergeos VNetRules API for testing.
type VNetRuleClient interface {
	ListByNetwork(ctx context.Context, vnetID int, opts ...vergeos.ListOption) ([]vergeos.VNetRule, error)
	GetByName(ctx context.Context, vnetID int, name string) (*vergeos.VNetRule, error)
	Create(ctx context.Context, req *vergeos.VNetRuleCreateRequest) (*vergeos.VNetRule, error)
	Update(ctx context.Context, id int, req *vergeos.VNetRuleUpdateRequest) (*vergeos.VNetRule, error)
	Delete(ctx context.Context, id int) error
}

// NetworkClient abstracts the govergeos Networks API for testing.
type NetworkClient interface {
	Get(ctx context.Context, id int) (*vergeos.Network, error)
	ApplyRules(ctx context.Context, id int) error
}

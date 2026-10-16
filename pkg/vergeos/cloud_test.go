package vergeos

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	cloudprovider "k8s.io/cloud-provider"
)

// setEnv sets the VERGEOS_* client environment for one test. Empty value = unset.
func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for _, k := range []string{envHost, envAPIKey, envUsername, envPassword, envVerifySSL, envTimeout, envCAFile} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	for k, v := range kv {
		if v != "" {
			t.Setenv(k, v)
		}
	}
}

// useValidEnv points newCloud at a local fake VergeOS API with an API key.
// (govergeos.NewClient does a live /version.json check.)
func useValidEnv(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(&vergeosFake{})
	t.Cleanup(srv.Close)
	setEnv(t, map[string]string{envHost: srv.URL, envAPIKey: "k"})
}

func TestNewCloud_Errors(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		config  string
		wantErr string
	}{
		{"no env at all", nil, `{}`, envHost},
		{"host but no credentials (chart omits API key when empty)", map[string]string{envHost: "https://v"}, `{}`, envAPIKey},
		{"username without password", map[string]string{envHost: "https://v", envUsername: "u"}, `{}`, envAPIKey},
		{"api key but no host", map[string]string{envAPIKey: "k"}, `{}`, envHost},
		{"invalid timeout", map[string]string{envHost: "https://v", envAPIKey: "k", envTimeout: "abc"}, `{}`, envTimeout},
		{"malformed config JSON", map[string]string{envHost: "https://v", envAPIKey: "k"}, `{`, "parsing cloud config"},
		{"negative nodeNetworkID", map[string]string{envHost: "https://v", envAPIKey: "k"}, `{"nodeNetworkID":-1}`, "nodeNetworkID"},
		{"LB enabled without networkID", map[string]string{envHost: "https://v", envAPIKey: "k"}, `{"loadBalancer":{"enabled":true,"ipPool":["10.0.0.1"]}}`, "networkID"},
		{"API unreachable at startup", map[string]string{envHost: "http://127.0.0.1:1", envAPIKey: "k"}, `{}`, "creating VergeOS client"},
		{"missing CA file", map[string]string{envHost: "https://v", envAPIKey: "k"}, `{"caFile":"/nonexistent/ca.pem"}`, "CA file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEnv(t, tt.env)
			c, err := newCloud(strings.NewReader(tt.config))
			if err == nil {
				t.Fatalf("expected error containing %q, got cloud %v", tt.wantErr, c)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not mention %q", err, tt.wantErr)
			}
			if c != nil {
				t.Errorf("expected nil cloud on error")
			}
		})
	}
}

func TestNewCloud_InvalidCAFileContents(t *testing.T) {
	setEnv(t, map[string]string{envHost: "https://v", envAPIKey: "k"})
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := newCloud(strings.NewReader(`{"caFile":"` + path + `"}`))
	if err == nil || !strings.Contains(err.Error(), "no valid PEM") {
		t.Fatalf("expected no-valid-PEM error, got %v", err)
	}
}

func TestNewCloud_NilConfigWithEnv(t *testing.T) {
	useValidEnv(t)
	c, err := newCloud(nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.instances == nil || c.instances.vms == nil || c.instances.nics == nil || c.instances.machineStatus == nil {
		t.Errorf("instances must have all clients wired: %+v", c.instances)
	}
}

func TestNewCloud_WiresClientsAndConfig(t *testing.T) {
	useValidEnv(t)
	c, err := newCloud(strings.NewReader(`{"nodeNetworkID":7,"loadBalancer":{"enabled":true,"networkID":5,"ipPool":["10.0.0.1"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.instances.nodeNetworkID != 7 {
		t.Errorf("nodeNetworkID = %d, want 7", c.instances.nodeNetworkID)
	}
	if c.lb == nil || len(c.lb.workers) != 1 {
		t.Fatalf("expected one load balancer worker, got %+v", c.lb)
	}
	if w := c.lb.def; w.rules == nil || w.network == nil || w.addrs == nil || w.nodes == nil {
		t.Errorf("load balancer must have all clients wired: %+v", w)
	}
}

// vergeosFake is a minimal TLS VergeOS API: one VM with one NIC lease.
type vergeosFake struct {
	mu      sync.Mutex
	auth    []string
	queries []string
}

func (f *vergeosFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.queries = append(f.queries, r.URL.Path+"?"+r.URL.Query().Get("filter"))
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/version.json":
		_, _ = w.Write([]byte(`{"version":"26.0.0"}`))
	case "/api/v4/vms":
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"$key": 42, "uuid": testUUID, "name": "node-1", "machine": 71, "cpu_cores": 4, "ram": 8192},
		})
	case "/api/v4/machine_nics":
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"$key": 3, "machine": 71, "vnet": 10, "macaddress": "52:54:00:aa:bb:cc", "ipaddress": "192.0.2.50"},
			{"$key": 4, "machine": 71, "vnet": 99, "macaddress": "52:54:00:aa:bb:dd", "ipaddress": "10.99.0.5"},
		})
	default:
		http.NotFound(w, r)
	}
}

func writeServerCA(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.pem")
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func nodeNamed(name string) *v1.Node {
	return &v1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

// Wiring test: env -> newCloud -> real govergeos client -> HTTPS (private CA)
// -> InstanceMetadata, asserting auth, filter strings and node addresses.
func TestNewCloud_PrivateCA_InstanceMetadataRoundTrip(t *testing.T) {
	fake := &vergeosFake{}
	srv := httptest.NewTLSServer(fake)
	defer srv.Close()
	caFile := writeServerCA(t, srv)

	setEnv(t, map[string]string{envHost: srv.URL, envAPIKey: "secret-key", envVerifySSL: "true"})
	c, err := newCloud(strings.NewReader(`{"nodeNetworkID":10,"caFile":"` + caFile + `"}`))
	if err != nil {
		t.Fatal(err)
	}

	meta, err := c.instances.InstanceMetadata(context.Background(), nodeNamed("node-1"))
	if err != nil {
		t.Fatalf("InstanceMetadata over private-CA TLS: %v", err)
	}
	want := []v1.NodeAddress{
		{Type: v1.NodeInternalIP, Address: "192.0.2.50"},
		{Type: v1.NodeHostName, Address: "node-1"},
	}
	if len(meta.NodeAddresses) != 2 || meta.NodeAddresses[0] != want[0] || meta.NodeAddresses[1] != want[1] {
		t.Errorf("addresses = %v, want %v (vnet 99 NIC must be dropped)", meta.NodeAddresses, want)
	}
	if meta.ProviderID != "vergeos://"+testUUID {
		t.Errorf("providerID = %s", meta.ProviderID)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, a := range fake.auth {
		if a != "Bearer secret-key" {
			t.Errorf("Authorization = %q", a)
		}
	}
	joined := strings.Join(fake.queries, "\n")
	for _, w := range []string{"/api/v4/vms?name eq 'node-1' and is_snapshot eq false", "/api/v4/machine_nics?machine eq 71"} {
		if !strings.Contains(joined, w) {
			t.Errorf("expected request %q, got:\n%s", w, joined)
		}
	}
}

func TestNewCloud_PrivateCA_ConfigViaEnvVar(t *testing.T) {
	srv := httptest.NewTLSServer(&vergeosFake{})
	defer srv.Close()
	setEnv(t, map[string]string{envHost: srv.URL, envAPIKey: "k", envCAFile: writeServerCA(t, srv)})
	c, err := newCloud(strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.instances.InstanceMetadata(context.Background(), nodeNamed("node-1")); err != nil {
		t.Fatalf("VERGEOS_CA_FILE not honoured: %v", err)
	}
}

func TestNewCloud_UntrustedCA_FailsFastOnVerification(t *testing.T) {
	srv := httptest.NewTLSServer(&vergeosFake{})
	defer srv.Close()
	setEnv(t, map[string]string{envHost: srv.URL, envAPIKey: "k", envVerifySSL: "true"})
	_, err := newCloud(strings.NewReader(`{}`))
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("expected certificate verification error without the CA configured, got %v", err)
	}
}

func TestNewCloud_VerifySSLFalse_TrustsSelfSigned(t *testing.T) {
	srv := httptest.NewTLSServer(&vergeosFake{})
	defer srv.Close()
	setEnv(t, map[string]string{envHost: srv.URL, envAPIKey: "k", envVerifySSL: "false"})
	c, err := newCloud(strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.instances.InstanceMetadata(context.Background(), nodeNamed("node-1")); err != nil {
		t.Fatalf("verifySSL=false should accept self-signed cert: %v", err)
	}
}

func TestCloudProviderName(t *testing.T) {
	useValidEnv(t)
	cfg := `{}`
	c, err := newCloud(strings.NewReader(cfg))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.ProviderName() != ProviderName {
		t.Errorf("expected provider name %q, got %q", ProviderName, c.ProviderName())
	}
}

func TestCloudImplementsInterface(t *testing.T) {
	useValidEnv(t)
	cfg := `{}`
	c, err := newCloud(strings.NewReader(cfg))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Verify cloud implements cloudprovider.Interface at compile time.
	var _ cloudprovider.Interface = c
}

func TestCloudInstancesV2(t *testing.T) {
	useValidEnv(t)
	cfg := `{}`
	c, err := newCloud(strings.NewReader(cfg))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	inst, ok := c.InstancesV2()
	if !ok {
		t.Error("expected InstancesV2 to be supported")
	}
	if inst == nil {
		t.Error("expected InstancesV2 to be non-nil")
	}
}

func TestCloudLoadBalancer_Enabled(t *testing.T) {
	useValidEnv(t)
	cfg := `{"loadBalancer":{"enabled":true,"networkID":5,"ipPool":["10.0.0.1"]}}`
	c, err := newCloud(strings.NewReader(cfg))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	lb, ok := c.LoadBalancer()
	if !ok {
		t.Error("expected LoadBalancer to be supported when enabled")
	}
	if lb == nil {
		t.Error("expected LoadBalancer to be non-nil")
	}
}

func TestCloudLoadBalancer_Disabled(t *testing.T) {
	useValidEnv(t)
	cfg := `{}`
	c, err := newCloud(strings.NewReader(cfg))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	lb, ok := c.LoadBalancer()
	if ok {
		t.Error("expected LoadBalancer to not be supported when disabled")
	}
	if lb != nil {
		t.Error("expected LoadBalancer to be nil when disabled")
	}
}

func TestCloudUnsupportedInterfaces(t *testing.T) {
	useValidEnv(t)
	cfg := `{}`
	c, err := newCloud(strings.NewReader(cfg))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := c.Instances(); ok {
		t.Error("Instances should not be supported (deprecated)")
	}
	if _, ok := c.Zones(); ok {
		t.Error("Zones should not be supported (deprecated)")
	}
	if _, ok := c.Routes(); ok {
		t.Error("Routes should not be supported")
	}
	if _, ok := c.Clusters(); ok {
		t.Error("Clusters should not be supported")
	}
}

// The same path main.go's cloudInitializer takes: registry lookup + config file.
func TestRegisteredProvider_InitCloudProvider(t *testing.T) {
	cfgFile := filepath.Join(t.TempDir(), "cloud-config.json")
	if err := os.WriteFile(cfgFile, []byte(`{"nodeNetworkID":3}`), 0o600); err != nil {
		t.Fatal(err)
	}

	setEnv(t, nil)
	if p, err := cloudprovider.InitCloudProvider(ProviderName, cfgFile); err == nil {
		t.Fatalf("expected error (CCM must exit non-zero) with no VERGEOS_* env, got provider %v", p)
	} else if !strings.Contains(err.Error(), envHost) {
		t.Errorf("error should name %s: %v", envHost, err)
	}

	useValidEnv(t)
	p, err := cloudprovider.InitCloudProvider(ProviderName, cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || p.ProviderName() != ProviderName {
		t.Errorf("unexpected provider %v", p)
	}
}

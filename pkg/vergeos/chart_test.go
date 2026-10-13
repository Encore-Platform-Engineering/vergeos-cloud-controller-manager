package vergeos

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

const chartDir = "../../charts/vergeos-cloud-controller-manager"

type renderedDoc struct {
	Kind string            `json:"kind"`
	Data map[string]string `json:"data"`
	Spec struct {
		Template struct {
			Spec struct {
				Containers []struct {
					VolumeMounts []struct {
						Name      string `json:"name"`
						MountPath string `json:"mountPath"`
					} `json:"volumeMounts"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

// renderChart runs `helm template` and returns the parsed documents. Without
// helm the test is skipped, unless REQUIRE_HELM=1 (set in CI).
func renderChart(t *testing.T, args ...string) []renderedDoc {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		if os.Getenv("REQUIRE_HELM") == "1" {
			t.Fatal("helm not found but REQUIRE_HELM=1")
		}
		t.Skip("helm not installed")
	}
	out, err := exec.Command(helm, append([]string{"template", "rel", chartDir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template %v: %v\n%s", args, err, out)
	}
	var docs []renderedDoc
	for _, part := range strings.Split(string(out), "\n---\n") {
		var d renderedDoc
		if err := yaml.Unmarshal([]byte(part), &d); err != nil {
			t.Fatalf("parsing rendered manifest: %v\n%s", err, part)
		}
		docs = append(docs, d)
	}
	return docs
}

func kind(docs []renderedDoc, k string) *renderedDoc {
	for i := range docs {
		if docs[i].Kind == k {
			return &docs[i]
		}
	}
	return nil
}

// The rendered cloud-config.json must be accepted by parseConfig, for the
// value combinations users actually deploy.
func TestChart_CloudConfigParses(t *testing.T) {
	lb := []string{"--set", "vergeos.host=https://v", "--set", "vergeos.apiKey=k",
		"--set", "loadBalancer.enabled=true", "--set", "loadBalancer.networkID=5", "--set", "loadBalancer.ipPool={10.0.0.1,10.0.0.2}"}
	tests := []struct {
		name  string
		args  []string
		check func(t *testing.T, cfg *cloudConfig)
	}{
		{"defaults", nil, func(t *testing.T, cfg *cloudConfig) {
			if cfg.LoadBalancer.Enabled {
				t.Error("LB must be off by default")
			}
		}},
		{"lb enabled", lb, func(t *testing.T, cfg *cloudConfig) {
			if !cfg.LoadBalancer.Enabled || cfg.LoadBalancer.NetworkID != 5 || len(cfg.LoadBalancer.IPPool) != 2 {
				t.Errorf("lb config = %+v", cfg.LoadBalancer)
			}
		}},
		{"lb disabled", []string{"--set", "loadBalancer.enabled=false"}, func(t *testing.T, cfg *cloudConfig) {
			if cfg.LoadBalancer.Enabled {
				t.Error("lb should be disabled")
			}
		}},
		{"nodeNetworkID", append([]string{"--set", "nodeNetworkID=12"}, lb...), func(t *testing.T, cfg *cloudConfig) {
			if cfg.NodeNetworkID != 12 {
				t.Errorf("nodeNetworkID = %d", cfg.NodeNetworkID)
			}
		}},
		{"caBundle", append([]string{"--set", "vergeos.caBundle=-----BEGIN CERTIFICATE-----"}, lb...), func(t *testing.T, cfg *cloudConfig) {
			if cfg.CAFile != "/etc/vergeos/ca.crt" {
				t.Errorf("caFile = %q", cfg.CAFile)
			}
		}},
		{"caSecret", append([]string{"--set", "vergeos.caSecret=my-ca"}, lb...), func(t *testing.T, cfg *cloudConfig) {
			if cfg.CAFile != "/etc/vergeos/ca.crt" {
				t.Errorf("caFile = %q", cfg.CAFile)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			docs := renderChart(t, tt.args...)
			cm := kind(docs, "ConfigMap")
			if cm == nil {
				t.Fatal("no ConfigMap rendered")
			}
			cfg, err := parseConfig(strings.NewReader(cm.Data["cloud-config.json"]))
			if err != nil {
				t.Fatalf("parseConfig(rendered cloud-config.json): %v\n%s", err, cm.Data["cloud-config.json"])
			}
			tt.check(t, cfg)
		})
	}
}

func TestChart_CAMount(t *testing.T) {
	caMount := func(docs []renderedDoc) bool {
		d := kind(docs, "Deployment")
		if d == nil || len(d.Spec.Template.Spec.Containers) == 0 {
			t.Fatal("no Deployment/container rendered")
		}
		for _, m := range d.Spec.Template.Spec.Containers[0].VolumeMounts {
			if m.MountPath == "/etc/vergeos/ca.crt" {
				return true
			}
		}
		return false
	}
	base := []string{"--set", "loadBalancer.enabled=false"}
	if caMount(renderChart(t, base...)) {
		t.Error("CA must not be mounted by default")
	}
	if !caMount(renderChart(t, append(base, "--set", "vergeos.caSecret=my-ca")...)) {
		t.Error("caSecret must mount ca.crt")
	}
	docs := renderChart(t, append(base, "--set", "vergeos.caBundle=pem")...)
	if !caMount(docs) {
		t.Error("caBundle must mount ca.crt")
	}
	if cm := kind(docs, "ConfigMap"); cm == nil || cm.Data["ca.crt"] == "" {
		t.Error("caBundle must be rendered into the ConfigMap as ca.crt")
	}
}

package charts_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

const chartDir = "vergeos-cloud-controller-manager"

type rendered struct {
	deployment   appsv1.Deployment
	configMap    corev1.ConfigMap
	clusterRole  rbacv1.ClusterRole
	role         rbacv1.Role
	roleBinding  rbacv1.RoleBinding
	cloudConfig  map[string]any
	appVersion   string
	allManifests []map[string]any
}

func helmTemplate(t *testing.T, args ...string) (*rendered, error) {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not installed")
	}
	cmd := exec.Command("helm", append([]string{"template", "vergeos-ccm", chartDir, "--namespace", "kube-system"}, args...)...)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return nil, errors.New(stderr.String())
	}

	chartYAML, err := os.ReadFile(filepath.Join(chartDir, "Chart.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var chart struct {
		AppVersion string `json:"appVersion"`
	}
	if err := utilyaml.Unmarshal(chartYAML, &chart); err != nil {
		t.Fatal(err)
	}
	r := &rendered{appVersion: chart.AppVersion}
	dec := utilyaml.NewYAMLOrJSONDecoder(&out, 4096)
	for {
		var raw map[string]any
		if err := dec.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("decode: %v", err)
		}
		if raw == nil {
			continue
		}
		r.allManifests = append(r.allManifests, raw)
		var target any
		switch raw["kind"] {
		case "Deployment":
			target = &r.deployment
		case "ConfigMap":
			target = &r.configMap
		case "ClusterRole":
			target = &r.clusterRole
		case "Role":
			target = &r.role
		case "RoleBinding":
			target = &r.roleBinding
		default:
			continue
		}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, target); err != nil {
			t.Fatalf("convert %v: %v", raw["kind"], err)
		}
	}
	if err := json.Unmarshal([]byte(r.configMap.Data["cloud-config.json"]), &r.cloudConfig); err != nil {
		t.Fatalf("cloud-config.json is not valid JSON: %v", err)
	}
	return r, nil
}

func mustRender(t *testing.T, args ...string) *rendered {
	t.Helper()
	r, err := helmTemplate(t, args...)
	if err != nil {
		t.Fatalf("helm template failed: %v", err)
	}
	return r
}

// #9
func TestDefaultsRenderStartableConfigAndPullableImage(t *testing.T) {
	r := mustRender(t)
	lb := r.cloudConfig["loadBalancer"].(map[string]any)
	if lb["enabled"] != false {
		t.Errorf("loadBalancer must be disabled by default, got %v", lb["enabled"])
	}
	img := r.deployment.Spec.Template.Spec.Containers[0].Image
	if want := "ghcr.io/encore-platform-engineering/vergeos-cloud-controller-manager:v" + r.appVersion; img != want {
		t.Errorf("image = %q, want %q (CI pushes v* tags)", img, want)
	}
}

func TestImageTagOverride(t *testing.T) {
	r := mustRender(t, "--set", "image.tag=v9.9.9")
	if img := r.deployment.Spec.Template.Spec.Containers[0].Image; !strings.HasSuffix(img, ":v9.9.9") {
		t.Errorf("image = %q", img)
	}
}

func TestLoadBalancerEnabledRequiresNetworkAndPool(t *testing.T) {
	if _, err := helmTemplate(t, "--set", "loadBalancer.enabled=true"); err == nil {
		t.Error("expected render failure without networkID and ipPool")
	}
	if _, err := helmTemplate(t, "--set", "loadBalancer.enabled=true", "--set", "loadBalancer.networkID=5"); err == nil {
		t.Error("expected render failure without ipPool")
	}
	if _, err := helmTemplate(t, "--set", "loadBalancer.enabled=true", "--set", "loadBalancer.ipPool[0]=10.0.0.5"); err == nil {
		t.Error("expected render failure without networkID")
	}
	r := mustRender(t, "--set", "loadBalancer.enabled=true", "--set", "loadBalancer.networkID=5", "--set", "loadBalancer.ipPool[0]=10.0.0.5")
	lb := r.cloudConfig["loadBalancer"].(map[string]any)
	if lb["enabled"] != true || lb["networkID"] != float64(5) {
		t.Errorf("unexpected lb config: %v", lb)
	}
}

// #10
func TestSchedulingWorksOnWorkerOnlyAndTaintedClusters(t *testing.T) {
	spec := mustRender(t).deployment.Spec.Template.Spec
	if len(spec.NodeSelector) != 0 {
		t.Errorf("nodeSelector must be empty, got %v", spec.NodeSelector)
	}
	if !spec.HostNetwork || spec.DNSPolicy != corev1.DNSClusterFirstWithHostNet {
		t.Errorf("want hostNetwork + ClusterFirstWithHostNet, got %v / %v", spec.HostNetwork, spec.DNSPolicy)
	}
	for _, key := range []string{
		"node-role.kubernetes.io/control-plane",
		"node-role.kubernetes.io/master",
		"node.cloudprovider.kubernetes.io/uninitialized",
		"node.kubernetes.io/not-ready",
		"node.kubernetes.io/unreachable",
	} {
		found := false
		for _, tol := range spec.Tolerations {
			if tol.Key == key && tol.Operator == corev1.TolerationOpExists && tol.Value == "" {
				found = true
			}
		}
		if !found {
			t.Errorf("missing operator Exists toleration for %s", key)
		}
	}
	// Tolerations for control-plane/master/not-ready/unreachable must cover NoExecute too (no effect set).
	for _, tol := range spec.Tolerations {
		if tol.Key != "node.cloudprovider.kubernetes.io/uninitialized" && tol.Effect != "" {
			t.Errorf("toleration %s should match all effects, has %s", tol.Key, tol.Effect)
		}
	}
}

func TestHostNetworkRolloutDoesNotSurge(t *testing.T) {
	d := mustRender(t).deployment
	ru := d.Spec.Strategy.RollingUpdate
	if ru == nil || ru.MaxSurge == nil || ru.MaxSurge.IntValue() != 0 {
		t.Errorf("hostNetwork rollout must not surge: %+v", d.Spec.Strategy)
	}
	d = mustRender(t, "--set", "hostNetwork=false", "--set", "dnsPolicy=ClusterFirst").deployment
	if d.Spec.Template.Spec.HostNetwork {
		t.Error("hostNetwork=false not honored")
	}
}

// #15
func TestRBACLeastPrivilege(t *testing.T) {
	r := mustRender(t)

	for _, rule := range r.clusterRole.Rules {
		for _, res := range rule.Resources {
			if strings.HasPrefix(res, "serviceaccounts") || res == "leases" || res == "endpoints" || res == "configmaps" {
				t.Errorf("ClusterRole must not grant cluster-wide %s", res)
			}
		}
		for _, res := range rule.Resources {
			if res == "nodes" || res == "services" {
				for _, v := range rule.Verbs {
					if v != "get" && v != "list" && v != "watch" {
						t.Errorf("ClusterRole grants write verb %q on %s", v, res)
					}
				}
			}
		}
	}

	if r.role.Namespace != "kube-system" || r.roleBinding.Namespace != "kube-system" {
		t.Errorf("Role/RoleBinding must live in kube-system, got %s / %s", r.role.Namespace, r.roleBinding.Namespace)
	}
	if r.roleBinding.Subjects[0].Namespace != "kube-system" || r.roleBinding.Subjects[0].Name != "vergeos-ccm" {
		t.Errorf("unexpected binding subject: %+v", r.roleBinding.Subjects[0])
	}

	wantSAs := map[string]bool{"node-controller": true, "service-controller": true, "route-controller": true}
	sawToken := false
	for _, rule := range r.role.Rules {
		for _, res := range rule.Resources {
			switch res {
			case "serviceaccounts/token":
				sawToken = true
				if len(rule.ResourceNames) != len(wantSAs) {
					t.Errorf("token create must be limited to %v, got %v", wantSAs, rule.ResourceNames)
				}
				for _, n := range rule.ResourceNames {
					if !wantSAs[n] {
						t.Errorf("token create allowed for unexpected SA %q", n)
					}
				}
			case "leases":
				for _, v := range rule.Verbs {
					if v == "delete" || v == "list" || v == "watch" {
						t.Errorf("leases verb %q not needed", v)
					}
				}
			}
		}
	}
	if !sawToken {
		t.Error("serviceaccounts/token create rule missing")
	}
}

// #16
func TestPodHardeningAndProbes(t *testing.T) {
	spec := mustRender(t).deployment.Spec.Template.Spec
	if spec.PriorityClassName != "system-cluster-critical" {
		t.Errorf("priorityClassName = %q", spec.PriorityClassName)
	}
	psc := spec.SecurityContext
	if psc == nil || psc.RunAsNonRoot == nil || !*psc.RunAsNonRoot || psc.SeccompProfile == nil || psc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("pod securityContext not hardened: %+v", psc)
	}
	c := spec.Containers[0]
	sc := c.SecurityContext
	if sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation ||
		sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem ||
		sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("container securityContext not hardened: %+v", sc)
	}
	for name, p := range map[string]*corev1.Probe{"liveness": c.LivenessProbe, "readiness": c.ReadinessProbe} {
		if p == nil || p.HTTPGet == nil || p.HTTPGet.Path != "/healthz" || p.HTTPGet.Scheme != corev1.URISchemeHTTPS {
			t.Errorf("%s probe must be HTTPS /healthz, got %+v", name, p)
		}
	}
	if !strings.Contains(strings.Join(c.Command, " "), "--secure-port=10258") {
		t.Errorf("secure port not set: %v", c.Command)
	}
}

func TestMultiReplicaPrefersSpreading(t *testing.T) {
	if a := mustRender(t).deployment.Spec.Template.Spec.Affinity; a != nil {
		t.Errorf("single replica should have no affinity, got %+v", a)
	}
	a := mustRender(t, "--set", "replicaCount=2").deployment.Spec.Template.Spec.Affinity
	if a == nil || a.PodAntiAffinity == nil || len(a.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution) != 1 {
		t.Errorf("replicas>1 should add preferred anti-affinity, got %+v", a)
	}
}

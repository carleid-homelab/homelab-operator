/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// liveConfig mirrors the shape of the cluster's cloudflared ConfigMap: keys the
// operator does not manage, one hostname rule, and the catch-all last.
const liveConfig = `tunnel: vps-cluster-tunnel
metrics: 0.0.0.0:2000
no-autoupdate: true
ingress:
- hostname: grafana.carleid.dev
  service: http://monitoring-grafana.monitoring.svc.cluster.local:80
- service: http_status:404
`

func service(namespace, name, domain string, port int32) corev1.Service {
	svc := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{Port: port}},
		},
	}
	if domain != "" {
		svc.Annotations = map[string]string{DomainAnnotation: domain}
	}
	return svc
}

func TestDesiredRoutes(t *testing.T) {
	tests := []struct {
		name     string
		services []corev1.Service
		current  []cloudflaredIngressRule
		want     []cloudflaredIngressRule
	}{
		{
			name: "ignores services without the annotation",
			services: []corev1.Service{
				service("homelab-web", "homelab-web", "homelab.carleid.dev", 80),
				service("homelab-api", "homelab-api", "", 80),
			},
			want: []cloudflaredIngressRule{{
				Hostname: "homelab.carleid.dev",
				Service:  "http://homelab-web.homelab-web.svc.cluster.local:80",
			}},
		},
		{
			name: "sorts by hostname regardless of service order",
			services: []corev1.Service{
				service("b", "b", "zeta.carleid.dev", 80),
				service("a", "a", "alpha.carleid.dev", 80),
			},
			want: []cloudflaredIngressRule{
				{Hostname: "alpha.carleid.dev", Service: "http://a.a.svc.cluster.local:80"},
				{Hostname: "zeta.carleid.dev", Service: "http://b.b.svc.cluster.local:80"},
			},
		},
		{
			name:     "uses the service's own port",
			services: []corev1.Service{service("apps", "grafana", "grafana.carleid.dev", 3000)},
			want: []cloudflaredIngressRule{{
				Hostname: "grafana.carleid.dev",
				Service:  "http://grafana.apps.svc.cluster.local:3000",
			}},
		},
		{
			name:     "carries extra keys across a rebuild",
			services: []corev1.Service{service("apps", "web", "web.carleid.dev", 80)},
			current: []cloudflaredIngressRule{{
				Hostname: "web.carleid.dev",
				Service:  "http://stale.old.svc.cluster.local:80",
				Extra:    map[string]any{"originRequest": map[string]any{"noTLSVerify": true}},
			}},
			want: []cloudflaredIngressRule{{
				Hostname: "web.carleid.dev",
				Service:  "http://web.apps.svc.cluster.local:80",
				Extra:    map[string]any{"originRequest": map[string]any{"noTLSVerify": true}},
			}},
		},
		{
			name:     "no annotated services yields no rules",
			services: []corev1.Service{service("apps", "internal", "", 80)},
			want:     []cloudflaredIngressRule{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, conflicts := desiredRoutes(tt.services, tt.current)
			if len(conflicts) != 0 {
				t.Fatalf("unexpected conflicts: %v", conflicts)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d rules, want %d: %+v", len(got), len(tt.want), got)
			}
			for i := range tt.want {
				if got[i].Hostname != tt.want[i].Hostname || got[i].Service != tt.want[i].Service {
					t.Errorf("rule %d = %+v, want %+v", i, got[i], tt.want[i])
				}
				if tt.want[i].Extra != nil && got[i].Extra == nil {
					t.Errorf("rule %d lost its extra keys", i)
				}
			}
		})
	}
}

func TestDesiredRoutesResolvesDuplicateHostnames(t *testing.T) {
	// Declared in the order that would win if resolution were arbitrary, to
	// catch a regression to map-iteration ordering.
	services := []corev1.Service{
		service("zebra", "web", "homelab.carleid.dev", 80),
		service("alpha", "web", "homelab.carleid.dev", 80),
	}

	got, conflicts := desiredRoutes(services, nil)

	if len(got) != 1 {
		t.Fatalf("got %d rules, want 1: %+v", len(got), got)
	}
	if want := "http://web.alpha.svc.cluster.local:80"; got[0].Service != want {
		t.Errorf("service = %q, want %q", got[0].Service, want)
	}
	if len(conflicts) != 1 {
		t.Fatalf("got %d conflicts, want 1", len(conflicts))
	}
	if conflicts[0].Used != "alpha/web" || conflicts[0].Ignored != "zebra/web" {
		t.Errorf("conflict = %+v, want alpha/web over zebra/web", conflicts[0])
	}
}

func TestRoundTripPreservesUnmanagedKeys(t *testing.T) {
	cfg, err := parseCloudflaredConfig(liveConfig)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	idx := catchAllIndex(cfg.Ingress)
	if idx == -1 {
		t.Fatal("catch-all not found in the parsed config")
	}

	rules, _ := desiredRoutes(
		[]corev1.Service{service("monitoring", "monitoring-grafana", "grafana.carleid.dev", 80)},
		cfg.Ingress,
	)
	cfg.Ingress = append(rules, cfg.Ingress[idx])

	rendered, err := marshalCloudflaredConfig(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	for _, key := range []string{"tunnel: vps-cluster-tunnel", "metrics: 0.0.0.0:2000", "no-autoupdate: true"} {
		if !strings.Contains(rendered, key) {
			t.Errorf("rendered config dropped %q:\n%s", key, rendered)
		}
	}
	if !strings.Contains(rendered, "http_status:404") {
		t.Errorf("rendered config dropped the catch-all:\n%s", rendered)
	}

	// The catch-all must remain last, or cloudflared would answer every
	// hostname with a 404.
	reparsed, err := parseCloudflaredConfig(rendered)
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if got := catchAllIndex(reparsed.Ingress); got != len(reparsed.Ingress)-1 {
		t.Errorf("catch-all at index %d of %d, want last", got, len(reparsed.Ingress))
	}
}

func TestCatchAllIndex(t *testing.T) {
	tests := []struct {
		name  string
		rules []cloudflaredIngressRule
		want  int
	}{
		{name: "empty", rules: nil, want: -1},
		{
			name:  "no catch-all",
			rules: []cloudflaredIngressRule{{Hostname: "a", Service: "http://a"}},
			want:  -1,
		},
		{
			name: "catch-all last",
			rules: []cloudflaredIngressRule{
				{Hostname: "a", Service: "http://a"},
				{Service: "http_status:404"},
			},
			want: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := catchAllIndex(tt.rules); got != tt.want {
				t.Errorf("catchAllIndex() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestIngressEqual(t *testing.T) {
	base := []cloudflaredIngressRule{
		{Hostname: "a.carleid.dev", Service: "http://a.a.svc.cluster.local:80"},
		{Service: "http_status:404"},
	}

	tests := []struct {
		name string
		b    []cloudflaredIngressRule
		want bool
	}{
		{name: "identical", b: base, want: true},
		{
			name: "different backend",
			b: []cloudflaredIngressRule{
				{Hostname: "a.carleid.dev", Service: "http://a.b.svc.cluster.local:80"},
				{Service: "http_status:404"},
			},
			want: false,
		},
		{
			name: "shorter",
			b:    []cloudflaredIngressRule{{Service: "http_status:404"}},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ingressEqual(base, tt.b); got != tt.want {
				t.Errorf("ingressEqual() = %v, want %v", got, tt.want)
			}
		})
	}
}

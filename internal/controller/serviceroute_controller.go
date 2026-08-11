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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"gopkg.in/yaml.v3"
)

const (
	// DomainAnnotation marks a Service for public exposure. Its value is the
	// hostname cloudflared routes to that Service.
	DomainAnnotation = "homelab.carleid.dev/domain"

	// configHashAnnotation carries a digest of the rendered config on the
	// cloudflared pod template. A locally-managed tunnel reads its config file
	// only at startup, so the pods have to roll for a route change to apply.
	configHashAnnotation = "homelab.carleid.dev/config-hash"
)

// ServiceRouteReconciler keeps the cloudflared ingress list in step with the
// Services that ask to be exposed.
type ServiceRouteReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	CloudflaredConfigMap  string
	CloudflaredDeployment string
	CloudflaredNamespace  string
}

// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;patch

func (r *ServiceRouteReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logf.FromContext(ctx).Info("Reconciling cloudflared routes", "trigger", req.NamespacedName)
	return ctrl.Result{}, r.syncRoutes(ctx)
}

// syncRoutes rebuilds the entire ingress list from the annotated Services in
// the cluster. Rebuilding wholesale rather than patching individual rules means
// deleted Services and removed annotations need no special handling, and the
// ConfigMap converges on cluster state from any starting point.
func (r *ServiceRouteReconciler) syncRoutes(ctx context.Context) error {
	log := logf.FromContext(ctx)

	cm, cfg, err := r.getCloudflaredConfig(ctx)
	if err != nil {
		return err
	}

	// The catch-all is cloudflared's fallback for unmatched hostnames and must
	// stay last. It is the one rule not derived from a Service, so it is carried
	// across untouched.
	idx := catchAllIndex(cfg.Ingress)
	if idx == -1 {
		return fmt.Errorf("cloudflared config has no catch-all ingress rule, refusing to rewrite it")
	}

	var services corev1.ServiceList
	if err := r.List(ctx, &services); err != nil {
		return err
	}

	desired, conflicts := desiredRoutes(services.Items, cfg.Ingress)
	for _, c := range conflicts {
		log.Info("Duplicate domain annotation", "domain", c.Domain, "using", c.Used, "ignoring", c.Ignored)
	}
	desired = append(desired, cfg.Ingress[idx])

	if ingressEqual(cfg.Ingress, desired) {
		return nil
	}

	cfg.Ingress = desired
	rendered, err := marshalCloudflaredConfig(cfg)
	if err != nil {
		return err
	}

	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data["config.yaml"] = rendered

	log.Info("Rewriting cloudflared ingress", "routes", len(desired)-1)
	if err := r.Update(ctx, cm); err != nil {
		return err
	}

	return r.rollCloudflared(ctx, rendered)
}

// domainConflict records a hostname claimed by more than one Service, and which
// claim won.
type domainConflict struct {
	Domain  string
	Used    string
	Ignored string
}

// desiredRoutes builds one ingress rule per annotated Service, sorted by
// hostname so the rendered config is byte-stable between reconciles. Extra keys
// on an existing rule for the same hostname are carried over, so hand-added
// settings such as originRequest survive a rebuild. The catch-all is not
// included; the caller appends it.
func desiredRoutes(
	services []corev1.Service,
	current []cloudflaredIngressRule,
) ([]cloudflaredIngressRule, []domainConflict) {
	claimed := map[string]corev1.Service{}
	var conflicts []domainConflict

	for _, svc := range services {
		domain := svc.Annotations[DomainAnnotation]
		if domain == "" {
			continue
		}
		// Two Services claiming one hostname is a misconfiguration. Resolve it
		// by name so the winner does not change from one reconcile to the next.
		if prev, taken := claimed[domain]; taken {
			winner, loser := prev, svc
			if serviceKey(svc) < serviceKey(prev) {
				winner, loser = svc, prev
			}
			claimed[domain] = winner
			conflicts = append(conflicts, domainConflict{
				Domain:  domain,
				Used:    serviceKey(winner),
				Ignored: serviceKey(loser),
			})
			continue
		}
		claimed[domain] = svc
	}

	preserved := map[string]cloudflaredIngressRule{}
	for _, rule := range current {
		if rule.Hostname != "" {
			preserved[rule.Hostname] = rule
		}
	}

	hostnames := make([]string, 0, len(claimed))
	for hostname := range claimed {
		hostnames = append(hostnames, hostname)
	}
	sort.Strings(hostnames)

	rules := make([]cloudflaredIngressRule, 0, len(hostnames))
	for _, hostname := range hostnames {
		svc := claimed[hostname]
		rule := cloudflaredIngressRule{Hostname: hostname, Service: serviceURL(&svc)}
		if prev, ok := preserved[hostname]; ok {
			rule.Extra = prev.Extra
		}
		rules = append(rules, rule)
	}

	sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].Domain < conflicts[j].Domain })
	return rules, conflicts
}

// rollCloudflared stamps a digest of the rendered config onto the tunnel's pod
// template so the deployment rolls and the new config is read at startup.
func (r *ServiceRouteReconciler) rollCloudflared(ctx context.Context, rendered string) error {
	sum := sha256.Sum256([]byte(rendered))
	patch := fmt.Sprintf(
		`{"spec":{"template":{"metadata":{"annotations":{%q:%q}}}}}`,
		configHashAnnotation, hex.EncodeToString(sum[:]),
	)

	dep := &appsv1.Deployment{}
	dep.Name = r.CloudflaredDeployment
	dep.Namespace = r.CloudflaredNamespace

	logf.FromContext(ctx).Info("Rolling cloudflared", "deployment", r.CloudflaredDeployment)
	return r.Patch(ctx, dep, client.RawPatch(types.StrategicMergePatchType, []byte(patch)))
}

func (r *ServiceRouteReconciler) getCloudflaredConfig(
	ctx context.Context,
) (*corev1.ConfigMap, *cloudflaredConfig, error) {
	cm := &corev1.ConfigMap{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      r.CloudflaredConfigMap,
		Namespace: r.CloudflaredNamespace,
	}, cm); err != nil {
		return nil, nil, err
	}
	cfg, err := parseCloudflaredConfig(cm.Data["config.yaml"])
	if err != nil {
		return nil, nil, err
	}
	return cm, cfg, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ServiceRouteReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Service{}, builder.WithPredicates(annotatedService)).
		Named("serviceroute").
		Complete(r)
}

// annotatedService admits events for Services carrying the domain annotation.
// An update is admitted when either side carries it, so stripping the
// annotation still triggers the rebuild that drops its route.
var annotatedService = predicate.Funcs{
	CreateFunc:  func(e event.CreateEvent) bool { return hasDomain(e.Object) },
	DeleteFunc:  func(e event.DeleteEvent) bool { return hasDomain(e.Object) },
	UpdateFunc:  func(e event.UpdateEvent) bool { return hasDomain(e.ObjectOld) || hasDomain(e.ObjectNew) },
	GenericFunc: func(e event.GenericEvent) bool { return hasDomain(e.Object) },
}

func hasDomain(obj client.Object) bool {
	return obj.GetAnnotations()[DomainAnnotation] != ""
}

func serviceKey(svc corev1.Service) string {
	return svc.Namespace + "/" + svc.Name
}

// serviceURL is the in-cluster address cloudflared forwards a hostname to.
func serviceURL(svc *corev1.Service) string {
	port := int32(80)
	if len(svc.Spec.Ports) > 0 {
		port = svc.Spec.Ports[0].Port
	}
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", svc.Name, svc.Namespace, port)
}

func ingressEqual(a, b []cloudflaredIngressRule) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Hostname != b[i].Hostname || a[i].Service != b[i].Service {
			return false
		}
	}
	return true
}

// cloudflaredIngressRule is one rule in cloudflared's ingress list. Any extra
// keys (path, originRequest, …) are preserved through the inline map.
type cloudflaredIngressRule struct {
	Hostname string                 `yaml:"hostname,omitempty"`
	Service  string                 `yaml:"service,omitempty"`
	Extra    map[string]any `yaml:",inline"`
}

// cloudflaredConfig models cloudflared's config.yaml. Top-level keys other than
// "ingress" (tunnel, metrics, no-autoupdate, …) are preserved via the inline map.
type cloudflaredConfig struct {
	Ingress []cloudflaredIngressRule `yaml:"ingress"`
	Extra   map[string]any   `yaml:",inline"`
}

func parseCloudflaredConfig(raw string) (*cloudflaredConfig, error) {
	cfg := &cloudflaredConfig{}
	if err := yaml.Unmarshal([]byte(raw), cfg); err != nil {
		return nil, fmt.Errorf("parsing cloudflared config.yaml: %w", err)
	}
	return cfg, nil
}

func marshalCloudflaredConfig(cfg *cloudflaredConfig) (string, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(cfg); err != nil {
		_ = enc.Close()
		return "", fmt.Errorf("marshaling cloudflared config.yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// catchAllIndex returns the index of the catch-all rule (the rule with no
// hostname, e.g. `service: http_status:404`), or -1 if there is none.
func catchAllIndex(rules []cloudflaredIngressRule) int {
	for i, rule := range rules {
		if rule.Hostname == "" {
			return i
		}
	}
	return -1
}

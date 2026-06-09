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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"gopkg.in/yaml.v3"

	homelabv1alpha1 "github.com/ceid1987/homelab/operator/api/v1alpha1"
)

const finalizerName = "homelab.carleid.dev/finalizer"

// HomelabAppReconciler reconciles a HomelabApp object
type HomelabAppReconciler struct {
	client.Client
	Scheme               *runtime.Scheme
	CloudflaredConfigMap string
	CloudflaredNamespace string
}

// +kubebuilder:rbac:groups=homelab.carleid.dev,resources=homelabapps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=homelab.carleid.dev,resources=homelabapps/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=homelab.carleid.dev,resources=homelabapps/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=argoproj.io,resources=applications,verbs=get;list;watch;create;update;patch

func (r *HomelabAppReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// 1. Fetch the HomelabApp object
	app := &homelabv1alpha1.HomelabApp{}
	if err := r.Get(ctx, req.NamespacedName, app); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("Reconciling HomelabApp", "name", app.Name, "domain", app.Spec.Domain)

	// 2. Handle deletion
	if !app.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.handleDeletion(ctx, app)
	}

	// 3. Register finalizer if domain is set
	if app.Spec.Domain != "" && !controllerutil.ContainsFinalizer(app, finalizerName) {
		controllerutil.AddFinalizer(app, finalizerName)
		if err := r.Update(ctx, app); err != nil {
			return ctrl.Result{}, err
		}
	}

	// 4. Ensure namespace
	if err := r.ensureNamespace(ctx, app); err != nil {
		log.Error(err, "Failed to ensure namespace")
		return ctrl.Result{}, err
	}

	// 5. Ensure ArgoCD Application
	if err := r.ensureArgoCDApplication(ctx, app); err != nil {
		log.Error(err, "Failed to ensure ArgoCD Application")
		return ctrl.Result{}, err
	}

	// 6. Ensure cloudflared route if domain is set
	if app.Spec.Domain != "" {
		if err := r.ensureCloudflaredRoute(ctx, app); err != nil {
			log.Error(err, "Failed to ensure cloudflared route")
			return ctrl.Result{}, err
		}
	}

	log.Info("HomelabApp reconciled successfully", "name", app.Name)
	return ctrl.Result{}, nil
}

// handleDeletion removes the cloudflared route then removes the finalizer
func (r *HomelabAppReconciler) handleDeletion(ctx context.Context, app *homelabv1alpha1.HomelabApp) error {
	log := logf.FromContext(ctx)

	if controllerutil.ContainsFinalizer(app, finalizerName) {
		if app.Spec.Domain != "" {
			if err := r.removeCloudflaredRoute(ctx, app); err != nil {
				log.Error(err, "Failed to remove cloudflared route")
				return err
			}
		}

		controllerutil.RemoveFinalizer(app, finalizerName)
		if err := r.Update(ctx, app); err != nil {
			return err
		}
	}

	return nil
}

// ensureNamespace creates the target namespace if it doesn't exist
func (r *HomelabAppReconciler) ensureNamespace(ctx context.Context, app *homelabv1alpha1.HomelabApp) error {
	log := logf.FromContext(ctx)

	ns := &corev1.Namespace{}
	err := r.Get(ctx, types.NamespacedName{Name: app.Spec.TargetNamespace}, ns)
	if err == nil {
		return nil
	}
	if !errors.IsNotFound(err) {
		return err
	}

	ns = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: app.Spec.TargetNamespace,
			Labels: map[string]string{
				"managed-by":  "homelab-operator",
				"homelab-app": app.Name,
			},
		},
	}
	log.Info("Creating namespace", "namespace", app.Spec.TargetNamespace)
	return r.Create(ctx, ns)
}

// ensureArgoCDApplication creates the ArgoCD Application if it doesn't exist
func (r *HomelabAppReconciler) ensureArgoCDApplication(ctx context.Context, app *homelabv1alpha1.HomelabApp) error {
	log := logf.FromContext(ctx)

	argoApp := &unstructured.Unstructured{}
	argoApp.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "argoproj.io",
		Version: "v1alpha1",
		Kind:    "Application",
	})

	err := r.Get(ctx, types.NamespacedName{
		Name:      app.Name,
		Namespace: "argocd",
	}, argoApp)

	if err == nil {
		return nil
	}
	if !errors.IsNotFound(err) {
		return err
	}

	argoApp = &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Application",
			"metadata": map[string]interface{}{
				"name":      app.Name,
				"namespace": "argocd",
				"labels": map[string]interface{}{
					"managed-by":  "homelab-operator",
					"homelab-app": app.Name,
				},
			},
			"spec": map[string]interface{}{
				"project": "default",
				"source": map[string]interface{}{
					"repoURL":        app.Spec.Repo,
					"path":           app.Spec.Path,
					"targetRevision": "HEAD",
				},
				"destination": map[string]interface{}{
					"server":    "https://kubernetes.default.svc",
					"namespace": app.Spec.TargetNamespace,
				},
				"syncPolicy": map[string]interface{}{
					"automated": map[string]interface{}{
						"prune":    true,
						"selfHeal": true,
					},
				},
			},
		},
	}

	log.Info("Creating ArgoCD Application", "name", app.Name, "path", app.Spec.Path)
	return r.Create(ctx, argoApp)
}

// cloudflaredIngressRule is one rule in cloudflared's ingress list. Any extra
// keys (path, originRequest, …) are preserved through the inline map.
type cloudflaredIngressRule struct {
	Hostname string                 `yaml:"hostname,omitempty"`
	Service  string                 `yaml:"service,omitempty"`
	Extra    map[string]interface{} `yaml:",inline"`
}

// cloudflaredConfig models cloudflared's config.yaml. Top-level keys other than
// "ingress" (tunnel, metrics, no-autoupdate, …) are preserved via the inline map.
type cloudflaredConfig struct {
	Ingress []cloudflaredIngressRule `yaml:"ingress"`
	Extra   map[string]interface{}   `yaml:",inline"`
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

// serviceURL is the in-cluster address cloudflared forwards the hostname to.
func serviceURL(app *homelabv1alpha1.HomelabApp) string {
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:80", app.Name, app.Spec.TargetNamespace)
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

func (r *HomelabAppReconciler) getCloudflaredConfig(ctx context.Context) (*corev1.ConfigMap, *cloudflaredConfig, error) {
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

func (r *HomelabAppReconciler) writeCloudflaredConfig(ctx context.Context, cm *corev1.ConfigMap, cfg *cloudflaredConfig) error {
	out, err := marshalCloudflaredConfig(cfg)
	if err != nil {
		return err
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data["config.yaml"] = out
	return r.Update(ctx, cm)
}

// ensureCloudflaredRoute makes sure the cloudflared ingress list has a rule for
// the app's domain pointing at its Service, inserted before the catch-all.
func (r *HomelabAppReconciler) ensureCloudflaredRoute(ctx context.Context, app *homelabv1alpha1.HomelabApp) error {
	log := logf.FromContext(ctx)

	cm, cfg, err := r.getCloudflaredConfig(ctx)
	if err != nil {
		return err
	}

	desired := serviceURL(app)

	// If a rule for this hostname already exists, just make sure it points at
	// the right service.
	for i := range cfg.Ingress {
		if cfg.Ingress[i].Hostname == app.Spec.Domain {
			if cfg.Ingress[i].Service == desired {
				return nil
			}
			cfg.Ingress[i].Service = desired
			log.Info("Updating cloudflared route", "domain", app.Spec.Domain, "service", desired)
			return r.writeCloudflaredConfig(ctx, cm, cfg)
		}
	}

	// Otherwise insert a new rule just before the catch-all so it stays last.
	idx := catchAllIndex(cfg.Ingress)
	if idx == -1 {
		return fmt.Errorf("cloudflared config missing catch-all ingress rule, cannot safely patch")
	}
	newRule := cloudflaredIngressRule{Hostname: app.Spec.Domain, Service: desired}
	cfg.Ingress = append(cfg.Ingress[:idx], append([]cloudflaredIngressRule{newRule}, cfg.Ingress[idx:]...)...)

	log.Info("Adding cloudflared route", "domain", app.Spec.Domain, "service", desired)
	return r.writeCloudflaredConfig(ctx, cm, cfg)
}

// removeCloudflaredRoute removes the ingress rule for the app's domain.
func (r *HomelabAppReconciler) removeCloudflaredRoute(ctx context.Context, app *homelabv1alpha1.HomelabApp) error {
	log := logf.FromContext(ctx)

	cm, cfg, err := r.getCloudflaredConfig(ctx)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}

	kept := make([]cloudflaredIngressRule, 0, len(cfg.Ingress))
	removed := false
	for _, rule := range cfg.Ingress {
		if rule.Hostname == app.Spec.Domain {
			removed = true
			continue
		}
		kept = append(kept, rule)
	}
	if !removed {
		return nil
	}
	cfg.Ingress = kept

	log.Info("Removing cloudflared route", "domain", app.Spec.Domain)
	return r.writeCloudflaredConfig(ctx, cm, cfg)
}

// SetupWithManager sets up the controller with the Manager.
func (r *HomelabAppReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&homelabv1alpha1.HomelabApp{}).
		Named("homelabapp").
		Complete(r)
}

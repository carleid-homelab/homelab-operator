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
	"context"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	homelabv1alpha1 "github.com/ceid1987/homelab/operator/api/v1alpha1"
)

// HomelabAppReconciler reconciles a HomelabApp object
type HomelabAppReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=homelab.carleid.dev,resources=homelabapps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=homelab.carleid.dev,resources=homelabapps/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=homelab.carleid.dev,resources=homelabapps/finalizers,verbs=update
// +kubebuilder:rbac:groups="", resources=namespaces,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=argoproj.io,resources=applications,verbs=get;list;watch;create;update;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the HomelabApp object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.3/pkg/reconcile

// Reconcile
func (r *HomelabAppReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// 1. Fetch the HomelapApp object
	app := &homelabv1alpha1.HomelabApp{}
	if err := r.Get(ctx, req.NamespacedName, app); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("Reconciling HomelabApp", "name", app.Name, "domain", app.Spec.Domain)

	// 2. Check if target namespace exists
	if err := r.ensureNamespace(ctx, app); err != nil {
		log.Error(err, "Failed to ensure namespace")
		return ctrl.Result{}, err
	}

	// 3. Check if ArgoCD app exists
	if err := r.ensureArgoCDApplication(ctx, app); err != nil {
		log.Error(err, "Failed to ensure ArgoCD Application")
		return ctrl.Result{}, err
	}

	log.Info("HomelabApp reconciled successfully", "name", app.Name)
	return ctrl.Result{}, nil
}

// ensureNamespace - Creates target namespace if it doesn't exist
func (r *HomelabAppReconciler) ensureNamespace(ctx context.Context, app *homelabv1alpha1.HomelabApp) error {
	log := logf.FromContext(ctx)

	ns := &corev1.Namespace{}
	err := r.Get(ctx, types.NamespacedName{Name: app.Spec.TargetNamespace}, ns)
	if err == nil {
		// Already exists?
		return nil
	}
	if !errors.IsNotFound(err) {
		return err
	}

	// Create ns
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

// ensureArgoCDApplication - Creates ArgoCD app if it doesn't exist
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
		// Already exists
		return nil
	}
	if !errors.IsNotFound(err) {
		return err
	}

	// Build ArgoCD app object
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

// SetupWithManager sets up the controller with the Manager.
func (r *HomelabAppReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&homelabv1alpha1.HomelabApp{}).
		Named("homelabapp").
		Complete(r)
}

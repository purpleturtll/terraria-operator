/*
Copyright 2024.

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
	"fmt"
	"slices"
	"strconv"
	"time"

	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	terrariav1 "github.com/terraria-operator/terraria-operator/api/v1"
)

const (
	serverFinalizer = "finalizer.terraria-server.com"
)

// ServerReconciler reconciles a Server object
type ServerReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=terraria.terraria-operator,resources=servers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=terraria.terraria-operator,resources=servers/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=terraria.terraria-operator,resources=servers/finalizers,verbs=update

// Reconcile is part of the main Kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *ServerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Fetch the Server instance
	server := &terrariav1.Server{}
	err := r.Get(ctx, req.NamespacedName, server)
	if err != nil {
		if errors.IsNotFound(err) {
			// Server resource not found. Ignoring since object must be deleted
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get Server")
		return ctrl.Result{}, err
	}

	// Check if the Pod already exists, if not create a new one
	pod := &corev1.Pod{}
	err = r.Get(ctx, client.ObjectKey{Namespace: server.Namespace, Name: server.Name}, pod)
	if err != nil && errors.IsNotFound(err) {
		// Define a new Pod
		pod := newPodForCR(server)
		log.Info("Creating a new Pod", "Pod.Namespace", pod.Namespace, "Pod.Name", pod.Name)
		if err := r.Create(ctx, pod); err != nil {
			log.Error(err, "Failed to create new Pod", "Pod.Namespace", pod.Namespace, "Pod.Name", pod.Name)
			return ctrl.Result{Requeue: true}, err
		}
	} else if err != nil {
		log.Error(err, "Failed to get Pod")
		return ctrl.Result{}, err
	}

	// Check if the Service already exists, if not create a new one
	service := &corev1.Service{}
	err = r.Get(ctx, client.ObjectKey{Namespace: server.Namespace, Name: server.Name}, service)
	if err != nil && errors.IsNotFound(err) {
		// Define a new Service
		service := newServiceForCR(server)
		log.Info("Creating a new Service", "Service.Namespace", service.Namespace, "Service.Name", service.Name)
		if err := r.Create(ctx, service); err != nil {
			log.Error(err, "Failed to create new Service", "Service.Namespace", service.Namespace, "Service.Name", service.Name)
			return ctrl.Result{Requeue: true}, err
		}
	} else if err != nil {
		log.Error(err, "Failed to get Service")
		return ctrl.Result{}, err
	}

	// Check if the Ingress already exists, if not create a new one
	ingress := &networkingv1.Ingress{}
	err = r.Get(ctx, client.ObjectKey{Namespace: server.Namespace, Name: server.Name}, ingress)
	if err != nil && errors.IsNotFound(err) {
		// Define a new Ingress
		ingress := newIngressForCR(server)
		log.Info("Creating a new Ingress", "Ingress.Namespace", ingress.Namespace, "Ingress.Name", ingress.Name)
		if err := r.Create(ctx, ingress); err != nil {
			log.Error(err, "Failed to create new Ingress", "Ingress.Namespace", ingress.Namespace, "Ingress.Name", ingress.Name)
			return ctrl.Result{Requeue: true}, err
		}
	} else if err != nil {
		log.Error(err, "Failed to get Ingress")
		return ctrl.Result{}, err
	}

	// If the server object is not being deleted and does not have a finalizer then add one
	if server.ObjectMeta.DeletionTimestamp.IsZero() {
		if !slices.Contains(server.GetFinalizers(), serverFinalizer) {
			controllerutil.AddFinalizer(server, serverFinalizer)
			if err := r.Update(ctx, server); err != nil {
				return ctrl.Result{}, err
			}
		}

		// Update nginx configuration and restart nginx
		if err := updateNginxConfigMapAndService(ctx, r, server, true); err != nil {
			log.Error(err, "Failed to update Nginx ConfigMap and Service")
			return ctrl.Result{Requeue: true}, err
		}
		if err := restartNginx(ctx, r); err != nil {
			log.Error(err, "Failed to restart Nginx")
			return ctrl.Result{Requeue: true}, err
		}
	}

	// Handle cleanup when the server object is being deleted
	if !server.ObjectMeta.DeletionTimestamp.IsZero() {
		if slices.Contains(server.GetFinalizers(), serverFinalizer) {
			if err := updateNginxConfigMapAndService(ctx, r, server, false); err != nil {
				log.Error(err, "Failed to remove port from Nginx ConfigMap and Service")
				return ctrl.Result{Requeue: true}, err
			}
			if err := restartNginx(ctx, r); err != nil {
				log.Error(err, "Failed to restart Nginx after cleanup")
				return ctrl.Result{Requeue: true}, err
			}
			controllerutil.RemoveFinalizer(server, serverFinalizer)
			if err := r.Update(ctx, server); err != nil {
				return ctrl.Result{}, err
			}
		}
		// Deletion of pod, service, ingress not shown here to avoid redundancy; should be done as needed
	}

	// No requeue needed, Kubernetes will call Reconcile when changes occur to the resources watched
	return ctrl.Result{}, nil
}

// restartNginx updates an annotation on the Nginx deployment to trigger a pod restart.
func restartNginx(ctx context.Context, r *ServerReconciler) error {
	nginxDeploymentName := "nginx" // Name of the Nginx deployment
	nginxNamespace := "default"    // Namespace where the Nginx is deployed

	// Patch to update the 'kubectl.kubernetes.io/restartedAt' annotation to current time
	patch := client.RawPatch(types.StrategicMergePatchType, []byte(`{
        "spec": {
            "template": {
                "metadata": {
                    "annotations": {
                        "kubectl.kubernetes.io/restartedAt": "`+time.Now().Format(time.RFC3339)+`"
                    }
                }
            }
        }
    }`))

	// Create a namespaced name for the Nginx deployment
	depKey := client.ObjectKey{Name: nginxDeploymentName, Namespace: nginxNamespace}

	// Fetch the current deployment
	deployment := &v1.Deployment{}
	if err := r.Get(ctx, depKey, deployment); err != nil {
		return err
	}

	// Apply the patch to the deployment
	if err := r.Patch(ctx, deployment, patch); err != nil {
		return err
	}

	return nil
}

func updateNginxConfigMapAndService(
	ctx context.Context,
	r *ServerReconciler,
	server *terrariav1.Server,
	add bool,
) error {
	configMap := &corev1.ConfigMap{}
	cmKey := client.ObjectKey{
		Namespace: "ingress-nginx", // Assuming this is the namespace where Nginx is deployed
		Name:      "tcp-services",
	}

	if err := r.Get(ctx, cmKey, configMap); err != nil {
		return err
	}

	servicePort := strconv.Itoa(server.Spec.Port) // Assuming server.Spec.Port is the desired port
	nginxPort := servicePort                      // Define mapping logic if different
	serviceName := fmt.Sprintf("%s/%s:80", server.Namespace, server.Name)

	if add {
		configMap.Data[nginxPort] = serviceName
	} else {
		delete(configMap.Data, nginxPort)
	}

	if err := r.Update(ctx, configMap); err != nil {
		return err
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ServerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&terrariav1.Server{}).
		Complete(r)
}

func newPodForCR(cr *terrariav1.Server) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.Name,
			Namespace: cr.Namespace,
			Labels: map[string]string{
				"app": cr.Name,
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "terraria",
					Image: "ghcr.io/purpleturtll/terraria-server:1449",
					Ports: []corev1.ContainerPort{
						{
							ContainerPort: 7777,
							Name:          "terraria",
						},
					},
				},
			},
		},
	}
	return pod
}

func newServiceForCR(cr *terrariav1.Server) *corev1.Service {
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.Name,
			Namespace: cr.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				"app": cr.Name,
			},
			Ports: []corev1.ServicePort{
				{
					Protocol: corev1.ProtocolTCP,
					Port:     80,
					TargetPort: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: 7777,
					},
				},
			},
			Type: corev1.ServiceTypeClusterIP,
		},
	}
	return service
}

// newIngressForCR returns a Ingress with the same name/namespace as the cr
func newIngressForCR(cr *terrariav1.Server) *networkingv1.Ingress {
	ingress := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.Name,
			Namespace: cr.Namespace,
			Annotations: map[string]string{
				"kubernetes.io/ingress.class":                "nginx",
				"nginx.ingress.kubernetes.io/rewrite-target": "/",
			},
		},
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{
				{
					Host: cr.Name,
					IngressRuleValue: networkingv1.IngressRuleValue{
						HTTP: &networkingv1.HTTPIngressRuleValue{
							Paths: []networkingv1.HTTPIngressPath{
								{
									Path: "/",
									PathType: func() *networkingv1.PathType {
										pt := networkingv1.PathTypePrefix
										return &pt
									}(),
									Backend: networkingv1.IngressBackend{
										Service: &networkingv1.IngressServiceBackend{
											Name: cr.Name,
											Port: networkingv1.ServiceBackendPort{
												Number: 80,
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	return ingress
}

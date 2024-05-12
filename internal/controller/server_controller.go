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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	terrariav1 "github.com/terraria-operator/terraria-operator/api/v1"
)

// ServerReconciler reconciles a Server object
type ServerReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=terraria.terraria-operator,resources=servers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=terraria.terraria-operator,resources=servers/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=terraria.terraria-operator,resources=servers/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Server object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.16.3/pkg/reconcile
func (r *ServerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// This operator will only manage Terraria Server objects
	// For every Server there needs to be a Pod running the Terraria Server with
	// configuration from CR and a Service to expose the Pod
	// The operator will also manage the lifecycle of the Pod and Service

	// Fetch the Server instance
	server := &terrariav1.Server{}
	err := r.Get(ctx, req.NamespacedName, server)
	if err != nil {
		log.Error(err, "Failed to get Server")
		return ctrl.Result{}, err
	}

	log.Info("Reconciling Server", "Name", server.Name)

	// Check if the Pod already exists, if not create a new one
	pod := &corev1.Pod{}
	err = r.Get(ctx, client.ObjectKey{
		Namespace: server.Namespace,
		Name:      server.Name,
	}, pod)
	if err != nil {
		if errors.IsNotFound(err) {
			// Define a new Pod
			pod := newPodForCR(server)
			log.Info("Creating a new Pod", "Pod.Namespace", pod.Namespace, "Pod.Name", pod.Name)
			err = r.Create(ctx, pod)
			if err != nil {
				log.Error(err, "Failed to create new Pod", "Pod.Namespace", pod.Namespace, "Pod.Name", pod.Name)
				return ctrl.Result{Requeue: true}, err
			}
			// Pod created successfully - don't requeue
			return ctrl.Result{}, nil
		} else {
			log.Error(err, "Failed to get Pod")
			return ctrl.Result{}, err
		}
	}

	// Check if the Service already exists, if not create a new one
	service := &corev1.Service{}
	err = r.Get(ctx, client.ObjectKey{
		Namespace: server.Namespace,
		Name:      server.Name,
	}, service)
	if err != nil {
		if errors.IsNotFound(err) {
			// Define a new Service
			service := newServiceForCR(server)
			log.Info("Creating a new Service", "Service.Namespace", service.Namespace, "Service.Name", service.Name)
			err = r.Create(ctx, service)
			if err != nil {
				log.Error(err, "Failed to create new Service", "Service.Namespace", service.Namespace, "Service.Name", service.Name)
				return ctrl.Result{Requeue: true}, err
			}
			// Service created successfully - don't requeue
			return ctrl.Result{}, nil
		} else {
			log.Error(err, "Failed to get Service")
			return ctrl.Result{}, err
		}
	}

	// Pod and Service already exists - don't requeue
	log.Info("Skip reconcile: Pod and Service already exists",
		"Pod.Namespace",
		pod.Namespace,
		"Pod.Name",
		pod.Name,
		"Service.Namespace",
		service.Namespace,
		"Service.Name",
		service.Name,
	)

	return ctrl.Result{}, nil
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
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "terraria",
					Image: "quay.io/terraria/terraria:latest",
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
					Port:     7777,
					TargetPort: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: 7777,
					},
				},
			},
			Type: corev1.ServiceTypeNodePort,
		},
	}
	return service
}

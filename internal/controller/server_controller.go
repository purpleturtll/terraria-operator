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
	"slices"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
	if err := r.Get(ctx, req.NamespacedName, server); err != nil {
		if errors.IsNotFound(err) {
			// Server resource not found. Ignoring since object must be deleted
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get Server")
		return ctrl.Result{}, err
	}

	// Determine if we are adding or removing resources
	isDeletion := !server.ObjectMeta.DeletionTimestamp.IsZero()
	isFinalizerPresent := slices.Contains(server.GetFinalizers(), serverFinalizer)

	if isDeletion {
		if isFinalizerPresent {
			// Perform cleanup for LoadBalancer and port assignments
			if err := updateLoadBalancerService(ctx, r, server, false); err != nil {
				log.Error(err, "Failed to update LoadBalancer service for deletion")
				return ctrl.Result{Requeue: true}, err
			}
			if err := managePortAssignments(ctx, r, server, false); err != nil {
				log.Error(err, "Failed to manage port assignments for deletion")
				return ctrl.Result{Requeue: true}, err
			}

			// Continue with deletion logic for dependent resources
			if err := deleteResources(ctx, r, server); err != nil {
				log.Error(err, "Failed to delete resources")
				return ctrl.Result{Requeue: true}, err
			}

			// Remove the finalizer
			controllerutil.RemoveFinalizer(server, serverFinalizer)
			if err := r.Update(ctx, server); err != nil {
				return ctrl.Result{}, err
			}
		}
	} else {
		// Add finalizer for new or updated Server resources
		if !isFinalizerPresent {
			controllerutil.AddFinalizer(server, serverFinalizer)
			if err := r.Update(ctx, server); err != nil {
				return ctrl.Result{}, err
			}
		}

		// Handle creation and updates for dependent resources
		if err := reconcileResources(ctx, r, server); err != nil {
			log.Error(err, "Failed to reconcile resources")
			return ctrl.Result{Requeue: true}, err
		}

		// Update LoadBalancer service and manage port assignments
		if err := updateLoadBalancerService(ctx, r, server, true); err != nil {
			log.Error(err, "Failed to update LoadBalancer service")
			return ctrl.Result{Requeue: true}, err
		}
		// Update the Server CR with the LoadBalancer port if necessary
		if err := updateServerStatusWithLoadBalancerPort(ctx, r, server); err != nil {
			log.Error(err, "Failed to update Server status with LoadBalancer port")
			return ctrl.Result{}, err
		}
		if err := managePortAssignments(ctx, r, server, true); err != nil {
			log.Error(err, "Failed to manage port assignments")
			return ctrl.Result{Requeue: true}, err
		}
	}

	// No requeue needed, Kubernetes will call Reconcile when changes occur to the resources watched
	return ctrl.Result{}, nil
}

func reconcileResources(ctx context.Context, r *ServerReconciler, server *terrariav1.Server) error {
	log := log.FromContext(ctx)

	// Reconcile Pod
	pod := &corev1.Pod{}
	podName := client.ObjectKey{Namespace: server.Namespace, Name: server.Name}
	if err := r.Get(ctx, podName, pod); err != nil {
		if errors.IsNotFound(err) {
			pod = newPodForCR(server)
			if err = r.Create(ctx, pod); err != nil {
				log.Error(err, "Failed to create Pod", "Pod.Namespace", pod.Namespace, "Pod.Name", pod.Name)
				return err
			}
		} else {
			return err
		}
	}

	// Reconcile Service
	service := &corev1.Service{}
	serviceName := client.ObjectKey{Namespace: server.Namespace, Name: server.Name}
	if err := r.Get(ctx, serviceName, service); err != nil {
		if errors.IsNotFound(err) {
			service = newServiceForCR(server)
			if err = r.Create(ctx, service); err != nil {
				log.Error(err, "Failed to create Service", "Service.Namespace", service.Namespace, "Service.Name", service.Name)
				return err
			}
		} else {
			return err
		}
	}

	return nil
}

func deleteResources(ctx context.Context, r *ServerReconciler, server *terrariav1.Server) error {
	log := log.FromContext(ctx)

	// Delete Pod
	pod := &corev1.Pod{}
	podName := client.ObjectKey{Namespace: server.Namespace, Name: server.Name}
	if err := r.Get(ctx, podName, pod); err == nil {
		if err = r.Delete(ctx, pod); err != nil {
			log.Error(err, "Failed to delete Pod", "Pod.Namespace", pod.Namespace, "Pod.Name", pod.Name)
			return err
		}
	} else if !errors.IsNotFound(err) {
		return err
	}

	// Delete Service
	service := &corev1.Service{}
	serviceName := client.ObjectKey{Namespace: server.Namespace, Name: server.Name}
	if err := r.Get(ctx, serviceName, service); err == nil {
		if err = r.Delete(ctx, service); err != nil {
			log.Error(err, "Failed to delete Service", "Service.Namespace", service.Namespace, "Service.Name", service.Name)
			return err
		}
	} else if !errors.IsNotFound(err) {
		return err
	}

	// Delete Ingress
	ingress := &networkingv1.Ingress{}
	ingressName := client.ObjectKey{Namespace: server.Namespace, Name: server.Name}
	if err := r.Get(ctx, ingressName, ingress); err == nil {
		if err = r.Delete(ctx, ingress); err != nil {
			log.Error(err, "Failed to delete Ingress", "Ingress.Namespace", ingress.Namespace, "Ingress.Name", ingress.Name)
			return err
		}
	} else if !errors.IsNotFound(err) {
		return err
	}

	return nil
}

func managePortAssignments(ctx context.Context, r *ServerReconciler, server *terrariav1.Server, add bool) error {
	configMap := &corev1.ConfigMap{}
	cmKey := client.ObjectKey{
		Namespace: "default", // Adjust as necessary
		Name:      "port-assignments",
	}

	if err := r.Get(ctx, cmKey, configMap); err != nil {
		if errors.IsNotFound(err) {
			// Create the ConfigMap if it does not exist
			configMap = &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "port-assignments",
					Namespace: "default",
				},
				Data: make(map[string]string),
			}
			if err := r.Create(ctx, configMap); err != nil {
				return err
			}
		} else {
			return err
		}
	}

	key := "server-" + server.Name

	if configMap.Data == nil {
		configMap.Data = make(map[string]string)
	}

	if add {
		configMap.Data[key] = strconv.Itoa(server.Spec.Port)
	} else {
		if _, exists := configMap.Data[key]; exists {
			delete(configMap.Data, key)
			if err := r.Update(ctx, configMap); err != nil {
				return err
			}
		}
	}

	if err := r.Update(ctx, configMap); err != nil {
		return err
	}

	// Ensure the NGINX tcp-services config map is updated accordingly
	return updateNGINXConfigMap(ctx, r, server, add)
}

func updateNGINXConfigMap(ctx context.Context, r *ServerReconciler, server *terrariav1.Server, add bool) error {
	nginxConfigMap := &corev1.ConfigMap{}
	nginxCmKey := client.ObjectKey{
		Namespace: "ingress-nginx", // Adjust as necessary
		Name:      "tcp-services",
	}

	if err := r.Get(ctx, nginxCmKey, nginxConfigMap); err != nil {
		if errors.IsNotFound(err) {
			// Create the ConfigMap if it does not exist
			nginxConfigMap = &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "tcp-services",
					Namespace: "ingress-nginx",
				},
				Data: make(map[string]string),
			}
			if err := r.Create(ctx, nginxConfigMap); err != nil {
				return err
			}
		} else {
			return err
		}
	}

	portKey := strconv.Itoa(server.Spec.Port)

	if nginxConfigMap.Data == nil {
		nginxConfigMap.Data = make(map[string]string)
	}

	if add {
		nginxConfigMap.Data[portKey] = "default/" + server.Name + ":" + strconv.Itoa(server.Spec.Port)
	} else {
		delete(nginxConfigMap.Data, portKey)
	}

	if err := r.Update(ctx, nginxConfigMap); err != nil {
		return err
	}

	return nil
}

func updateLoadBalancerService(ctx context.Context, r *ServerReconciler, server *terrariav1.Server, add bool) error {
	lbService := &corev1.Service{}
	lbKey := client.ObjectKey{
		Namespace: "ingress-nginx", // Adjust as necessary
		Name:      "ingress-nginx", // Adjust the LB service name as necessary
	}

	if err := r.Get(ctx, lbKey, lbService); err != nil {
		return err
	}

	port := corev1.ServicePort{
		Name:       "tcp-" + strconv.Itoa(server.Spec.Port),
		Port:       int32(server.Spec.Port),
		TargetPort: intstr.FromInt(server.Spec.Port),
		Protocol:   corev1.ProtocolTCP,
	}

	// Find if port already exists
	exists := false
	for _, p := range lbService.Spec.Ports {
		if p.Port == port.Port {
			exists = true
			break
		}
	}

	if add {
		if !exists {
			lbService.Spec.Ports = append(lbService.Spec.Ports, port)
		}
	} else {
		for i, p := range lbService.Spec.Ports {
			if p.Port == port.Port {
				lbService.Spec.Ports = append(lbService.Spec.Ports[:i], lbService.Spec.Ports[i+1:]...)
				break
			}
		}
	}

	if err := r.Update(ctx, lbService); err != nil {
		return err
	}

	return nil
}

func updateServerStatusWithLoadBalancerPort(ctx context.Context, r *ServerReconciler, server *terrariav1.Server) error {
	lbService := &corev1.Service{}
	lbKey := client.ObjectKey{
		Namespace: "ingress-nginx", // Adjust as necessary
		Name:      "ingress-nginx", // Adjust the LB service name as necessary
	}

	if err := r.Get(ctx, lbKey, lbService); err != nil {
		return err
	}

	// Extract the NodeIP
	// Get some node IP from list nodes
	nodeIP := &corev1.Node{}
	nodeList := &corev1.NodeList{}
	if err := r.List(ctx, nodeList); err != nil {
		return err
	}
	if len(nodeList.Items) > 0 {
		nodeIP = &nodeList.Items[0]
	}

	// Get Addresses where type is ExternalIP
	for _, address := range nodeIP.Status.Addresses {
		if address.Type == corev1.NodeExternalIP {
			server.Status.LoadBalancerIP = address.Address
			break
		}
	}

	// Extract the NodePort for the LoadBalancer service
	for _, port := range lbService.Spec.Ports {
		if port.TargetPort.Type == intstr.Int && port.TargetPort.IntVal == int32(server.Spec.Port) {
			server.Status.LoadBalancerPort = int(port.NodePort)
			break
		}
	}

	if err := r.Status().Update(ctx, server); err != nil {
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
					Port:     int32(cr.Spec.Port),
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

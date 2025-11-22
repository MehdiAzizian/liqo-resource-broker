/*
Copyright 2025.

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
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	brokerv1alpha1 "github.com/mehdiazizian/liqo-resource-broker/api/v1alpha1"
	"github.com/mehdiazizian/liqo-resource-broker/internal/broker"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ClusterAdvertisementReconciler reconciles a ClusterAdvertisement object
type ClusterAdvertisementReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	DecisionEngine *broker.DecisionEngine
}

// +kubebuilder:rbac:groups=broker.fluidos.eu,resources=clusteradvertisements,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=broker.fluidos.eu,resources=clusteradvertisements/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=broker.fluidos.eu,resources=clusteradvertisements/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop
func (r *ClusterAdvertisementReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling ClusterAdvertisement", "name", req.Name, "namespace", req.Namespace)

	// Fetch the ClusterAdvertisement instance
	clusterAdv := &brokerv1alpha1.ClusterAdvertisement{}
	err := r.Get(ctx, req.NamespacedName, clusterAdv)
	if err != nil {
		if client.IgnoreNotFound(err) == nil {
			logger.Info("ClusterAdvertisement not found, may have been deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get ClusterAdvertisement")
		return ctrl.Result{}, err
	}

	// Recalculate Available based on Allocatable - Allocated - Reserved
	available := clusterAdv.Spec.Resources.Allocatable.CPU.DeepCopy()
	available.Sub(clusterAdv.Spec.Resources.Allocated.CPU)
	if clusterAdv.Spec.Resources.Reserved != nil {
		available.Sub(clusterAdv.Spec.Resources.Reserved.CPU)
	}
	clusterAdv.Spec.Resources.Available.CPU = available

	availableMem := clusterAdv.Spec.Resources.Allocatable.Memory.DeepCopy()
	availableMem.Sub(clusterAdv.Spec.Resources.Allocated.Memory)
	if clusterAdv.Spec.Resources.Reserved != nil {
		availableMem.Sub(clusterAdv.Spec.Resources.Reserved.Memory)
	}
	clusterAdv.Spec.Resources.Available.Memory = availableMem

	// Update the spec with recalculated available
	if err := r.Update(ctx, clusterAdv); err != nil {
		logger.Error(err, "Failed to update available resources")
		// Continue anyway to update status
	}

	// Check if advertisement is stale (older than 2 minutes)
	age := time.Since(clusterAdv.Spec.Timestamp.Time)
	isStale := age > 10*time.Minute

	// Update status
	clusterAdv.Status.Active = !isStale
	if isStale {
		clusterAdv.Status.Phase = "Stale"
		clusterAdv.Status.Message = "Advertisement has not been updated recently"
	} else {
		clusterAdv.Status.Phase = "Active"
		clusterAdv.Status.Message = "Cluster is active and available"
	}

	// Calculate and update score
	if err := r.DecisionEngine.UpdateClusterScore(ctx, clusterAdv); err != nil {
		logger.Error(err, "Failed to update cluster score")
	}

	clusterAdv.Status.LastUpdateTime = metav1.Now()

	if err := r.Status().Update(ctx, clusterAdv); err != nil {
		logger.Error(err, "Failed to update ClusterAdvertisement status")
		return ctrl.Result{}, err
	}

	logger.Info("Updated ClusterAdvertisement",
		"clusterID", clusterAdv.Spec.ClusterID,
		"availableCPU", clusterAdv.Spec.Resources.Available.CPU.String(),
		"availableMemory", clusterAdv.Spec.Resources.Available.Memory.String(),
		"score", clusterAdv.Status.Score,
		"active", clusterAdv.Status.Active)

	// Requeue after 30 seconds to check for staleness
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterAdvertisementReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Initialize decision engine if not set
	if r.DecisionEngine == nil {
		r.DecisionEngine = &broker.DecisionEngine{
			Client: r.Client,
		}
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&brokerv1alpha1.ClusterAdvertisement{}).
		Named("clusteradvertisement").
		Complete(r)
}

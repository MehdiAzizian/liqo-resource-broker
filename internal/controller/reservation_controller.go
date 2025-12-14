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
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	brokerv1alpha1 "github.com/mehdiazizian/liqo-resource-broker/api/v1alpha1"
	"github.com/mehdiazizian/liqo-resource-broker/internal/broker"
	"github.com/mehdiazizian/liqo-resource-broker/internal/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ReservationReconciler reconciles a Reservation object
type ReservationReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	DecisionEngine *broker.DecisionEngine
}

// +kubebuilder:rbac:groups=broker.fluidos.eu,resources=reservations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=broker.fluidos.eu,resources=reservations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=broker.fluidos.eu,resources=reservations/finalizers,verbs=update
// +kubebuilder:rbac:groups=broker.fluidos.eu,resources=clusteradvertisements,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop
func (r *ReservationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling Reservation", "name", req.Name, "namespace", req.Namespace)

	// Fetch the Reservation instance
	reservation := &brokerv1alpha1.Reservation{}
	err := r.Get(ctx, req.NamespacedName, reservation)
	if err != nil {
		if client.IgnoreNotFound(err) == nil {
			logger.Info("Reservation not found, may have been deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get Reservation")
		return ctrl.Result{}, err
	}

	// Handle deletion with finalizer
	if reservation.ObjectMeta.DeletionTimestamp != nil {
		if controllerutil.ContainsFinalizer(reservation, brokerv1alpha1.ReservationFinalizer) {
			// Release resources before deletion
			if err := r.releaseResources(ctx, reservation, logger); err != nil {
				logger.Error(err, "Failed to release resources")
				return ctrl.Result{}, err
			}

			// Remove finalizer
			controllerutil.RemoveFinalizer(reservation, brokerv1alpha1.ReservationFinalizer)
			if err := r.Update(ctx, reservation); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(reservation, brokerv1alpha1.ReservationFinalizer) {
		controllerutil.AddFinalizer(reservation, brokerv1alpha1.ReservationFinalizer)
		if err := r.Update(ctx, reservation); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Handle different phases
	switch reservation.Status.Phase {
	case "": // New reservation
		return r.handlePendingReservation(ctx, reservation, logger)

	case brokerv1alpha1.ReservationPhasePending:
		return r.handlePendingReservation(ctx, reservation, logger)

	case brokerv1alpha1.ReservationPhaseReserved:
		return r.handleReservedReservation(ctx, reservation, logger)

	case brokerv1alpha1.ReservationPhaseActive:
		return r.handleActiveReservation(ctx, reservation, logger)

	case brokerv1alpha1.ReservationPhaseFailed, brokerv1alpha1.ReservationPhaseReleased:
		// Terminal states - no action needed
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

// handlePendingReservation processes a new reservation request
func (r *ReservationReconciler) handlePendingReservation(
	ctx context.Context,
	reservation *brokerv1alpha1.Reservation,
	logger logr.Logger,
) (ctrl.Result, error) {

	// If TargetClusterID is already specified, use it
	if reservation.Spec.TargetClusterID != "" {
		return r.reserveInTargetCluster(ctx, reservation, logger)
	}

	// Otherwise, select best cluster based on decision engine
	bestCluster, err := r.DecisionEngine.SelectBestCluster(
		ctx,
		reservation.Spec.RequesterID,
		reservation.Spec.RequestedResources.CPU,
		reservation.Spec.RequestedResources.Memory,
	)

	if err != nil {
		logger.Error(err, "Failed to select cluster")
		reservation.Status.Phase = brokerv1alpha1.ReservationPhaseFailed
		reservation.Status.Message = fmt.Sprintf("Failed to find suitable cluster: %v", err)
		reservation.Status.LastUpdateTime = metav1.Now()

		if err := r.Status().Update(ctx, reservation); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Update reservation with selected cluster
	reservation.Spec.TargetClusterID = bestCluster.Spec.ClusterID
	if err := r.Update(ctx, reservation); err != nil {
		logger.Error(err, "Failed to update reservation with target cluster")
		return ctrl.Result{}, err
	}

	return r.reserveInTargetCluster(ctx, reservation, logger)
}

// reserveInTargetCluster attempts to reserve resources in the target cluster
func (r *ReservationReconciler) reserveInTargetCluster(
	ctx context.Context,
	reservation *brokerv1alpha1.Reservation,
	logger logr.Logger,
) (ctrl.Result, error) {

	// Find target cluster
	clusterAdv := &brokerv1alpha1.ClusterAdvertisement{}
	clusterList := &brokerv1alpha1.ClusterAdvertisementList{}

	if err := r.List(ctx, clusterList); err != nil {
		return ctrl.Result{}, err
	}

	found := false
	for i := range clusterList.Items {
		if clusterList.Items[i].Spec.ClusterID == reservation.Spec.TargetClusterID {
			clusterAdv = &clusterList.Items[i]
			found = true
			break
		}
	}

	if !found {
		reservation.Status.Phase = brokerv1alpha1.ReservationPhaseFailed
		reservation.Status.Message = "Target cluster not found"
		reservation.Status.LastUpdateTime = metav1.Now()

		if err := r.Status().Update(ctx, reservation); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Check if cluster has enough available resources (using resource calculator)
	if !resource.CanReserve(
		clusterAdv,
		reservation.Spec.RequestedResources.CPU,
		reservation.Spec.RequestedResources.Memory,
	) {
		reservation.Status.Phase = brokerv1alpha1.ReservationPhaseFailed
		reservation.Status.Message = "Insufficient resources in target cluster"
		reservation.Status.LastUpdateTime = metav1.Now()

		if err := r.Status().Update(ctx, reservation); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// LOCK RESOURCES: Add to reserved
	err := resource.AddReservation(
		clusterAdv,
		reservation.Spec.RequestedResources.CPU,
		reservation.Spec.RequestedResources.Memory,
	)
	if err != nil {
		logger.Error(err, "Failed to add reservation to cluster")
		reservation.Status.Phase = brokerv1alpha1.ReservationPhaseFailed
		reservation.Status.Message = fmt.Sprintf("Failed to lock resources: %v", err)
		reservation.Status.LastUpdateTime = metav1.Now()

		if err := r.Status().Update(ctx, reservation); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Update the cluster advertisement with new reserved resources
	if err := r.Update(ctx, clusterAdv); err != nil {
		logger.Error(err, "Failed to update cluster advertisement")
		// Try to rollback the reservation
		_ = resource.RemoveReservation(
			clusterAdv,
			reservation.Spec.RequestedResources.CPU,
			reservation.Spec.RequestedResources.Memory,
		)

		reservation.Status.Phase = brokerv1alpha1.ReservationPhaseFailed
		reservation.Status.Message = "Failed to lock resources in cluster"
		reservation.Status.LastUpdateTime = metav1.Now()

		if err := r.Status().Update(ctx, reservation); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Mark as reserved
	now := metav1.Now()
	reservation.Status.Phase = brokerv1alpha1.ReservationPhaseReserved
	reservation.Status.Message = fmt.Sprintf("Resources locked in cluster %s", reservation.Spec.TargetClusterID)
	reservation.Status.ReservedAt = &now

	// Set expiration if duration is specified
	if reservation.Spec.Duration != nil {
		expiresAt := metav1.NewTime(now.Add(reservation.Spec.Duration.Duration))
		reservation.Status.ExpiresAt = &expiresAt
	}

	reservation.Status.LastUpdateTime = now

	if err := r.Status().Update(ctx, reservation); err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("Resources successfully locked",
		"targetCluster", reservation.Spec.TargetClusterID,
		"cpu", reservation.Spec.RequestedResources.CPU.String(),
		"memory", reservation.Spec.RequestedResources.Memory.String(),
		"availableAfter", clusterAdv.Spec.Resources.Available.CPU.String())

	return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
}

// handleReservedReservation manages a reserved reservation
func (r *ReservationReconciler) handleReservedReservation(
	ctx context.Context,
	reservation *brokerv1alpha1.Reservation,
	logger logr.Logger,
) (ctrl.Result, error) {

	// Check if expired
	if reservation.Status.ExpiresAt != nil && time.Now().After(reservation.Status.ExpiresAt.Time) {
		logger.Info("Reservation expired, releasing resources")

		// Release resources
		if err := r.releaseResources(ctx, reservation, logger); err != nil {
			logger.Error(err, "Failed to release resources on expiration")
			return ctrl.Result{}, err
		}

		reservation.Status.Phase = brokerv1alpha1.ReservationPhaseReleased
		reservation.Status.Message = "Reservation expired and resources released"
		reservation.Status.LastUpdateTime = metav1.Now()

		if err := r.Status().Update(ctx, reservation); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Still valid, check again in 1 minute
	return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
}

// handleActiveReservation manages an active reservation
func (r *ReservationReconciler) handleActiveReservation(
	ctx context.Context,
	reservation *brokerv1alpha1.Reservation,
	logger logr.Logger,
) (ctrl.Result, error) {

	// Check if expired
	if reservation.Status.ExpiresAt != nil && time.Now().After(reservation.Status.ExpiresAt.Time) {
		logger.Info("Active reservation expired, releasing resources")

		// Release resources
		if err := r.releaseResources(ctx, reservation, logger); err != nil {
			logger.Error(err, "Failed to release resources on expiration")
			return ctrl.Result{}, err
		}

		reservation.Status.Phase = brokerv1alpha1.ReservationPhaseReleased
		reservation.Status.Message = "Reservation expired and released"
		reservation.Status.LastUpdateTime = metav1.Now()

		if err := r.Status().Update(ctx, reservation); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
}

// releaseResources releases reserved resources when reservation is deleted
func (r *ReservationReconciler) releaseResources(
	ctx context.Context,
	reservation *brokerv1alpha1.Reservation,
	logger logr.Logger,
) error {
	// Only release if reservation was actually reserved
	if reservation.Status.Phase != brokerv1alpha1.ReservationPhaseReserved &&
		reservation.Status.Phase != brokerv1alpha1.ReservationPhaseActive {
		return nil
	}

	// Find the cluster advertisement
	clusterList := &brokerv1alpha1.ClusterAdvertisementList{}
	if err := r.List(ctx, clusterList); err != nil {
		return err
	}

	var targetCluster *brokerv1alpha1.ClusterAdvertisement
	for i := range clusterList.Items {
		if clusterList.Items[i].Spec.ClusterID == reservation.Spec.TargetClusterID {
			targetCluster = &clusterList.Items[i]
			break
		}
	}

	if targetCluster == nil {
		logger.Info("Target cluster not found, skipping resource release")
		return nil
	}

	// Release the resources
	err := resource.RemoveReservation(
		targetCluster,
		reservation.Spec.RequestedResources.CPU,
		reservation.Spec.RequestedResources.Memory,
	)
	if err != nil {
		return fmt.Errorf("failed to remove reservation: %w", err)
	}

	// Update the cluster advertisement
	if err := r.Update(ctx, targetCluster); err != nil {
		return fmt.Errorf("failed to update cluster after releasing resources: %w", err)
	}

	logger.Info("Successfully released resources",
		"cluster", reservation.Spec.TargetClusterID,
		"cpu", reservation.Spec.RequestedResources.CPU.String(),
		"memory", reservation.Spec.RequestedResources.Memory.String())

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ReservationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Initialize decision engine if not set
	if r.DecisionEngine == nil {
		r.DecisionEngine = &broker.DecisionEngine{
			Client: r.Client,
		}
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&brokerv1alpha1.Reservation{}).
		Named("reservation").
		Complete(r)
}

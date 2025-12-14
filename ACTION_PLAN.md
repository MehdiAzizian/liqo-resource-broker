# Action Plan: Fixing Critical Issues

This document provides **exact code changes** needed to align your implementation with the desired flow.

---

## 🔴 Critical Fix #1: Add --cluster-id Flag to Agent

### Current Problem
Agent uses kube-system namespace UID as cluster identifier:
```go
// agent/internal/metrics/collector.go:37
func (c *Collector) GetClusterID(ctx context.Context) (string, error) {
    ns, err := c.clientset.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{})
    return string(ns.UID), nil  // Returns "fd32c7d2-7cc6-46e6..."
}
```

### Solution

#### Step 1: Update `collector.go` to accept cluster ID
```go
// agent/internal/metrics/collector.go

type Collector struct {
    clientset kubernetes.Interface
    clusterID string  // Add this field
}

// NewCollector creates a new Collector with explicit cluster ID
func NewCollector(clientset kubernetes.Interface, clusterID string) *Collector {
    return &Collector{
        clientset: clientset,
        clusterID: clusterID,
    }
}

// GetClusterID returns the configured cluster ID
func (c *Collector) GetClusterID() string {
    return c.clusterID
}

// Remove the old GetClusterID(ctx) method that fetches from kube-system
```

#### Step 2: Update `main.go` to add flag
```go
// agent/cmd/main.go

var (
    metricsAddr          string
    enableLeaderElection bool
    probeAddr            string
    secureMetrics        bool
    enableHTTP2          bool
    brokerKubeconfig     string
    clusterID            string  // Add this
)

func init() {
    flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "...")
    flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "...")
    flag.BoolVar(&enableLeaderElection, "leader-elect", false, "...")
    flag.BoolVar(&secureMetrics, "metrics-secure", true, "...")
    flag.BoolVar(&enableHTTP2, "enable-http2", false, "...")
    flag.StringVar(&brokerKubeconfig, "broker-kubeconfig", "", "...")

    // Add new flag
    flag.StringVar(&clusterID, "cluster-id", "",
        "Human-readable cluster identifier (e.g., 'rome', 'paris', 'berlin')")
}

func main() {
    flag.Parse()

    // Validate required flag
    if clusterID == "" {
        setupLog.Error(fmt.Errorf("missing required flag"),
            "--cluster-id must be specified (e.g., --cluster-id=rome)")
        os.Exit(1)
    }

    // Validate format (optional but recommended)
    if !regexp.MustCompile(`^[a-z0-9-]+$`).MatchString(clusterID) {
        setupLog.Error(fmt.Errorf("invalid cluster-id format"),
            "cluster-id must contain only lowercase letters, numbers, and hyphens")
        os.Exit(1)
    }

    setupLog.Info("Starting agent", "clusterID", clusterID)

    // ... rest of setup ...

    // Update collector initialization (around line 175)
    collector := metrics.NewCollector(clientset, clusterID)

    // Update controller setup (around line 185)
    if err = (&controller.AdvertisementReconciler{
        Client:    mgr.GetClient(),
        Scheme:    mgr.GetScheme(),
        Collector: collector,
        Publisher: brokerClient,  // Pass broker client here
    }).SetupWithManager(mgr); err != nil {
        setupLog.Error(err, "unable to create controller", "controller", "Advertisement")
        os.Exit(1)
    }

    // ... rest of main ...
}
```

#### Step 3: Update controller to use new ClusterID
```go
// agent/internal/controller/advertisement_controller.go

func (r *AdvertisementReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    log := log.FromContext(ctx)

    // ... fetch Advertisement ...

    // Get cluster ID from collector (no context needed now)
    clusterID := r.Collector.GetClusterID()

    // Collect resources
    resources, err := r.Collector.CollectClusterResources(ctx)
    if err != nil {
        log.Error(err, "Failed to collect cluster resources")
        return ctrl.Result{}, err
    }

    // Update spec with cluster ID and name
    advertisement.Spec.ClusterID = clusterID
    advertisement.Spec.ClusterName = clusterID  // Or make this a separate flag
    advertisement.Spec.Resources = *resources
    advertisement.Spec.Timestamp = metav1.Now()

    // ... rest of reconciliation ...
}
```

#### Step 4: Update deployment manifest
```yaml
# agent/config/manager/manager.yaml

apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      containers:
      - name: manager
        image: controller:latest
        command:
        - /manager
        args:
        - --leader-elect
        - --health-probe-bind-address=:8081
        - --cluster-id=$(CLUSTER_ID)  # Add this
        - --broker-kubeconfig=/etc/broker/kubeconfig  # If using broker
        env:
        - name: CLUSTER_ID
          value: "rome"  # Can be overridden with kustomize
```

#### Step 5: Create kustomization overlay for different clusters
```yaml
# agent/config/overlays/rome/kustomization.yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
bases:
- ../../default
patches:
- patch: |-
    apiVersion: apps/v1
    kind: Deployment
    metadata:
      name: tesi2-controller-manager
      namespace: tesi2-system
    spec:
      template:
        spec:
          containers:
          - name: manager
            env:
            - name: CLUSTER_ID
              value: "rome"
```

```yaml
# agent/config/overlays/paris/kustomization.yaml
# Same as above but with value: "paris"
```

---

## 🔴 Critical Fix #2: Implement 70/30 Resource/Cost Scoring

### Current Problem
```go
// broker/internal/broker/decision.go:88-100

// Current scoring: 50% CPU + 50% Memory, no cost consideration
cpuUtilization := 1.0 - ((float64(availableCPU.MilliValue())-float64(requestedCPU.MilliValue()))/float64(allocatableCPU.MilliValue()))
memoryUtilization := 1.0 - ((float64(availableMem.Value())-float64(requestedMem.Value()))/float64(allocatableMem.Value()))
score := (1.0 - cpuUtilization*0.5) + (1.0 - memoryUtilization*0.5)
```

### Solution

#### Step 1: Define scoring configuration
```go
// broker/internal/broker/config.go (NEW FILE)

package broker

type ScoringWeights struct {
    ResourceAvailability float64  // Default: 0.70
    Cost                 float64  // Default: 0.30
}

func DefaultScoringWeights() ScoringWeights {
    return ScoringWeights{
        ResourceAvailability: 0.70,
        Cost:                 0.30,
    }
}

// Validate ensures weights sum to 1.0
func (w ScoringWeights) Validate() error {
    sum := w.ResourceAvailability + w.Cost
    if sum < 0.99 || sum > 1.01 {  // Allow small floating point errors
        return fmt.Errorf("scoring weights must sum to 1.0, got %.2f", sum)
    }
    return nil
}
```

#### Step 2: Update DecisionEngine
```go
// broker/internal/broker/decision.go

type DecisionEngine struct {
    Client  client.Client
    Weights ScoringWeights  // Add this field
}

// NewDecisionEngine creates a decision engine with configurable weights
func NewDecisionEngine(client client.Client, weights ScoringWeights) *DecisionEngine {
    if err := weights.Validate(); err != nil {
        // Use defaults if invalid
        weights = DefaultScoringWeights()
    }
    return &DecisionEngine{
        Client:  client,
        Weights: weights,
    }
}

// SelectBestCluster finds the optimal cluster based on weighted scoring
func (de *DecisionEngine) SelectBestCluster(
    ctx context.Context,
    requestedCPU resource.Quantity,
    requestedMemory resource.Quantity,
    requesterID string,
) (*v1alpha1.ClusterAdvertisement, error) {

    // ... existing list logic ...

    var bestCluster *v1alpha1.ClusterAdvertisement
    var bestScore float64 = -1.0

    for i := range clusterAdvList.Items {
        cluster := &clusterAdvList.Items[i]

        // Skip if inactive
        if !cluster.Status.Active {
            continue
        }

        // Skip if this is the requester's own cluster
        if cluster.Spec.ClusterID == requesterID {
            log.V(1).Info("Skipping requester's own cluster",
                "clusterID", cluster.Spec.ClusterID,
                "requesterID", requesterID)
            continue
        }

        // Check if cluster has enough resources
        availableCPU := cluster.Spec.Resources.Available.CPU
        availableMem := cluster.Spec.Resources.Available.Memory

        if availableCPU.Cmp(requestedCPU) < 0 || availableMem.Cmp(requestedMemory) < 0 {
            log.V(1).Info("Cluster has insufficient resources",
                "clusterID", cluster.Spec.ClusterID,
                "availableCPU", availableCPU.String(),
                "requestedCPU", requestedCPU.String(),
                "availableMemory", availableMem.String(),
                "requestedMemory", requestedMemory.String())
            continue
        }

        // Calculate comprehensive score
        score := de.calculateClusterScore(cluster, requestedCPU, requestedMemory)

        log.Info("Cluster scored",
            "clusterID", cluster.Spec.ClusterID,
            "score", fmt.Sprintf("%.2f", score),
            "availableCPU", availableCPU.String(),
            "availableMemory", availableMem.String())

        if score > bestScore {
            bestScore = score
            bestCluster = cluster
        }
    }

    if bestCluster == nil {
        return nil, fmt.Errorf("no suitable cluster found for requested resources")
    }

    log.Info("Selected best cluster",
        "clusterID", bestCluster.Spec.ClusterID,
        "score", fmt.Sprintf("%.2f", bestScore))

    return bestCluster, nil
}

// calculateClusterScore computes weighted score: 70% resources + 30% cost
func (de *DecisionEngine) calculateClusterScore(
    cluster *v1alpha1.ClusterAdvertisement,
    requestedCPU resource.Quantity,
    requestedMemory resource.Quantity,
) float64 {

    // 1. Calculate Resource Score (0-1 scale)
    resourceScore := de.calculateResourceScore(cluster, requestedCPU, requestedMemory)

    // 2. Calculate Cost Score (0-1 scale, higher is better/cheaper)
    costScore := de.calculateCostScore(cluster, requestedCPU, requestedMemory)

    // 3. Weighted combination
    finalScore := (resourceScore * de.Weights.ResourceAvailability) +
                  (costScore * de.Weights.Cost)

    return finalScore * 100.0  // Scale to 0-100 for readability
}

// calculateResourceScore returns 0-1 score based on resource availability
func (de *DecisionEngine) calculateResourceScore(
    cluster *v1alpha1.ClusterAdvertisement,
    requestedCPU resource.Quantity,
    requestedMemory resource.Quantity,
) float64 {

    allocatableCPU := cluster.Spec.Resources.Allocatable.CPU
    allocatableMem := cluster.Spec.Resources.Allocatable.Memory
    availableCPU := cluster.Spec.Resources.Available.CPU
    availableMem := cluster.Spec.Resources.Available.Memory

    // Calculate post-reservation availability percentage
    remainingCPU := availableCPU.DeepCopy()
    remainingCPU.Sub(requestedCPU)

    remainingMem := availableMem.DeepCopy()
    remainingMem.Sub(requestedMemory)

    // CPU availability percentage (0-1)
    cpuAvailPct := 0.0
    if allocatableCPU.MilliValue() > 0 {
        cpuAvailPct = float64(remainingCPU.MilliValue()) / float64(allocatableCPU.MilliValue())
    }

    // Memory availability percentage (0-1)
    memAvailPct := 0.0
    if allocatableMem.Value() > 0 {
        memAvailPct = float64(remainingMem.Value()) / float64(allocatableMem.Value())
    }

    // Balanced CPU + Memory (equal weight)
    resourceScore := (cpuAvailPct * 0.5) + (memAvailPct * 0.5)

    return resourceScore
}

// calculateCostScore returns 0-1 score based on pricing (1 = cheapest)
func (de *DecisionEngine) calculateCostScore(
    cluster *v1alpha1.ClusterAdvertisement,
    requestedCPU resource.Quantity,
    requestedMemory resource.Quantity,
) float64 {

    // If no cost info, return neutral score
    if cluster.Spec.Cost == nil {
        return 0.5  // Neutral score when cost unknown
    }

    // Parse cost per hour (strings to float64)
    cpuCostPerHour, err := strconv.ParseFloat(cluster.Spec.Cost.CPUPerHour, 64)
    if err != nil {
        cpuCostPerHour = 0.0
    }

    memCostPerHourGB, err := strconv.ParseFloat(cluster.Spec.Cost.MemoryPerHour, 64)
    if err != nil {
        memCostPerHourGB = 0.0
    }

    // Calculate hourly cost for this reservation
    cpuCores := float64(requestedCPU.MilliValue()) / 1000.0
    memoryGB := float64(requestedMemory.Value()) / (1024 * 1024 * 1024)

    totalCostPerHour := (cpuCores * cpuCostPerHour) + (memoryGB * memCostPerHourGB)

    // Normalize: Lower cost = higher score
    // This is a simple inverse relationship
    // TODO: Improve by comparing against all clusters' costs
    if totalCostPerHour == 0 {
        return 1.0  // Free is best
    }

    // For now, use inverse: score = 1 / (1 + cost)
    // This maps: $0 -> 1.0, $1 -> 0.5, $10 -> 0.09, etc.
    costScore := 1.0 / (1.0 + totalCostPerHour)

    return costScore
}
```

#### Step 3: Update reservation controller to use new engine
```go
// broker/internal/controller/reservation_controller.go

func (r *ReservationReconciler) SetupWithManager(mgr ctrl.Manager) error {
    // Initialize decision engine with default weights
    r.DecisionEngine = broker.NewDecisionEngine(
        r.Client,
        broker.DefaultScoringWeights(),
    )

    return ctrl.NewControllerManagedBy(mgr).
        For(&v1alpha1.Reservation{}).
        Complete(r)
}
```

#### Step 4: Add cost normalization (ADVANCED)
```go
// broker/internal/broker/decision.go

// calculateCostScoreNormalized compares cost against all clusters
func (de *DecisionEngine) calculateCostScoreNormalized(
    ctx context.Context,
    cluster *v1alpha1.ClusterAdvertisement,
    requestedCPU resource.Quantity,
    requestedMemory resource.Quantity,
) float64 {

    // If no cost info, return neutral
    if cluster.Spec.Cost == nil {
        return 0.5
    }

    // Get current cluster's cost
    currentCost := de.calculateTotalCost(cluster, requestedCPU, requestedMemory)

    // Fetch all clusters to find min/max costs
    var clusterAdvList v1alpha1.ClusterAdvertisementList
    if err := de.Client.List(ctx, &clusterAdvList); err != nil {
        return 0.5  // Fallback
    }

    minCost := currentCost
    maxCost := currentCost

    for i := range clusterAdvList.Items {
        c := &clusterAdvList.Items[i]
        if c.Spec.Cost == nil {
            continue
        }
        cost := de.calculateTotalCost(c, requestedCPU, requestedMemory)
        if cost < minCost {
            minCost = cost
        }
        if cost > maxCost {
            maxCost = cost
        }
    }

    // Normalize: map cost to 0-1 scale (1 = cheapest)
    if maxCost == minCost {
        return 1.0  // All same price
    }

    // Linear normalization: cheapest=1.0, most expensive=0.0
    normalizedScore := 1.0 - ((currentCost - minCost) / (maxCost - minCost))

    return normalizedScore
}

func (de *DecisionEngine) calculateTotalCost(
    cluster *v1alpha1.ClusterAdvertisement,
    requestedCPU resource.Quantity,
    requestedMemory resource.Quantity,
) float64 {
    if cluster.Spec.Cost == nil {
        return 0.0
    }

    cpuCost, _ := strconv.ParseFloat(cluster.Spec.Cost.CPUPerHour, 64)
    memCostGB, _ := strconv.ParseFloat(cluster.Spec.Cost.MemoryPerHour, 64)

    cpuCores := float64(requestedCPU.MilliValue()) / 1000.0
    memoryGB := float64(requestedMemory.Value()) / (1024 * 1024 * 1024)

    return (cpuCores * cpuCost) + (memoryGB * memCostGB)
}
```

---

## 🔴 Critical Fix #3: Populate and Use EndpointURL

### Step 1: Add endpoint discovery to agent
```go
// agent/internal/metrics/collector.go

import (
    "k8s.io/client-go/rest"
)

type Collector struct {
    clientset  kubernetes.Interface
    clusterID  string
    restConfig *rest.Config  // Add this
}

func NewCollector(clientset kubernetes.Interface, clusterID string, restConfig *rest.Config) *Collector {
    return &Collector{
        clientset:  clientset,
        clusterID:  clusterID,
        restConfig: restConfig,
    }
}

// GetEndpointURL returns the cluster's API server endpoint
func (c *Collector) GetEndpointURL() string {
    if c.restConfig == nil {
        return ""
    }
    return c.restConfig.Host
}
```

### Step 2: Update advertisement controller
```go
// agent/internal/controller/advertisement_controller.go

func (r *AdvertisementReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    // ... existing code ...

    // Update spec with endpoint
    advertisement.Spec.ClusterID = r.Collector.GetClusterID()
    advertisement.Spec.ClusterName = r.Collector.GetClusterID()
    advertisement.Spec.Resources = *resources
    advertisement.Spec.Timestamp = metav1.Now()
    advertisement.Spec.EndpointURL = r.Collector.GetEndpointURL()  // Add this

    // ... rest of reconciliation ...
}
```

### Step 3: Update main.go to pass restConfig
```go
// agent/cmd/main.go

func main() {
    // ... existing setup ...

    // Create collector with rest config
    collector := metrics.NewCollector(clientset, clusterID, mgr.GetConfig())

    // ... rest of main ...
}
```

### Step 4: Include endpoint in Reservation status
```go
// broker/api/v1alpha1/reservation_types.go

type ReservationStatus struct {
    Phase                  ReservationPhase `json:"phase,omitempty"`
    Message                string           `json:"message,omitempty"`
    ReservedAt             *metav1.Time     `json:"reservedAt,omitempty"`
    ExpiresAt              *metav1.Time     `json:"expiresAt,omitempty"`
    TargetClusterEndpoint  string           `json:"targetClusterEndpoint,omitempty"`  // Add this
    LastUpdateTime         metav1.Time      `json:"lastUpdateTime,omitempty"`
    Conditions             []metav1.Condition `json:"conditions,omitempty"`
}
```

### Step 5: Update controller to populate endpoint
```go
// broker/internal/controller/reservation_controller.go

func (r *ReservationReconciler) reserveInTargetCluster(...) error {
    // ... existing reservation logic ...

    // After successful reservation
    reservation.Status.Phase = v1alpha1.ReservationPhaseReserved
    reservation.Status.TargetClusterEndpoint = targetCluster.Spec.EndpointURL  // Add this
    reservation.Status.ReservedAt = &now

    // ... rest of function ...
}
```

---

## 🟡 Important Fix #4: Convert Reservation Watcher to Controller

### Current Architecture Problem
```go
// agent/cmd/main.go:220
// Background goroutine - not managed by controller-runtime
go func() {
    if err := watcher.Start(ctx); err != nil {
        setupLog.Error(err, "Reservation watcher failed")
    }
}()
```

### Solution: Create ReservationController

#### Step 1: Create new controller
```go
// agent/internal/controller/reservation_controller.go (NEW FILE)

package controller

import (
    "context"
    "fmt"

    "k8s.io/apimachinery/pkg/runtime"
    "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
    "k8s.io/apimachinery/pkg/runtime/schema"
    ctrl "sigs.k8s.io/controller-runtime"
    "sigs.k8s.io/controller-runtime/pkg/client"
    "sigs.k8s.io/controller-runtime/pkg/log"
    "sigs.k8s.io/controller-runtime/pkg/predicate"
)

// ReservationReconciler watches broker reservations
type ReservationReconciler struct {
    BrokerClient client.Client
    ClusterID    string
    Scheme       *runtime.Scheme
    LiqoPeering  LiqoPeeringInterface  // For triggering peering
}

func (r *ReservationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    log := log.FromContext(ctx)

    // Fetch reservation from broker cluster
    reservation := &unstructured.Unstructured{}
    reservation.SetGroupVersionKind(schema.GroupVersionKind{
        Group:   "broker.fluidos.eu",
        Version: "v1alpha1",
        Kind:    "Reservation",
    })

    if err := r.BrokerClient.Get(ctx, req.NamespacedName, reservation); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }

    // Extract fields
    spec, _, _ := unstructured.NestedMap(reservation.Object, "spec")
    status, _, _ := unstructured.NestedMap(reservation.Object, "status")

    requesterID, _, _ := unstructured.NestedString(spec, "requesterID")
    phase, _, _ := unstructured.NestedString(status, "phase")
    targetClusterID, _, _ := unstructured.NestedString(status, "targetClusterID")
    targetEndpoint, _, _ := unstructured.NestedString(status, "targetClusterEndpoint")

    // Filter: only handle our reservations
    if requesterID != r.ClusterID {
        return ctrl.Result{}, nil
    }

    // Handle reservation phases
    switch phase {
    case "Reserved":
        log.Info("!!! RESERVATION FULFILLED !!!",
            "reservationName", req.Name,
            "targetCluster", targetClusterID,
            "endpoint", targetEndpoint)

        // Extract requested resources
        requestedResources, _, _ := unstructured.NestedMap(spec, "requestedResources")
        cpu, _, _ := unstructured.NestedString(requestedResources, "cpu")
        memory, _, _ := unstructured.NestedString(requestedResources, "memory")

        log.Info("Manager Notification",
            "action", "Use target cluster",
            "targetCluster", targetClusterID,
            "cpu", cpu,
            "memory", memory,
            "endpoint", targetEndpoint)

        // Trigger Liqo peering
        if r.LiqoPeering != nil {
            if err := r.LiqoPeering.EstablishPeering(ctx, targetClusterID, targetEndpoint); err != nil {
                log.Error(err, "Failed to establish Liqo peering")
                return ctrl.Result{RequeueAfter: time.Second * 30}, err
            }
            log.Info("Liqo peering established", "targetCluster", targetClusterID)
        }

    case "Failed":
        log.Info("Reservation failed", "reservationName", req.Name)

    case "Released":
        log.Info("Reservation released", "reservationName", req.Name)
        // TODO: Tear down peering if needed
    }

    return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with manager
func (r *ReservationReconciler) SetupWithManager(mgr ctrl.Manager) error {
    // Create predicate to filter only our reservations
    isOurReservation := predicate.NewPredicateFuncs(func(obj client.Object) bool {
        u, ok := obj.(*unstructured.Unstructured)
        if !ok {
            return false
        }

        spec, _, _ := unstructured.NestedMap(u.Object, "spec")
        requesterID, _, _ := unstructured.NestedString(spec, "requesterID")

        return requesterID == r.ClusterID
    })

    // Create unstructured reservation object for watching
    reservation := &unstructured.Unstructured{}
    reservation.SetGroupVersionKind(schema.GroupVersionKind{
        Group:   "broker.fluidos.eu",
        Version: "v1alpha1",
        Kind:    "Reservation",
    })

    return ctrl.NewControllerManagedBy(mgr).
        For(reservation).
        WithEventFilter(isOurReservation).
        Complete(r)
}
```

#### Step 2: Update main.go
```go
// agent/cmd/main.go

func main() {
    // ... existing setup ...

    // Remove old watcher code:
    // watcher := publisher.NewReservationWatcher(brokerClient)
    // go func() { watcher.Start(ctx) }()

    // Add new controller (if broker enabled)
    if brokerClient != nil {
        brokerMgr, err := ctrl.NewManager(brokerRestConfig, ctrl.Options{
            Scheme: scheme,
            Metrics: metricsserver.Options{BindAddress: "0"},  // Disable metrics for broker manager
            Cache: cache.Options{
                DefaultNamespaces: map[string]cache.Config{
                    "default": {},  // Only watch default namespace
                },
            },
        })
        if err != nil {
            setupLog.Error(err, "unable to create broker manager")
            os.Exit(1)
        }

        if err = (&controller.ReservationReconciler{
            BrokerClient: brokerMgr.GetClient(),
            ClusterID:    clusterID,
            Scheme:       brokerMgr.GetScheme(),
            LiqoPeering:  nil,  // TODO: Implement liqo peering interface
        }).SetupWithManager(brokerMgr); err != nil {
            setupLog.Error(err, "unable to create controller", "controller", "Reservation")
            os.Exit(1)
        }

        // Start broker manager
        go func() {
            setupLog.Info("starting broker manager")
            if err := brokerMgr.Start(ctx); err != nil {
                setupLog.Error(err, "problem running broker manager")
                os.Exit(1)
            }
        }()
    }

    // ... rest of main ...
}
```

---

## 🟡 Important Fix #5: Basic Liqo Integration Stub

### Step 1: Create Liqo peering interface
```go
// agent/internal/liqo/interface.go (NEW FILE)

package liqo

import "context"

// PeeringInterface defines methods for Liqo peering operations
type PeeringInterface interface {
    // EstablishPeering creates peering with target cluster
    EstablishPeering(ctx context.Context, clusterID, endpointURL string) error

    // TearDownPeering removes peering with target cluster
    TearDownPeering(ctx context.Context, clusterID string) error

    // IsPeeringActive checks if peering is established
    IsPeeringActive(ctx context.Context, clusterID string) (bool, error)
}
```

### Step 2: Create stub implementation
```go
// agent/internal/liqo/peering.go (NEW FILE)

package liqo

import (
    "context"
    "fmt"

    "sigs.k8s.io/controller-runtime/pkg/client"
    "sigs.k8s.io/controller-runtime/pkg/log"
)

type LiqoPeeringManager struct {
    Client client.Client
}

func NewLiqoPeeringManager(client client.Client) *LiqoPeeringManager {
    return &LiqoPeeringManager{
        Client: client,
    }
}

// EstablishPeering creates Liqo peering with target cluster
func (l *LiqoPeeringManager) EstablishPeering(ctx context.Context, clusterID, endpointURL string) error {
    log := log.FromContext(ctx)

    log.Info("Establishing Liqo peering",
        "targetCluster", clusterID,
        "endpoint", endpointURL)

    // TODO: Implement actual Liqo peering
    // 1. Create ForeignCluster CR
    // 2. Wait for authentication
    // 3. Create NetworkConfiguration
    // 4. Wait for virtual node

    // For now, just log
    log.Info("Liqo peering would be established here", "clusterID", clusterID)

    return nil
}

func (l *LiqoPeeringManager) TearDownPeering(ctx context.Context, clusterID string) error {
    log := log.FromContext(ctx)
    log.Info("Tearing down Liqo peering", "clusterID", clusterID)

    // TODO: Delete ForeignCluster CR

    return nil
}

func (l *LiqoPeeringManager) IsPeeringActive(ctx context.Context, clusterID string) (bool, error) {
    // TODO: Check ForeignCluster status
    return false, nil
}
```

### Step 3: Integrate with controller
```go
// agent/cmd/main.go

import (
    "github.com/your-org/liqo-resource-agent/internal/liqo"
)

func main() {
    // ... existing setup ...

    // Create Liqo peering manager
    var peeringManager liqo.PeeringInterface
    peeringManager = liqo.NewLiqoPeeringManager(mgr.GetClient())

    // Pass to reservation controller
    if brokerClient != nil {
        // ... broker manager setup ...

        if err = (&controller.ReservationReconciler{
            BrokerClient: brokerMgr.GetClient(),
            ClusterID:    clusterID,
            Scheme:       brokerMgr.GetScheme(),
            LiqoPeering:  peeringManager,  // Pass here
        }).SetupWithManager(brokerMgr); err != nil {
            // ...
        }
    }
}
```

---

## 📋 Summary Checklist

### Must Do (Critical)
- [ ] Add `--cluster-id` flag to agent
- [ ] Update collector to accept cluster ID
- [ ] Update deployment manifests with CLUSTER_ID env var
- [ ] Implement 70/30 resource/cost scoring
- [ ] Add cost score calculation function
- [ ] Test scoring with mock cost data
- [ ] Populate EndpointURL from rest config
- [ ] Include endpoint in Reservation status
- [ ] Update CRD with new status field (`make manifests`)

### Should Do (Important)
- [ ] Convert ReservationWatcher to controller
- [ ] Create broker manager in agent
- [ ] Add Liqo peering interface
- [ ] Implement peering stub
- [ ] Test reservation notification flow
- [ ] Add unit tests for scoring algorithm

### Nice to Have
- [ ] Add validation webhooks
- [ ] Implement actual Liqo ForeignCluster creation
- [ ] Add Prometheus metrics
- [ ] Create cost normalization across all clusters
- [ ] Build demo with 3 mock clusters

---

## 🧪 Testing Plan

### Unit Tests
```go
// broker/internal/broker/decision_test.go

func TestCalculateClusterScore(t *testing.T) {
    tests := []struct {
        name            string
        cluster         *v1alpha1.ClusterAdvertisement
        requestedCPU    resource.Quantity
        requestedMemory resource.Quantity
        weights         ScoringWeights
        expectedScore   float64  // Approximate
    }{
        {
            name: "70/30 weights with cost",
            cluster: &v1alpha1.ClusterAdvertisement{
                Spec: v1alpha1.ClusterAdvertisementSpec{
                    Resources: v1alpha1.ResourceMetrics{
                        Allocatable: v1alpha1.ResourceQuantities{
                            CPU:    resource.MustParse("10"),
                            Memory: resource.MustParse("20Gi"),
                        },
                        Available: v1alpha1.ResourceQuantities{
                            CPU:    resource.MustParse("8"),
                            Memory: resource.MustParse("16Gi"),
                        },
                    },
                    Cost: &v1alpha1.CostInfo{
                        CPUPerHour:    "0.05",
                        MemoryPerHour: "0.01",
                    },
                },
            },
            requestedCPU:    resource.MustParse("2"),
            requestedMemory: resource.MustParse("4Gi"),
            weights: ScoringWeights{
                ResourceAvailability: 0.70,
                Cost:                 0.30,
            },
            expectedScore: 55.0,  // Approximate, test with delta
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            engine := NewDecisionEngine(nil, tt.weights)
            score := engine.calculateClusterScore(tt.cluster, tt.requestedCPU, tt.requestedMemory)

            if math.Abs(score-tt.expectedScore) > 5.0 {
                t.Errorf("Expected score ~%.2f, got %.2f", tt.expectedScore, score)
            }
        })
    }
}
```

### Integration Test
```bash
# Test with 3 clusters: rome, paris, berlin
export CLUSTER_ID=rome
go run ./cmd/main.go --cluster-id=rome --broker-kubeconfig=/path/to/broker

# In another cluster
export CLUSTER_ID=paris
go run ./cmd/main.go --cluster-id=paris --broker-kubeconfig=/path/to/broker

# Create reservation from rome
kubectl apply -f - <<EOF
apiVersion: broker.fluidos.eu/v1alpha1
kind: Reservation
metadata:
  name: test-reservation
spec:
  requesterID: "rome"
  requestedResources:
    cpu: "2"
    memory: "4Gi"
  duration: "1h"
EOF

# Check that rome's agent logs show notification
# Check that reservation status shows targetClusterID
kubectl get reservation test-reservation -o yaml
```

---

This action plan provides **concrete, copy-pasteable code** to fix all critical issues. Start with Priority 1 items for immediate impact.

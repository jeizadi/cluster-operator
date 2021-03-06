package cluster

import (
	"context"
	"github.com/spf13/viper"
	"os"
	"time"

	"github.com/infobloxopen/cluster-operator/kops"
	clusteroperatorv1alpha1 "github.com/infobloxopen/cluster-operator/pkg/apis/clusteroperator/v1alpha1"

	"github.com/infobloxopen/cluster-operator/utils"
	corev1 "k8s.io/api/core/v1"

	// "k8s.io/apimachinery/pkg/api/errors"

	//metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	//"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"

	//"k8s.io/kops/cmd/kops"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

var log = logf.Log.WithName("controller_cluster")

// Add creates a new Cluster Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(cfg ReconcilerConfig) error {
	return add(cfg.Mgr, newReconciler(cfg))
}

type ReconcilerConfig struct {
	Mgr  manager.Manager
	Reap bool
}

func newReconciler(cfg ReconcilerConfig) reconcile.Reconciler {
	return &ReconcileCluster{client: cfg.Mgr.GetClient(), scheme: cfg.Mgr.GetScheme(), reap: cfg.Reap}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("cluster-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	pred := predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {

			if e.MetaNew.GetGeneration() == e.MetaOld.GetGeneration() {
				return false
			}

			return true
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			// Evaluates to false if the object has been confirmed deleted.
			return e.DeleteStateUnknown
		},
	}

	// Watch for changes to primary resource Cluster
	err = c.Watch(&source.Kind{Type: &clusteroperatorv1alpha1.Cluster{}}, &handler.EnqueueRequestForObject{}, pred)
	if err != nil {
		return err
	}

	// TODO(user): Modify this to be the types you create that are owned by the primary resource
	// Watch for changes to secondary resource Pods and requeue the owner Cluster
	err = c.Watch(&source.Kind{Type: &corev1.Pod{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &clusteroperatorv1alpha1.Cluster{},
	})
	if err != nil {
		return err
	}

	return nil
}

// blank assignment to verify that ReconcileCluster implements reconcile.Reconciler
var _ reconcile.Reconciler = &ReconcileCluster{}

// ReconcileCluster reconciles a Cluster object
type ReconcileCluster struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client client.Client
	scheme *runtime.Scheme
	reap   bool
}

// Reconcile reads that state of the cluster for a Cluster object and makes changes based on the state read
// and what is in the Cluster.Spec
//
// Note:
// The Controller will requeue the Request to be processed again if the returned error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
func (r *ReconcileCluster) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	reqLogger := log.WithValues("Request.Namespace", request.Namespace, "Request.Name", request.Name)
	reqLogger.Info("Reconciling Cluster")

	// Fetch the Cluster instance
	instance := &clusteroperatorv1alpha1.Cluster{}
	err := r.client.Get(context.TODO(), request.NamespacedName, instance)
	if err != nil {
		reqLogger.Error(err, "error requesting instance")
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}
	//Finalizer name
	clusterFinalizer := "cluster.finalizer.cluster-operator.infobloxopen.github.com"
	// TODO - We should maybe catch lack of kops configuration earlier in operator startup
	k, err := kops.NewKops()
	if err != nil {
		reqLogger.Error(err, "kops.NewKops Failed")
		return reconcile.Result{}, err
	}

	kc := CheckKopsDefaultConfig(instance.Spec)
	// If the cluster is not waiting for deletion, handle it normally
	if instance.ObjectMeta.DeletionTimestamp.IsZero() {

		// If no phase set default to pending for the initial phase
		if instance.Status.Phase == "" {
			instance.Spec.KopsConfig = CheckKopsDefaultConfig(instance.Spec)
			// The following routine will remove any clusters from the state store that are not in etcd
			// This will run whenever a cluster is created on the state store its beeing created in
			if r.reap == true {
				//get all clusters in etcd (grabbing only from namespace operator is working in, see fix me)
				etcdClusters := &clusteroperatorv1alpha1.ClusterList{}
				err = r.client.List(context.Background(), etcdClusters)
				if err != nil {
					log.Error(err, "Error getting list of clusters")
					os.Exit(1)
				}

				// List clusters in current state store
				ssClusters, err := k.ListClusters(instance.Spec.KopsConfig.StateStore)
				if err != nil {
					reqLogger.Error(err, "Cannot list clusters")
					return reconcile.Result{}, err
				}

				var badClusters []string

				// FIXME This is banking off the fact that the operator only looks for clusters in one
				// namespace. If that is changed, we need to take into account that the cluster we are looking for
				// may exist in etcd, just in a different namespace. Need to look into if this will break or not
				for _, s := range ssClusters {
					found := false
					for _, e := range etcdClusters.Items {
						if s == e.Spec.KopsConfig.Name {
							found = true
						}
					}
					if !found {
						badClusters = append(badClusters, s)
					}
				}

				if badClusters != nil {
					reqLogger.Info("Clusters found in state store (" + instance.Spec.KopsConfig.StateStore + ") that are not in etcd")
					for _, cluster := range badClusters {
						reqLogger.Info("Deleting cluster " + cluster)
						tempKopsConfig := clusteroperatorv1alpha1.KopsConfig{StateStore: instance.Spec.KopsConfig.StateStore, Name: cluster}
						err := k.DeleteCluster(tempKopsConfig)
						if err != nil {
							reqLogger.Error(err, "Cannot delete cluster from stat store")
							return reconcile.Result{}, err
						}
					}

				}
			}

			instance.Status.Phase = clusteroperatorv1alpha1.ClusterPending
			instance.Spec.KopsConfig = kc
		}
		// Add the finalizer and update the object
		if !utils.Contains(instance.ObjectMeta.Finalizers, clusterFinalizer) {
			instance.ObjectMeta.Finalizers = append(instance.ObjectMeta.Finalizers, clusterFinalizer)
			if err := r.client.Update(context.TODO(), instance); err != nil {
				return reconcile.Result{}, err
			}
		}

		//go through the cycle of phases
		//PENDING: CREATING CLUSTER
		reqLogger.Info("Phase: PENDING")
		//creating cluster
		err := k.ReplaceCluster(instance.Spec)

		if err != nil {
			reqLogger.Error(err, "error creating cluster")
			return reconcile.Result{}, err
		}
		reqLogger.Info("Cluster Config Updated")

		instance.Status.Phase = clusteroperatorv1alpha1.ClusterUpdate
		if err := r.client.Status().Update(context.TODO(), instance); err != nil {
			return reconcile.Result{}, err
		}

		//UPDATIG: UPDATING CLUSTER
		reqLogger.Info("Phase: UPDATE")

		err = k.UpdateCluster(kc)

		if err != nil {
			reqLogger.Error(err, "error updating cluster")
			return reconcile.Result{}, err
		}

		reqLogger.Info("Cluster Updated")

		//get kubeconfig
		var mode os.FileMode = 509
		err = os.MkdirAll("./tmp", mode)
		if err != nil {
			return reconcile.Result{}, err
		}

		_, err = os.Create("tmp/config-" + kc.Name)
		if err != nil {
			return reconcile.Result{}, err
		}

		var config clusteroperatorv1alpha1.KubeConfig
		config, err = k.GetKubeConfig(kc)
		if err != nil {
			return reconcile.Result{}, err
		}

		instance.Status.KubeConfig = config
		reqLogger.Info("KUBECONFIG Updated")

		//rolling udpates
		os.Setenv("KUBECONFIG", "tmp/config-"+kc.Name)

		//TODO: Right now, using defaults for intervals. Need to make changable
		// Some changes will require rebuilding the nodes (for example, resizing nodes or changing the AMI)
		// We call rolling-update to apply these changes
		if instance.Status.Validated {
			err = k.RollingUpdateCluster(kc)
			if err != nil {
				reqLogger.Error(err, "error performing rolling update on cluster")
				return reconcile.Result{}, err
			}
			reqLogger.Info("Rolling Update Complete")
		} else {
			reqLogger.Info("Cluster not validated yet... Skipping rolling update for now")
		}

		instance.Status.Phase = clusteroperatorv1alpha1.ClusterSetup
		if err := r.client.Status().Update(context.TODO(), instance); err != nil {
			return reconcile.Result{}, err
		}
		// SETUP: CLUSTER VALIDATION
		reqLogger.Info("Phase: SETUP")

		// Setenv required if not using default .kube/config,
		// the --kubeconfig option does not currently work for kops validate (1.18.2-alpha2)
		os.Setenv("KUBECONFIG", "tmp/config-"+kc.Name)
		status, err := k.ValidateCluster(kc)

		instance.Status.KopsStatus = clusteroperatorv1alpha1.KopsStatus{}
		if err != nil {
			reqLogger.Info("Cluster Not Ready")
			instance.Status.Validated = false
			if err := r.client.Status().Update(context.TODO(), instance); err != nil {
				return reconcile.Result{}, err
			}
			// instance.Status.Phase = clusteroperatorv1alpha1.ClusterPending
		} else if len(status.Nodes) > 0 {
			instance.Status.KopsStatus.Nodes = status.Nodes
			reqLogger.Info("Cluster Created")
			instance.Status.Phase = clusteroperatorv1alpha1.ClusterDone
			instance.Status.Validated = true
			reqLogger.Info("Phase: DONE")
			if err := r.client.Status().Update(context.TODO(), instance); err != nil {
				return reconcile.Result{}, err
			}
			//requeues every ten minutes to make sure its synced if any manual changes were done
			return reconcile.Result{RequeueAfter: time.Minute * 10}, nil
		} else {
			// FIXME - If we get this state try validate again!!!
			reqLogger.Info("Validate Returned Unexpected Result")
			// instance.Status.Phase = clusteroperatorv1alpha1.ClusterPending
		}

		//It did not finish validating, requeue in five minutes
		return reconcile.Result{RequeueAfter: time.Minute * 5}, nil

	} else if utils.Contains(instance.ObjectMeta.Finalizers, clusterFinalizer) {

		//check if cluster still exists
		exists, err := k.GetCluster(instance.Spec.KopsConfig)
		if !exists {
			reqLogger.WithValues("error", err).Info("Cluster is already deleted...")
		} else if err != nil {
			reqLogger.WithValues("error", err).Info("Error getting cluster")
			return reconcile.Result{}, err
		} else {
			err = k.DeleteCluster(instance.Spec.KopsConfig)
			if err != nil {
				//error deleting cluster
				return reconcile.Result{}, err
			}
		}

		// our finalizer is present, so delete cluster first
		// remove our finalizer from the list and update it.
		instance.ObjectMeta.Finalizers = utils.Remove(instance.ObjectMeta.Finalizers, clusterFinalizer)

		if err := r.client.Update(context.TODO(), instance); err != nil {
			return reconcile.Result{}, err
		}

		//TODO: error when resource edited and requeued, but already deleted. Do we want that?

	}
	// Stop reconciliation as the item is being deleted
	return reconcile.Result{}, nil
}

// Get Kops Default Config Resource
func CheckKopsDefaultConfig(c clusteroperatorv1alpha1.ClusterSpec) clusteroperatorv1alpha1.KopsConfig {
	// If KopsConfig is not defined in CR, use default
	// TODO: Right now this only checks if the values are there. Eventually
	// we want to use a few inputs to pull information from the CMDB or
	// another controller that would hold the config information based on
	// the supplied infra info

	// Due to changes to use Kops manifests, the only required fields are Name and StateStore
	defaultConfig := clusteroperatorv1alpha1.KopsConfig{
		Name:        c.Name + "." + viper.GetString("kops.cluster.dns.zone"),
		StateStore:  viper.GetString("kops.state.store"),
		// MasterCount: 1,
		// MasterEc2:   "t2.micro",
		// WorkerCount: 2,
		// WorkerEc2:   "t2.micro",
		// Vpc:         "vpc-0a75b33895655b46a",
		// Zones:       []string{"us-east-2a", "us-east-2b"},
	}

	if c.KopsConfig.MasterCount > 0 {
		defaultConfig.MasterCount = c.KopsConfig.MasterCount
	}

	if len(c.KopsConfig.MasterEc2) != 0 {
		defaultConfig.MasterEc2 = c.KopsConfig.MasterEc2
	}

	if (c.KopsConfig.WorkerCount) > 0 {
		defaultConfig.WorkerCount = c.KopsConfig.WorkerCount
	}

	if len(c.KopsConfig.WorkerEc2) > 0 {
		defaultConfig.WorkerEc2 = c.KopsConfig.WorkerEc2
	}

	if len(c.KopsConfig.Vpc) > 0 {
		defaultConfig.Vpc = c.KopsConfig.Vpc
	}

	if len(c.KopsConfig.Zones) > 0 {
		c.KopsConfig.Zones = c.KopsConfig.Zones
	}

	return defaultConfig
}

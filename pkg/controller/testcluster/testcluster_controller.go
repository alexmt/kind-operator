package testcluster

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"k8s.io/client-go/util/retry"

	"github.com/go-logr/logr"

	testingv1alpha1 "github.com/argoproj/kind-operator/pkg/apis/testing/v1alpha1"
	dockerclient "github.com/docker/docker/client"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	logf "sigs.k8s.io/controller-runtime/pkg/runtime/log"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// Add creates a new TestCluster Controller and adds it to the Manager with default RBAC. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager) error {
	return add(mgr, newReconciler(mgr))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager) reconcile.Reconciler {
	return &ReconcileTestCluster{Client: mgr.GetClient(), scheme: mgr.GetScheme()}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("testcluster-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to TestCluster
	err = c.Watch(&source.Kind{Type: &testingv1alpha1.TestCluster{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &corev1.Pod{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &testingv1alpha1.TestCluster{},
	})
	if err != nil {
		return err
	}

	return nil
}

var _ reconcile.Reconciler = &ReconcileTestCluster{}

// ReconcileTestCluster reconciles a TestCluster object
type ReconcileTestCluster struct {
	client.Client
	scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=,resources=pods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=testing.argoproj.io,resources=testclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=testing.argoproj.io,resources=testclusters/status,verbs=get;update;patch
func (r *ReconcileTestCluster) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	log := logf.Log.WithName("controller").WithValues("testcluster", request.String())
	cluster := &testingv1alpha1.TestCluster{}
	err := r.Get(context.TODO(), request.NamespacedName, cluster)
	if err != nil {
		if errors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	updatedCluster := cluster.DeepCopy()

	log.Info("start reconciling")
	res, err := r.reconcile(request, updatedCluster, log)

	if err != nil {
		return reconcile.Result{}, err
	}

	if !reflect.DeepEqual(cluster.Status, updatedCluster.Status) {
		log.Info("reconciliation successfully completed", "previous phase", cluster.Status.Phase, "next phase", cluster.Status.Phase)
		err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			return r.Update(context.TODO(), updatedCluster)
		})
	} else {
		log.Info("reconciliation successfully completed (no cluster status changes)", "phase", cluster.Status.Phase)
	}

	if err != nil {
		return reconcile.Result{}, err
	}
	return res, nil
}

func (r *ReconcileTestCluster) getDindPod(cluster *testingv1alpha1.TestCluster) (*corev1.Pod, error) {
	privileged := true
	pod := &corev1.Pod{
		ObjectMeta: v1.ObjectMeta{
			Name:      cluster.Name + "-k8s",
			Namespace: cluster.Namespace,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "dind",
					Image: "docker:18.09.1-dind",
					SecurityContext: &corev1.SecurityContext{
						Privileged: &privileged,
					},
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList(map[corev1.ResourceName]resource.Quantity{
							corev1.ResourceMemory: resource.MustParse("2048Mi"),
						}),
					},
				},
			},
		},
	}
	if err := controllerutil.SetControllerReference(cluster, pod, r.scheme); err != nil {
		return nil, err
	}

	found := &corev1.Pod{}
	err := r.Get(context.TODO(), types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, found)
	if err != nil && errors.IsNotFound(err) {
		err = r.Create(context.TODO(), pod)
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else {
		pod = found
	}
	return pod, nil
}

func (r *ReconcileTestCluster) reconcile(request reconcile.Request, cluster *testingv1alpha1.TestCluster, log logr.Logger) (reconcile.Result, error) {

	if cluster.Status.Phase == testingv1alpha1.ClusterReady && cluster.Status.KubeConfig != "" {
		log.Info("cluster is ready, skipping reconciliation")
		return reconcile.Result{}, nil
	}

	cluster.Status.Message = ""

	pod, err := r.getDindPod(cluster)
	if err != nil {
		cluster.Status.Phase = testingv1alpha1.ClusterFailed
		cluster.Status.Message = fmt.Sprintf("Unable to get cluster pod: %v", err)
		return reconcile.Result{}, nil
	}

	if pod.DeletionTimestamp != nil {
		cluster.Status.Phase = testingv1alpha1.ClusterFailed
		cluster.Status.Message = "Cluster pod is being deleted"
		return reconcile.Result{}, nil
	}
	switch pod.Status.Phase {
	case corev1.PodRunning:
		if pod.Status.PodIP == "" {
			cluster.Status.Phase = testingv1alpha1.ClusterPending
			cluster.Status.Message = fmt.Sprintf("Waiting for IP of pod '%s'", pod.Name)
			return reconcile.Result{}, nil
		}
		for _, ctrStatus := range pod.Status.ContainerStatuses {
			if !ctrStatus.Ready {
				cluster.Status.Phase = testingv1alpha1.ClusterPending
				cluster.Status.Message = fmt.Sprintf("Waiting for container '%s' to become ready", ctrStatus.Name)
				return reconcile.Result{}, nil
			}
		}
	case corev1.PodPending:
		cluster.Status.Phase = testingv1alpha1.ClusterPending
		cluster.Status.Message = fmt.Sprintf("Waiting for pod '%s' to start", pod.Name)
		return reconcile.Result{}, nil
	case corev1.PodSucceeded:
	case corev1.PodFailed:
	default:
		cluster.Status.Phase = testingv1alpha1.ClusterFailed
		cluster.Status.Message = fmt.Sprintf("Pod '%s' unexpectedly moved into '%s' phase", pod.Name, pod.Status.Phase)
		return reconcile.Result{}, nil
	}

	log.Info("dind pod is ready, checking docker status")

	dockerHost := fmt.Sprintf("tcp://%s:2375", pod.Status.PodIP)
	docker, err := dockerclient.NewClient(dockerHost, "", nil, nil)
	if err != nil {
		return reconcile.Result{}, err
	}
	_, err = docker.Info(context.TODO())
	if err != nil {
		cluster.Status.Phase = testingv1alpha1.ClusterPending
		cluster.Status.Message = fmt.Sprintf("Waiting for docker to start on pod '%s'", pod.Name)
		return reconcile.Result{RequeueAfter: time.Second}, nil
	}

	log.Info("docker is running, checking k8s status")
	annotations := pod.Annotations
	if annotations == nil {
		annotations = make(map[string]string)
	}

	if errorStr, ok := annotations[testingv1alpha1.AnnotationK8SCreateError]; ok {
		log.Info("cluster creation already attempted and failed")
		cluster.Status.Phase = testingv1alpha1.ClusterFailed
		cluster.Status.Message = errorStr
		return reconcile.Result{}, nil
	}

	if _, ok := annotations[testingv1alpha1.AnnotationK8SCreateTime]; !ok {
		pod.Annotations = annotations
		log.Info("creating kubernetes cluster", "image", cluster.Spec.Image)
		out, err := r.createCluster(cluster, dockerHost, err)
		if err != nil {
			errorStr := fmt.Sprintf("Failed to create cluster: %v; '%s'", err, out)
			log.Info("cluster creation failed", "error", errorStr)
			cluster.Status.Phase = testingv1alpha1.ClusterFailed
			cluster.Status.Message = errorStr
			pod.Annotations[testingv1alpha1.AnnotationK8SCreateError] = errorStr
			return reconcile.Result{}, nil
		} else {
			log.Info("cluster successfully created")
			annotations[testingv1alpha1.AnnotationK8SCreateTime] = time.Now().String()
		}
		err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			return r.Update(context.TODO(), pod)
		})
		if err != nil {
			cluster.Status.Phase = testingv1alpha1.ClusterFailed
			cluster.Status.Message = fmt.Sprintf("Failed to update cluster pod: %v", err)
			return reconcile.Result{}, nil
		}

		out, err = r.getKubeConfig(cluster, pod)
		if err != nil {
			errorStr := fmt.Sprintf("Failed to get kubeconfig: %v; '%s'", err, out)
			log.Info("cluster creation failed", "error", errorStr)
			cluster.Status.Phase = testingv1alpha1.ClusterFailed
			cluster.Status.Message = errorStr
			return reconcile.Result{}, nil
		}

		log.Info("successfully got kubeconfig")
		cluster.Status.Phase = testingv1alpha1.ClusterReady
		cluster.Status.KubeConfig = out

	} else {
		log.Info("cluster already created")
	}

	return reconcile.Result{}, nil
}

func (r *ReconcileTestCluster) getKubeConfig(cluster *testingv1alpha1.TestCluster, pod *corev1.Pod) (string, error) {
	cmd := exec.Command("sh", "-c", fmt.Sprintf(`cat $(kind get kubeconfig-path --name %s) | sed -e 's/https\:\/\/localhost/https\:\/\/%s/g'`, cluster.Name, pod.Status.PodIP))
	cmd.Env = os.Environ()
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

func (r *ReconcileTestCluster) createCluster(cluster *testingv1alpha1.TestCluster, dockerHost string, err error) (string, error) {
	cmd := exec.Command("kind", "create", "cluster", "--image", cluster.Spec.Image, "--name", cluster.Name)
	cmd.Env = append(os.Environ(), fmt.Sprintf("DOCKER_HOST=%s", dockerHost))
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err = cmd.Run()
	return out.String(), err
}

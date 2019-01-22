package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestClusterSpec defines the desired state of TestCluster
type TestClusterSpec struct {
	Image string `json:"image,omitempty"`
}

type ClusterPhase string

const (
	AnnotationK8SCreateTime  = "testing.argoproj.io/k8s-create-time"
	AnnotationK8SCreateError = "testing.argoproj.io/k8s-create-error"
)

const (
	ClusterPending = "Pending"
	ClusterFailed  = "Failed"
	ClusterReady   = "Ready"
)

// TestClusterStatus defines the observed state of TestCluster
type TestClusterStatus struct {
	Phase      ClusterPhase `json:"phase,omitempty"`
	Message    string       `json:"message,omitempty"`
	KubeConfig string       `json:"kubeconfig,omitempty"`
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +k8s:openapi-gen=true
// TestCluster is the Schema for the testclusters API
type TestCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TestClusterSpec   `json:"spec,omitempty"`
	Status TestClusterStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// TestClusterList contains a list of TestCluster
type TestClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TestCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TestCluster{}, &TestClusterList{})
}

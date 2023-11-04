/*
Copyright 2023.

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

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.
const (
	SuccessState        = "success"
	RunningState        = "running"
	FailedState         = "failed"
	ResetState          = "reset"
	NextGenRunningState = "next-gen-running"
	OriginRunningState  = "orgin-running"

	ScaleInPriorityUpgradeMode     = "scaleInPriority"
	ScaleOutPriorityUpgradeMode    = "scaleOutPriority"
	ScaleSimultaneouslyUpgradeMode = "scaleSimultaneously"

	OriginNameField  = ".spec.originDeployName"
	NextGenNameField = ".spec.nextDeployName"
)

// DeploymentUpgradeSpec defines the desired state of DeploymentUpgrade
type DeploymentUpgradeSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// +kubebuilder:default="scaleOutPriority"
	Mode string `json:"mode"`
	// +kubebuilder:validation:Minimum=1
	Step int `json:"step"`

	// +kubebuilder:validation:Minimum=0
	OriginStageTarget int `json:"originStageTarget"`
	// +kubebuilder:validation:Minimum=0
	NextGenStageTarget int `json:"nextGenStageTarget"`

	// +kubebuilder:validation:Required
	OriginDeployName string `json:"originDeployName"`
	// +kubebuilder:validation:Required
	OriginDeployNamespace string `json:"originDeployNamespace"`
	// +kubebuilder:validation:Required
	NextDeployName string `json:"nextDeployName"`
	// +kubebuilder:validation:Required
	NextDeployNamespace string `json:"nextDeployNamespace"`
}

// DeploymentUpgradeStatus defines the observed state of DeploymentUpgrade
type DeploymentUpgradeStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	State    string `json:"state"`
	UpdateAt string `json:"updateAt"`

	DeployStatus DeployStatus  `json:"deployStatus"`
	Config       CurrentConfig `json:"config"`
}

type DeployStatus struct {
	OriginReady    int `json:"originReady"`
	NextGenReady   int `json:"nextGenReady"`
	OriginReplica  int `json:"originReplica"`
	NextGenReplica int `json:"nextGenReplica"`
	TotalReady     int `json:"totalReady"`
}

// Upgrade
type CurrentConfig struct {
	Mode string `json:"mode"`

	OriginStageReplica  int `json:"originStageTarget"`
	NextGenStageReplica int `json:"nextGenStageTarget"`
	OriginStepReplica   int `json:"originStepReplica"`
	NextGenStepReplica  int `json:"nextGenStepReplica"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// DeploymentUpgrade is the Schema for the deploymentupgrades API
type DeploymentUpgrade struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DeploymentUpgradeSpec   `json:"spec,omitempty"`
	Status DeploymentUpgradeStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// DeploymentUpgradeList contains a list of DeploymentUpgrade
type DeploymentUpgradeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DeploymentUpgrade `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DeploymentUpgrade{}, &DeploymentUpgradeList{})
}

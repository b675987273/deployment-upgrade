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

package controller

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	upgradev1 "github.com/b675987273/deployment-upgrade/api/v1"
)

// DeploymentUpgradeReconciler reconciles a DeploymentUpgrade object
type DeploymentUpgradeReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=upgrade.github.com,resources=deploymentupgrades,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=upgrade.github.com,resources=deploymentupgrades/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=upgrade.github.com,resources=deploymentupgrades/finalizers,verbs=update
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=apps,resources=deployments/status,verbs=get

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the DeploymentUpgrade object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.14.4/pkg/reconcile
func (r *DeploymentUpgradeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	logger := log.Log.WithValues("ns/name", req.NamespacedName.String())

	var deployUpgradeInK8s upgradev1.DeploymentUpgrade
	if err := r.Get(ctx, req.NamespacedName, &deployUpgradeInK8s); err != nil {
		logger.Error(err, "unable to fetch AppUpgrade")
		// we'll ignore not-found errors, since they can't be fixed by an immediate
		// requeue (we'll need to wait for a new notification), and we can get them
		// on deleted requests.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !deployUpgradeInK8s.DeletionTimestamp.IsZero() {
		logger.Info("deploy upgrade deleted.")
		return ctrl.Result{}, nil
	}

	logger = logger.WithValues("origin-deploy", deployUpgradeInK8s.Spec.OriginDeployName, "origin-ns", deployUpgradeInK8s.Spec.OriginDeployNamespace,
		"next-gen-deploy", deployUpgradeInK8s.Spec.NextDeployName, "next-gen-ns", deployUpgradeInK8s.Spec.NextDeployNamespace)

	// check if config changed, then reset for new config
	if r.IsUpgradeConfigChange(&deployUpgradeInK8s) {
		if err := r.ResetUpgrade(&deployUpgradeInK8s); err != nil {
			logger.Error(err, "reset deploy upgrade config failed.")
			return ctrl.Result{}, err
		}
	}

	latestStatus, err := r.CalculateCurrStatus(ctx, &deployUpgradeInK8s)
	if err != nil {
		logger.Error(err, "calculate current status failed")
		return ctrl.Result{}, err
	}
	logger.Info("get new status.", "status", latestStatus)

	// first init or reset will call a recalculate
	// it means base on current deploy situation, refresh all config
	// reset is equal to abandon all the previous config
	// in service upgrade case, reset is emergency
	// when upgrade is hanged up on some case, reset help return to normal state
	if deployUpgradeInK8s.Status.State == upgradev1.ResetState ||
		deployUpgradeInK8s.Status.State == "" {
		logger.Info("reset then next stage.")
		if err := r.NextStage(&deployUpgradeInK8s, latestStatus); err != nil {
			logger.Error(err, "unable to reset stage")
			return ctrl.Result{}, err
		}
		deployUpgradeInK8s.Status = *latestStatus
		if err := r.UpdateUpgradeStatus(&deployUpgradeInK8s); err != nil {
			logger.Error(err, "unable to update deployUpgrade status.")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	if latestStatus.State == upgradev1.SuccessState {
		err := r.CheckForceReset(ctx, &deployUpgradeInK8s, latestStatus)
		if err != nil {
			logger.Error(err, "force reset failed")
			return ctrl.Result{}, err
		}
		logger.Info("success then stop.")
	} else if latestStatus.State == upgradev1.FailedState {
		logger.Error(fmt.Errorf("upgrade failed"), "deployment failed found")
	} else {
		logger.Info("keep running stage.")
		if err := r.Continue(ctx, &deployUpgradeInK8s, latestStatus); err != nil {
			logger.Error(err, "unable to continue")
			return ctrl.Result{}, err
		}
	}

	if !r.CompareUpgradeStatus(&deployUpgradeInK8s, latestStatus) {
		logger.Info("skip unchange update.")
		return ctrl.Result{}, nil
	}
	logger.Info("try update status.", "old-status", deployUpgradeInK8s.Status,
		"new-status", latestStatus)

	deployUpgradeInK8s.Status = *latestStatus
	if err := r.UpdateUpgradeStatus(&deployUpgradeInK8s); err != nil {
		logger.Error(err, "unable to update deployUpgrade status.")
		return ctrl.Result{}, err
	}

	return ctrl.Result{
		RequeueAfter: 5 * time.Second,
	}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *DeploymentUpgradeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &upgradev1.DeploymentUpgrade{}, upgradev1.OriginNameField, func(rawObj client.Object) []string {
		upgradeObj := rawObj.(*upgradev1.DeploymentUpgrade)
		return []string{upgradeObj.Spec.OriginDeployName, upgradeObj.Spec.NextDeployName}
	}); err != nil {
		return err
	}
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &upgradev1.DeploymentUpgrade{}, upgradev1.NextGenNameField, func(rawObj client.Object) []string {
		upgradeObj := rawObj.(*upgradev1.DeploymentUpgrade)
		return []string{upgradeObj.Spec.OriginDeployName, upgradeObj.Spec.NextDeployName}
	}); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&upgradev1.DeploymentUpgrade{}).
		Owns(&appsv1.Deployment{}).
		Watches(
			&source.Kind{Type: &appsv1.Deployment{}},
			handler.EnqueueRequestsFromMapFunc(r.findObjectsForDeploy),
		).
		Complete(r)
}

func (r *DeploymentUpgradeReconciler) IsUpgradeConfigChange(deployUpgradeInK8s *upgradev1.DeploymentUpgrade) bool {
	if deployUpgradeInK8s.Status.Config.NextGenStageReplica != deployUpgradeInK8s.Spec.NextGenStageTarget ||
		deployUpgradeInK8s.Status.Config.OriginStageReplica != deployUpgradeInK8s.Spec.OriginStageTarget ||
		deployUpgradeInK8s.Status.Config.Mode != deployUpgradeInK8s.Spec.Mode {
		log.Log.Info("reset upgrade", "origin-deploy", deployUpgradeInK8s.Spec.OriginDeployName, "origin-ns", deployUpgradeInK8s.Spec.OriginDeployNamespace,
			"next-gen-deploy", deployUpgradeInK8s.Spec.NextDeployName, "next-gen-ns", deployUpgradeInK8s.Spec.NextDeployNamespace)
		return true
	}
	return false
}

func (r *DeploymentUpgradeReconciler) ResetUpgrade(deployUpgradeInK8s *upgradev1.DeploymentUpgrade) error {
	deployUpgradeInK8s.Status.Config.OriginStageReplica = deployUpgradeInK8s.Spec.OriginStageTarget
	deployUpgradeInK8s.Status.Config.NextGenStageReplica = deployUpgradeInK8s.Spec.NextGenStageTarget
	deployUpgradeInK8s.Status.Config.Mode = deployUpgradeInK8s.Spec.Mode
	deployUpgradeInK8s.Status.State = upgradev1.ResetState
	log.Log.Info("try reset status force.", "origin-deploy", deployUpgradeInK8s.Spec.OriginDeployName, "origin-ns", deployUpgradeInK8s.Spec.OriginDeployNamespace,
		"next-gen-deploy", deployUpgradeInK8s.Spec.NextDeployName, "next-gen-ns", deployUpgradeInK8s.Spec.NextDeployNamespace)
	if err := r.UpdateUpgradeStatus(deployUpgradeInK8s); err != nil {
		log.Log.Error(err, "unable to update deployUpgrade status when flush status.")
		return err
	}
	return nil
}

func (r *DeploymentUpgradeReconciler) CompareUpgradeStatus(deployUpgradeInK8s *upgradev1.DeploymentUpgrade, latestStatus *upgradev1.DeploymentUpgradeStatus) bool {
	if deployUpgradeInK8s.Status.DeployStatus.NextGenReady != latestStatus.DeployStatus.NextGenReady {
		return true
	}
	if deployUpgradeInK8s.Status.DeployStatus.OriginReady != latestStatus.DeployStatus.OriginReady {
		return true
	}
	if deployUpgradeInK8s.Status.DeployStatus.OriginReplica != latestStatus.DeployStatus.OriginReplica {
		return true
	}
	if deployUpgradeInK8s.Status.DeployStatus.NextGenReplica != latestStatus.DeployStatus.NextGenReplica {
		return true
	}
	if deployUpgradeInK8s.Status.DeployStatus.TotalReady != latestStatus.DeployStatus.TotalReady {
		return true
	}
	if deployUpgradeInK8s.Status.Config.OriginStepReplica != latestStatus.Config.OriginStepReplica {
		return true
	}
	if deployUpgradeInK8s.Status.Config.NextGenStepReplica != latestStatus.Config.NextGenStepReplica {
		return true
	}
	if deployUpgradeInK8s.Status.State != latestStatus.State {
		return true
	}

	return false
}

func (r *DeploymentUpgradeReconciler) CalculateCurrStatus(ctx context.Context, deployUpgradeInK8s *upgradev1.DeploymentUpgrade) (*upgradev1.DeploymentUpgradeStatus, error) {

	origind := &appsv1.Deployment{}
	err := r.Get(ctx, types.NamespacedName{
		Namespace: deployUpgradeInK8s.Spec.OriginDeployNamespace,
		Name:      deployUpgradeInK8s.Spec.OriginDeployName,
	}, origind)
	if err != nil {
		log.Log.Error(err, "get origin deployment failed")
		return nil, err
	}
	grayd := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{
		Namespace: deployUpgradeInK8s.Spec.NextDeployNamespace,
		Name:      deployUpgradeInK8s.Spec.NextDeployName,
	}, grayd)
	if err != nil {
		log.Log.Error(err, "get next gray deployments failed")
		return nil, err
	}
	var newStatus = deployUpgradeInK8s.Status.DeepCopy()
	newStatus.DeployStatus.NextGenReady = int(grayd.Status.ReadyReplicas)
	newStatus.DeployStatus.OriginReady = int(origind.Status.ReadyReplicas)
	newStatus.DeployStatus.NextGenReplica = int(grayd.Status.Replicas)
	newStatus.DeployStatus.OriginReplica = int(origind.Status.Replicas)
	newStatus.DeployStatus.TotalReady = int(origind.Status.ReadyReplicas) + int(grayd.Status.ReadyReplicas)
	newStatus.State = upgradev1.SuccessState
	latestOriginCondition := &appsv1.DeploymentCondition{}
	latestGrayCondition := &appsv1.DeploymentCondition{}

	// log.Log.Info("deploy debug", "gray", grayd.Status.Conditions, "origin", origind.Status.Conditions)

	// if deply in fail situation will show it on condition
	// only in this case, upgrade will fall in fail state
	for index, condition := range grayd.Status.Conditions {
		if condition.Status != corev1.ConditionTrue {
			continue
		}
		if condition.Type == appsv1.DeploymentReplicaFailure {
			latestGrayCondition = &grayd.Status.Conditions[index]
			break
		}
		if latestGrayCondition.Type == appsv1.DeploymentAvailable {
			continue
		}
		latestGrayCondition = &grayd.Status.Conditions[index]
	}
	for index, condition := range origind.Status.Conditions {
		if condition.Status != corev1.ConditionTrue {
			continue
		}
		if condition.Type == appsv1.DeploymentReplicaFailure {
			latestOriginCondition = &origind.Status.Conditions[index]
			break
		}
		if latestOriginCondition.Type == appsv1.DeploymentAvailable {
			continue
		}
		latestOriginCondition = &origind.Status.Conditions[index]
	}
	// log.Log.Info("deployUpgrade debug", "gray", *latestGrayCondition, "origin", *latestOriginCondition)

	if latestGrayCondition.Type == appsv1.DeploymentReplicaFailure ||
		latestOriginCondition.Type == appsv1.DeploymentReplicaFailure {
		newStatus.State = upgradev1.FailedState
		log.Log.Info(fmt.Sprintf("failed, origin:%d, gray:%v",
			origind.Status.UnavailableReplicas,
			grayd.Status.UnavailableReplicas))
		err := fmt.Errorf("deployemnt faield")
		log.Log.Error(err, latestGrayCondition.Reason+"/"+latestOriginCondition.Reason)
		return newStatus, nil
	}

	if newStatus.Config.OriginStepReplica != newStatus.DeployStatus.OriginReady ||
		newStatus.Config.NextGenStepReplica != newStatus.DeployStatus.NextGenReady {
		newStatus.State = upgradev1.RunningState
		log.Log.Info(fmt.Sprintf("upgrade still running, step lack of origin pod:%d, step lack of gray pod:%v",
			newStatus.Config.OriginStepReplica-newStatus.DeployStatus.OriginReady,
			newStatus.Config.NextGenStepReplica-newStatus.DeployStatus.NextGenReady))
		return newStatus, nil
	}
	if newStatus.Config.OriginStageReplica != newStatus.DeployStatus.OriginReady ||
		newStatus.Config.NextGenStageReplica != newStatus.DeployStatus.NextGenReady {
		newStatus.State = upgradev1.RunningState
		log.Log.Info(fmt.Sprintf("upgrade still running, stage lack of origin pod:%d, stage lack of gray pod:%v",
			newStatus.Config.OriginStageReplica-newStatus.DeployStatus.OriginReady,
			newStatus.Config.NextGenStageReplica-newStatus.DeployStatus.NextGenReady))
		return newStatus, nil
	}

	return newStatus, nil
}

// NextStage will calculate the next step base on the stage replica
// this func need to be run on the stable upgrade situation
func (r *DeploymentUpgradeReconciler) NextStage(deployUpgradeInK8s *upgradev1.DeploymentUpgrade, latestStatus *upgradev1.DeploymentUpgradeStatus) error {
	if deployUpgradeInK8s.Status.Config.OriginStageReplica > latestStatus.DeployStatus.OriginReady {
		latestStatus.Config.OriginStepReplica = deployUpgradeInK8s.Spec.Step + latestStatus.DeployStatus.OriginReady
		if latestStatus.Config.OriginStepReplica > latestStatus.Config.OriginStageReplica {
			latestStatus.Config.OriginStepReplica = latestStatus.Config.OriginStageReplica
		}

	} else if deployUpgradeInK8s.Status.Config.OriginStageReplica < latestStatus.DeployStatus.OriginReady {
		latestStatus.Config.OriginStepReplica = latestStatus.DeployStatus.OriginReady - deployUpgradeInK8s.Spec.Step
		if latestStatus.Config.OriginStepReplica < latestStatus.Config.OriginStageReplica {
			latestStatus.Config.OriginStepReplica = latestStatus.Config.OriginStageReplica
		}
	} else {
		// StepReplica sometime is different from StageReplica and ReadyReplica because of config reset
		latestStatus.Config.OriginStepReplica = deployUpgradeInK8s.Status.Config.OriginStageReplica
	}
	if latestStatus.Config.OriginStepReplica < 0 {
		latestStatus.Config.OriginStepReplica = 0
	}

	if deployUpgradeInK8s.Status.Config.NextGenStageReplica > latestStatus.DeployStatus.NextGenReady {
		latestStatus.Config.NextGenStepReplica = deployUpgradeInK8s.Spec.Step + latestStatus.DeployStatus.NextGenReady

		if latestStatus.Config.NextGenStepReplica > latestStatus.Config.NextGenStageReplica {
			latestStatus.Config.NextGenStepReplica = latestStatus.Config.NextGenStageReplica
		}
	} else if deployUpgradeInK8s.Status.Config.NextGenStageReplica < latestStatus.DeployStatus.NextGenReady {
		latestStatus.Config.NextGenStepReplica = latestStatus.DeployStatus.NextGenReady - deployUpgradeInK8s.Spec.Step
		if latestStatus.Config.NextGenStepReplica < latestStatus.Config.NextGenStageReplica {
			latestStatus.Config.NextGenStepReplica = latestStatus.Config.NextGenStageReplica
		}
	} else {
		latestStatus.Config.NextGenStepReplica = deployUpgradeInK8s.Status.Config.NextGenStageReplica
	}
	if latestStatus.Config.NextGenStepReplica < 0 {
		latestStatus.Config.NextGenStepReplica = 0
	}
	latestStatus.State = upgradev1.RunningState

	return nil
}

func (r *DeploymentUpgradeReconciler) Continue(ctx context.Context,
	deployUpgradeInK8s *upgradev1.DeploymentUpgrade,
	latestStatus *upgradev1.DeploymentUpgradeStatus) error {

	originD := &appsv1.Deployment{}
	err := r.Get(ctx, types.NamespacedName{
		Namespace: deployUpgradeInK8s.Spec.OriginDeployNamespace,
		Name:      deployUpgradeInK8s.Spec.OriginDeployName,
	}, originD)
	if err != nil {
		log.Log.Error(err, "get origin deployment failed")
		return err
	}
	nextGenD := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{
		Namespace: deployUpgradeInK8s.Spec.NextDeployNamespace,
		Name:      deployUpgradeInK8s.Spec.NextDeployName,
	}, nextGenD)
	if err != nil {
		log.Log.Error(err, "get next gray deployments failed")
		return err
	}

	// predict scale mode for origin/next gen  deploy
	var isOriginScaleOut, isOriginScaleIn, isNextGenScaleOut, isNextGenScaleIn bool
	if latestStatus.Config.OriginStepReplica > latestStatus.DeployStatus.OriginReady {
		isOriginScaleOut = true
	}
	if latestStatus.Config.OriginStepReplica < latestStatus.DeployStatus.OriginReady {
		isOriginScaleIn = true
	}
	if latestStatus.Config.NextGenStepReplica > latestStatus.DeployStatus.NextGenReady {
		isNextGenScaleOut = true
	}
	if latestStatus.Config.NextGenStepReplica < latestStatus.DeployStatus.NextGenReady {
		isNextGenScaleIn = true
	}
	// all scale done, this step is done, continue upgrade
	if !(isOriginScaleOut || isNextGenScaleOut || isOriginScaleIn || isNextGenScaleIn) {
		log.Log.Info("all step scaled, next stage", "origin-deploy", deployUpgradeInK8s.Spec.OriginDeployName, "origin-ns", deployUpgradeInK8s.Spec.OriginDeployNamespace,
			"next-gen-deploy", deployUpgradeInK8s.Spec.NextDeployName, "next-gen-ns", deployUpgradeInK8s.Spec.NextDeployNamespace)
		return r.NextStage(deployUpgradeInK8s, latestStatus)
	}
	switch deployUpgradeInK8s.Status.Config.Mode {
	case upgradev1.ScaleSimultaneouslyUpgradeMode:
		// no matter which deploy need scale
		// just modifying simultaneously
		if isNextGenScaleOut || isNextGenScaleIn {
			log.Log.Info("try next gen scale", "origin-deploy", deployUpgradeInK8s.Spec.OriginDeployName, "origin-ns", deployUpgradeInK8s.Spec.OriginDeployNamespace,
				"next-gen-deploy", deployUpgradeInK8s.Spec.NextDeployName, "next-gen-ns", deployUpgradeInK8s.Spec.NextDeployNamespace)
			err := tryUpdateDeploy(r.Client, nextGenD, latestStatus.Config.NextGenStepReplica)
			if err != nil {
				log.Log.Error(err, "deploy update failed")
				return err
			}
			latestStatus.State = upgradev1.NextGenRunningState
		}
		if isOriginScaleOut || isOriginScaleIn {
			log.Log.Info("try origin scale", "origin-deploy", deployUpgradeInK8s.Spec.OriginDeployName, "origin-ns", deployUpgradeInK8s.Spec.OriginDeployNamespace,
				"next-gen-deploy", deployUpgradeInK8s.Spec.NextDeployName, "next-gen-ns", deployUpgradeInK8s.Spec.NextDeployNamespace)
			err := tryUpdateDeploy(r.Client, originD, latestStatus.Config.OriginStepReplica)
			if err != nil {
				log.Log.Error(err, "origin deploy update failed")
				return err
			}
			latestStatus.State = upgradev1.OriginRunningState
		}
		// if someone scale mean this step still in running
		// just return immediately.
		// this code is writed for logic
		if isOriginScaleOut || isNextGenScaleOut || isOriginScaleIn || isNextGenScaleIn {
			return nil
		}

	case upgradev1.ScaleInPriorityUpgradeMode:
		// scale out deploy will hang up
		// until scale in finish
		if isNextGenScaleIn {
			log.Log.Info("try next gen scale in", "origin-deploy", deployUpgradeInK8s.Spec.OriginDeployName, "origin-ns", deployUpgradeInK8s.Spec.OriginDeployNamespace,
				"next-gen-deploy", deployUpgradeInK8s.Spec.NextDeployName, "next-gen-ns", deployUpgradeInK8s.Spec.NextDeployNamespace)
			err := tryUpdateDeploy(r.Client, nextGenD, latestStatus.Config.NextGenStepReplica)
			if err != nil {
				log.Log.Error(err, "next gen deploy update failed")
				return err
			}
			latestStatus.State = upgradev1.NextGenRunningState
		}
		if isOriginScaleIn {
			log.Log.Info("try origin scale in", "origin-deploy", deployUpgradeInK8s.Spec.OriginDeployName, "origin-ns", deployUpgradeInK8s.Spec.OriginDeployNamespace,
				"next-gen-deploy", deployUpgradeInK8s.Spec.NextDeployName, "next-gen-ns", deployUpgradeInK8s.Spec.NextDeployNamespace)
			err := tryUpdateDeploy(r.Client, originD, latestStatus.Config.OriginStepReplica)
			if err != nil {
				log.Log.Error(err, "origin deploy update failed")
				return err
			}
			latestStatus.State = upgradev1.OriginRunningState
		}
		// once either deploy still in scaleIn
		// just wait for it until finish
		if isOriginScaleIn || isNextGenScaleIn {
			return nil
		}
		if isNextGenScaleOut {
			log.Log.Info("try next gen scale out", "origin-deploy", deployUpgradeInK8s.Spec.OriginDeployName, "origin-ns", deployUpgradeInK8s.Spec.OriginDeployNamespace,
				"next-gen-deploy", deployUpgradeInK8s.Spec.NextDeployName, "next-gen-ns", deployUpgradeInK8s.Spec.NextDeployNamespace)
			err := tryUpdateDeploy(r.Client, nextGenD, latestStatus.Config.NextGenStepReplica)
			if err != nil {
				log.Log.Error(err, "deploy update failed")
				return err
			}
			latestStatus.State = upgradev1.NextGenRunningState
		}
		if isOriginScaleOut {
			log.Log.Info("try origin scale out", "origin-deploy", deployUpgradeInK8s.Spec.OriginDeployName, "origin-ns", deployUpgradeInK8s.Spec.OriginDeployNamespace,
				"next-gen-deploy", deployUpgradeInK8s.Spec.NextDeployName, "next-gen-ns", deployUpgradeInK8s.Spec.NextDeployNamespace)
			err := tryUpdateDeploy(r.Client, originD, latestStatus.Config.OriginStepReplica)
			if err != nil {
				log.Log.Error(err, "origin deploy update failed")
				return err
			}
			latestStatus.State = upgradev1.OriginRunningState
		}
		if isOriginScaleOut || isNextGenScaleOut {
			return nil
		}
	case upgradev1.ScaleOutPriorityUpgradeMode:
		// scale in deploy will hang up
		// until scale out finish
		if isNextGenScaleOut {
			log.Log.Info("try next gen scale out", "origin-deploy", deployUpgradeInK8s.Spec.OriginDeployName, "origin-ns", deployUpgradeInK8s.Spec.OriginDeployNamespace,
				"next-gen-deploy", deployUpgradeInK8s.Spec.NextDeployName, "next-gen-ns", deployUpgradeInK8s.Spec.NextDeployNamespace)
			err := tryUpdateDeploy(r.Client, nextGenD, latestStatus.Config.NextGenStepReplica)
			if err != nil {
				log.Log.Error(err, "deploy update failed")
				return err
			}
			latestStatus.State = upgradev1.NextGenRunningState
		}
		if isOriginScaleOut {
			log.Log.Info("try origin scale out", "origin-deploy", deployUpgradeInK8s.Spec.OriginDeployName, "origin-ns", deployUpgradeInK8s.Spec.OriginDeployNamespace,
				"next-gen-deploy", deployUpgradeInK8s.Spec.NextDeployName, "next-gen-ns", deployUpgradeInK8s.Spec.NextDeployNamespace)
			err := tryUpdateDeploy(r.Client, originD, latestStatus.Config.OriginStepReplica)
			if err != nil {
				log.Log.Error(err, "origin deploy update failed")
				return err
			}
			latestStatus.State = upgradev1.OriginRunningState
		}
		// once either deploy still in scaleOut
		// just wait for it until finish
		if isOriginScaleOut || isNextGenScaleOut {
			return nil
		}

		if isNextGenScaleIn {
			log.Log.Info("try next gen scale in", "origin-deploy", deployUpgradeInK8s.Spec.OriginDeployName, "origin-ns", deployUpgradeInK8s.Spec.OriginDeployNamespace,
				"next-gen-deploy", deployUpgradeInK8s.Spec.NextDeployName, "next-gen-ns", deployUpgradeInK8s.Spec.NextDeployNamespace)
			err := tryUpdateDeploy(r.Client, nextGenD, latestStatus.Config.NextGenStepReplica)
			if err != nil {
				log.Log.Error(err, "next gen deploy update failed")
				return err
			}
			latestStatus.State = upgradev1.NextGenRunningState
		}
		if isOriginScaleIn {
			log.Log.Info("try origin scale in", "origin-deploy", deployUpgradeInK8s.Spec.OriginDeployName, "origin-ns", deployUpgradeInK8s.Spec.OriginDeployNamespace,
				"next-gen-deploy", deployUpgradeInK8s.Spec.NextDeployName, "next-gen-ns", deployUpgradeInK8s.Spec.NextDeployNamespace)
			err := tryUpdateDeploy(r.Client, originD, latestStatus.Config.OriginStepReplica)
			if err != nil {
				log.Log.Error(err, "origin deploy update failed")
				return err
			}
			latestStatus.State = upgradev1.OriginRunningState
		}
		if isOriginScaleIn || isNextGenScaleIn {
			return nil
		}

	}
	return nil
}

func (r *DeploymentUpgradeReconciler) UpdateUpgradeStatus(deployUpgradeInK8s *upgradev1.DeploymentUpgrade) error {
	deployUpgradeInK8s.Status.UpdateAt = time.Now().Local().String()
	if err := r.Status().Update(context.TODO(), deployUpgradeInK8s); err != nil {
		log.Log.Error(err, "unable to update deployUpgrade status.")
		return err
	}
	return nil
}

// reset config makes StepReplica different from deploy replica
// force reset deploy replica
func (r *DeploymentUpgradeReconciler) CheckForceReset(ctx context.Context,
	deployUpgradeInK8s *upgradev1.DeploymentUpgrade,
	latestStatus *upgradev1.DeploymentUpgradeStatus) error {
	if latestStatus.Config.OriginStepReplica == latestStatus.DeployStatus.OriginReplica &&
		latestStatus.Config.NextGenStepReplica == latestStatus.DeployStatus.NextGenReplica {
		log.Log.Info("skip force reset", "origin-deploy", deployUpgradeInK8s.Spec.OriginDeployName, "origin-ns", deployUpgradeInK8s.Spec.OriginDeployNamespace,
			"next-gen-deploy", deployUpgradeInK8s.Spec.NextDeployName, "next-gen-ns", deployUpgradeInK8s.Spec.NextDeployNamespace)
		return nil
	}

	originD := &appsv1.Deployment{}
	err := r.Get(ctx, types.NamespacedName{
		Namespace: deployUpgradeInK8s.Spec.OriginDeployNamespace,
		Name:      deployUpgradeInK8s.Spec.OriginDeployName,
	}, originD)
	if err != nil {
		log.Log.Error(err, "get origin deployment failed")
		return err
	}
	nextGenD := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{
		Namespace: deployUpgradeInK8s.Spec.NextDeployNamespace,
		Name:      deployUpgradeInK8s.Spec.NextDeployName,
	}, nextGenD)
	if err != nil {
		log.Log.Error(err, "get next gray deployments failed")
		return err
	}

	log.Log.Info("try next gen reset", "origin-deploy", deployUpgradeInK8s.Spec.OriginDeployName, "origin-ns", deployUpgradeInK8s.Spec.OriginDeployNamespace,
		"next-gen-deploy", deployUpgradeInK8s.Spec.NextDeployName, "next-gen-ns", deployUpgradeInK8s.Spec.NextDeployNamespace)

	err = tryUpdateDeploy(r.Client, nextGenD, latestStatus.Config.NextGenStepReplica)
	if err != nil {
		log.Log.Error(err, "next gen deploy update failed")
		return err
	}
	latestStatus.State = upgradev1.NextGenRunningState

	log.Log.Info("try origin reset", "origin-deploy", deployUpgradeInK8s.Spec.OriginDeployName, "origin-ns", deployUpgradeInK8s.Spec.OriginDeployNamespace,
		"next-gen-deploy", deployUpgradeInK8s.Spec.NextDeployName, "next-gen-ns", deployUpgradeInK8s.Spec.NextDeployNamespace)
	err = tryUpdateDeploy(r.Client, originD, latestStatus.Config.OriginStepReplica)
	if err != nil {
		log.Log.Error(err, "origin deploy update failed")
		return err
	}
	latestStatus.State = upgradev1.OriginRunningState
	return nil
}
func (r *DeploymentUpgradeReconciler) findObjectsForDeploy(deployment client.Object) []reconcile.Request {

	attachedDeployUpgrades := &upgradev1.DeploymentUpgradeList{}
	listOps := &client.ListOptions{
		FieldSelector: fields.OneTermEqualSelector(upgradev1.OriginNameField, deployment.GetName()),
	}
	err := r.List(context.TODO(), attachedDeployUpgrades, listOps)
	if err != nil {
		log.Log.Error(err, "origin deploy lsit failed", "deploy", deployment.GetName())
		return []reconcile.Request{}
	}
	// log.Log.Info("deploy invoke", "deploy", deployment.GetName())

	others := &upgradev1.DeploymentUpgradeList{}
	listOps = &client.ListOptions{
		FieldSelector: fields.OneTermEqualSelector(upgradev1.NextGenNameField, deployment.GetName()),
	}
	err = r.List(context.TODO(), others, listOps)
	if err != nil {
		log.Log.Error(err, "nextgen deploy lsit failed", "deploy", deployment.GetName())
		return []reconcile.Request{}
	}
	attachedDeployUpgrades.Items = append(attachedDeployUpgrades.Items, others.Items...)
	if len(attachedDeployUpgrades.Items) == 0 {
		return nil
	}
	// log.Log.Info("deploy invoke", "deploy", deployment.GetName())

	requests := make([]reconcile.Request, len(attachedDeployUpgrades.Items))
	for i, item := range attachedDeployUpgrades.Items {
		requests[i] = reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      item.GetName(),
				Namespace: item.GetNamespace(),
			},
		}
	}
	return requests
}

func tryUpdateDeploy(cli client.Client, deploy *appsv1.Deployment, newreplica int) error {
	if int(*deploy.Spec.Replicas) == newreplica {
		log.Log.Info("unchange replica, skip update", "deployment", deploy.Name)
		return nil
	}
	replica := int32(newreplica)
	deploy.Spec.Replicas = &replica
	ctxWithTimeout, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := cli.Update(ctxWithTimeout, deploy); err != nil {
		log.Log.Error(err, "unexpected update failed", "deployment", deploy.Name)
		return err
	}
	return nil
}

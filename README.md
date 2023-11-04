# deployment-upgrade

This operator is specifically designed to facilitate gray upgrades in deployments. It effectively oversees the transition from one deployment to another, ensuring a smooth and stable service upgrade. It also allows for configuration of upgrade modes and forced interruptions, providing advanced control capabilities. Particularly in the context of AI model service upgrades, its performance is exceptional.

## Description

This operator effectively handles grey upgrades, smoothly transitioning an application from one version to another. The speed of the upgrade can be controlled by adjusting the upgrade steps. Each step represents a stage in the upgrade, and finally, the number of application copies is adjusted to match the configuration for a complete upgrade.

Besides the fundamental grayscale upgrade feature, we also provide three unique upgrade modes for your selection. By default, we employ the "scaleOutPriority" mode, which is most proficient in ensuring seamless grayscale upgrades. In this mode, we prioritize increasing the number of replicas, or scaling out before initiating the scale-in process, thus maintaining the overall replica count and guaranteeing service stability. The second mode, "scaleInPriority", is especially useful in circumstances like model inference or when resource conservation is paramount. This mode initiates resource recovery first, followed by a scale-out process, facilitating grayscale upgrades without the necessity for extra resources. The third mode, "scaleSimultaneously", carries out changes concurrently, irrespective of the current scale-out or scale-in phase. This mode enables the fastest grayscale upgrade and proves particularly effective during rapid rollouts.

Upgrading AI models often encounters difficulties due to hardware issues. Any operations are prohibited before reaching the intended upgrade target, often resulting in the number of replicas not reaching the expected stages, rendering operations impossible. To address this, we allow adjusting the target replica count during the grey upgrade stage. By modifying the target replica count to the current ready replica count, we can forcibly exit the uninterruptible state and allow resetting the grey upgrade configuration. Furthermore, considering that the initiation process of AI model grey upgrades may be slow, if the grey upgrade is not completed before the peak traffic arrives, we usually have to abandon the upgrade. To solve this problem, we allow adjusting the replica count of both the main and grey versions during the grey phase, achieving capacity expansion during the grey phase to cope with the surge in traffic during the grey phase.


## Getting Started
You’ll need a Kubernetes cluster to run against. You can use [KIND](https://sigs.k8s.io/kind) to get a local cluster for testing, or run against a remote cluster.
**Note:** Your controller will automatically use the current context in your kubeconfig file (i.e. whatever cluster `kubectl cluster-info` shows).

### Running on the cluster
1. Install Instances of Custom Resources:

```sh
kubectl apply -f config/samples/
```

2. Build and push your image to the location specified by `IMG`:

```sh
make docker-build docker-push IMG=<some-registry>/deployment-upgrade:tag
```

3. Deploy the controller to the cluster with the image specified by `IMG`:

```sh
make deploy IMG=<some-registry>/deployment-upgrade:tag
```

### Uninstall CRDs
To delete the CRDs from the cluster:

```sh
make uninstall
```

### Undeploy controller
UnDeploy the controller from the cluster:

```sh
make undeploy
```


### How it works
This project aims to follow the Kubernetes [Operator pattern](https://kubernetes.io/docs/concepts/extend-kubernetes/operator/).

It uses [Controllers](https://kubernetes.io/docs/concepts/architecture/controller/),
which provide a reconcile function responsible for synchronizing resources until the desired state is reached on the cluster.


## License

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


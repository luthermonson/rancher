package node

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rancher/rancher/pkg/agent/clean"
	v1 "github.com/rancher/rancher/pkg/generated/norman/batch/v1"
	v3 "github.com/rancher/rancher/pkg/generated/norman/management.cattle.io/v3"
	"github.com/rancher/rancher/pkg/kubectl"
	nodehelper "github.com/rancher/rancher/pkg/node"
	"github.com/rancher/rancher/pkg/settings"
	"github.com/rancher/rancher/pkg/types/config"
	rketypes "github.com/rancher/rke/types"
	"github.com/sirupsen/logrus"
	batchV1 "k8s.io/api/batch/v1"
	coreV1 "k8s.io/api/core/v1"
	kerror "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
)

func (m *Lifecycle) deleteV1Node(node *v3.Node) (runtime.Object, error) {
	logrus.Debugf("Deleting v1.node for [%v] node", node.Status.NodeName)
	if nodehelper.IgnoreNode(node.Status.NodeName, node.Status.NodeLabels) {
		logrus.Debugf("Skipping v1.node removal for [%v] node", node.Status.NodeName)
		return node, nil
	}

	if node.Status.NodeName == "" {
		return node, nil
	}

	userClient, err := m.clusterManager.UserContext(node.Namespace)
	if err != nil {
		if kerror.IsNotFound(err) {
			return node, nil
		}
		return node, err
	}

	ctx, cancel := context.WithTimeout(context.TODO(), 45*time.Second)
	defer cancel()
	err = userClient.K8sClient.CoreV1().Nodes().Delete(
		ctx, node.Status.NodeName, metav1.DeleteOptions{})
	if !kerror.IsNotFound(err) && ctx.Err() != context.DeadlineExceeded {
		return node, err
	}

	return node, nil
}

func (m *Lifecycle) drainNode(node *v3.Node) error {
	nodeCopy := node.DeepCopy() // copy for cache protection as we do no updating but need things set for the drain
	cluster, err := m.clusterLister.Get("", nodeCopy.Namespace)
	if err != nil {
		if kerror.IsNotFound(err) {
			return nil
		}
		return err
	}

	nodePool, err := m.getNodePool(node.Spec.NodePoolName)
	if err != nil && !kerror.IsNotFound(err) {
		return err
	}

	if !nodehelper.DrainBeforeDelete(nodeCopy, cluster, nodePool) {
		return nil
	}

	logrus.Infof("node [%s] requires draining before delete", nodeCopy.Spec.RequestedHostname)
	kubeConfig, err := m.getKubeConfig(cluster)
	if err != nil {
		return fmt.Errorf("node [%s] error getting kubeConfig", nodeCopy.Spec.RequestedHostname)
	}

	if nodeCopy.Spec.NodeDrainInput == nil {
		logrus.Debugf("node [%s] has no NodeDrainInput, creating one with 60s timeout",
			nodeCopy.Spec.RequestedHostname)
		nodeCopy.Spec.NodeDrainInput = &rketypes.NodeDrainInput{
			Force:           true,
			DeleteLocalData: true,
			GracePeriod:     60,
			Timeout:         60,
		}
	}

	backoff := wait.Backoff{
		Duration: 2 * time.Second,
		Factor:   1,
		Jitter:   0,
		Steps:    3,
	}

	logrus.Infof("node [%s] attempting to drain, retrying up to 3 times", nodeCopy.Spec.RequestedHostname)
	// purposefully ignoring error, if the drain fails this falls back to deleting the node as usual
	return wait.ExponentialBackoff(backoff, func() (bool, error) {
		ctx, cancel := context.WithTimeout(m.ctx, time.Duration(nodeCopy.Spec.NodeDrainInput.Timeout)*time.Second)
		defer cancel()

		_, msg, err := kubectl.Drain(ctx, kubeConfig, nodeCopy.Status.NodeName, nodehelper.GetDrainFlags(nodeCopy))
		if ctx.Err() != nil {
			logrus.Errorf("node [%s] kubectl drain failed, retrying: %s", nodeCopy.Spec.RequestedHostname, ctx.Err())
			return false, nil
		}
		if err != nil {
			// kubectl failed continue on with delete any way
			logrus.Errorf("node [%s] kubectl drain error, retrying: %s", nodeCopy.Spec.RequestedHostname, err)
			return false, nil
		}

		logrus.Infof("node [%s] kubectl drain response: %s", nodeCopy.Spec.RequestedHostname, msg)
		return true, nil
	})
}

func (m *Lifecycle) cleanNode(node *v3.Node) error {
	userContext, err := m.clusterManager.UserContext(node.Namespace)
	if err != nil {
		if kerror.IsNotFound(err) {
			return nil
		}
		return err
	}

	job, err := m.createCleanUpJob(userContext, node)
	if err != nil {
		return err
	}

	return m.waitUntilJobCompletes(userContext, job)
}

func (m *Lifecycle) waitUntilJobCompletes(userContext *config.UserContext, job *v1.Job) error {
	backoff := wait.Backoff{
		Duration: 2 * time.Second,
		Factor:   1,
		Jitter:   0,
		Steps:    10,
	}

	logrus.Infof("[node-cleanup] validating cleanup job %s finished, retrying up to 10 times", job.Name)
	// purposefully ignoring error, if the drain fails this falls back to deleting the node as usual
	return wait.ExponentialBackoff(backoff, func() (bool, error) {
		ctx, cancel := context.WithTimeout(m.ctx, 15*time.Second)
		defer cancel()

		j, err := userContext.K8sClient.BatchV1().Jobs(job.Namespace).Get(ctx, job.Name, metav1.GetOptions{})
		if ctx.Err() != nil {
			logrus.Errorf("[node-cleanup] context failed while retrieving job %s, retrying: %s", job.Name, ctx.Err())
			return false, nil
		}
		if err != nil {
			// kubectl failed continue on with delete any way
			logrus.Errorf("[node-cleanup] failed to get job %s, retrying: %s", job.Name, err)
			return false, nil
		}

		if j.Status.Succeeded == 0 {
			logrus.Infof("[node-cleanup] job %s still hasn't finished, backing off and retrying", job.Name)
			return false, nil
		}

		logrus.Infof("[node-cleanup] job %s finished, continuing to delete v3 node", job.Name)
		return true, nil
	})
}

func (m *Lifecycle) createCleanUpJob(userContext *config.UserContext, node *v3.Node) (*batchV1.Job, error) {
	meta := metav1.ObjectMeta{
		GenerateName: "cattle-node-cleanup-",
		Namespace:    "default",
		Labels:       map[string]string{"cattle.io/creator": "norman"},
	}

	cluster, err := m.clusterLister.Get("", node.Namespace)
	if err != nil {
		if !kerror.IsNotFound(err) {
			return nil, err
		}
	}

	var tolerations []coreV1.Toleration

	for _, taint := range node.Spec.InternalNodeSpec.Taints {
		tolerations = append(tolerations, coreV1.Toleration{
			Effect:   taint.Effect,
			Key:      taint.Key,
			Operator: "Exists",
		})
	}

	socket := coreV1.HostPathType("Socket")
	fiveMin := int32(5 * 60)
	job := batchV1.Job{
		ObjectMeta: meta,
		Spec: batchV1.JobSpec{
			TTLSecondsAfterFinished: &fiveMin,
			Template: coreV1.PodTemplateSpec{
				Spec: coreV1.PodSpec{
					NodeSelector: map[string]string{
						"kubernetes.io/hostname": node.Status.NodeName,
					},
					Tolerations: tolerations,
					Volumes: []coreV1.Volume{
						{
							Name: "docker",
							VolumeSource: coreV1.VolumeSource{
								HostPath: &coreV1.HostPathVolumeSource{
									Path: "/var/run/docker.sock",
									Type: &socket,
								},
							},
						},
					},
					Containers: []coreV1.Container{
						{
							Name:  clean.ContainerName,
							Image: settings.AgentImage.Get(),
							Args:  []string{"--", "agent", "clean", "job"},
							Env: []coreV1.EnvVar{
								{
									Name:  "AGENT_IMAGE",
									Value: settings.AgentImage.Get(),
								},
								{
									Name:  "PREFIX_PATH",
									Value: cluster.Spec.RancherKubernetesEngineConfig.PrefixPath,
								},
								{
									Name:  "WINDOWS_PREFIX_PATH",
									Value: cluster.Spec.RancherKubernetesEngineConfig.WindowsPrefixPath,
								},
							},
							VolumeMounts: []coreV1.VolumeMount{
								{
									MountPath: "/var/run/docker.sock",
									Name:      "docker",
								},
							},
							ImagePullPolicy: coreV1.PullAlways,
						},
					},
					RestartPolicy: "OnFailure",
				},
			},
		},
	}

	return userContext.K8sClient.BatchV1().Jobs("default").Create(context.TODO(), &job, metav1.CreateOptions{})
}

func createCleanupServiceAccount(userContext *config.UserContext) (*coreV1.ServiceAccount, error) {
	meta := metav1.ObjectMeta{
		GenerateName: "cattle-cleanup-node-",
		Namespace:    "default",
	}
	serviceAccount := coreV1.ServiceAccount{
		ObjectMeta: meta,
	}
	return userContext.K8sClient.CoreV1().ServiceAccounts("default").Create(context.TODO(), &serviceAccount, metav1.CreateOptions{})
}

func (m *Lifecycle) userNodeRemoveCleanup(obj *v3.Node) (runtime.Object, error) {
	newObj := obj.DeepCopy()
	newObj.SetFinalizers(removeFinalizerWithPrefix(newObj.GetFinalizers(), userNodeRemoveFinalizerPrefix))

	annos := newObj.GetAnnotations()
	if annos == nil {
		annos = make(map[string]string)
	} else {
		annos = removeAnnotationWithPrefix(annos, userNodeRemoveAnnotationPrefix)
		delete(annos, userNodeRemoveCleanupAnnotationOld)
	}

	annos[userNodeRemoveCleanupAnnotation] = "true"
	newObj.SetAnnotations(annos)
	return m.nodeClient.Update(newObj)
}

func removeFinalizerWithPrefix(finalizers []string, prefix string) []string {
	var nf []string
	for _, finalizer := range finalizers {
		if strings.HasPrefix(finalizer, prefix) {
			logrus.Debugf("a finalizer with prefix %s will be removed", prefix)
			continue
		}
		nf = append(nf, finalizer)
	}
	return nf
}

func removeAnnotationWithPrefix(annotations map[string]string, prefix string) map[string]string {
	for k := range annotations {
		if strings.HasPrefix(k, prefix) {
			logrus.Debugf("annotation with prefix %s will be removed", prefix)
			delete(annotations, k)
		}
	}
	return annotations
}

/*
Copyright 2025 The Kubernetes Authors.

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

package e2eupgraderollback

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/onsi/gomega"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	e2epod "k8s.io/kubernetes/test/e2e/framework/pod"
	"k8s.io/kubernetes/test/utils/client-go/ktesting"
)

// RepoRootDefault figures out whether an E2E suite is invoked in its directory (as in `go test ./test/e2e_upgrade_rollback`),
// directly in the root (as in `make test`), or somewhere deep inside
// the _output directory.
func RepoRootDefault() string {
	for i := range 10 {
		path := "." + strings.Repeat("/..", i)
		if _, err := os.Stat(path + "/test/e2e/framework"); err == nil {
			return path
		}
	}
	// Traditional default.
	return "../../"
}

// CurrentBinDir returns the environment variable name and the directory
// where the Kubernetes server binaries are located.
func CurrentBinDir() (envName, content string) {
	envName = "KUBERNETES_SERVER_BIN_DIR"
	content, _ = os.LookupEnv(envName)
	return
}

// MakeGUPodWithDownwardAPI creates a Guaranteed pod spec with CPU (request == limit (>= 1 full CPU))
// and a DownwardAPI volume that exposes the assigned.cpuset resource field.
// The container name is configurable so multiple pods can have distinct container names.
func MakeGUPodWithDownwardAPI(podName, containerName string, cpuRequest string) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: podName,
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:    containerName,
					Image:   e2epod.GetDefaultTestImage(),
					Command: e2epod.GenerateScriptCmd(e2epod.InfiniteSleepCommand),
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceCPU:    resource.MustParse(cpuRequest),
							v1.ResourceMemory: resource.MustParse("100Mi"),
						},
						Limits: v1.ResourceList{
							v1.ResourceCPU:    resource.MustParse(cpuRequest),
							v1.ResourceMemory: resource.MustParse("100Mi"),
						},
					},
					VolumeMounts: []v1.VolumeMount{
						{
							Name:      "podinfo",
							MountPath: "/podinfo",
						},
					},
				},
			},
			Volumes: []v1.Volume{
				{
					Name: "podinfo",
					VolumeSource: v1.VolumeSource{
						DownwardAPI: &v1.DownwardAPIVolumeSource{
							Items: []v1.DownwardAPIVolumeFile{
								{
									Path: fmt.Sprintf("assigned_cpuset_%s", containerName),
									ResourceFieldRef: &v1.ResourceFieldSelector{
										ContainerName: containerName,
										Resource:      "assigned.cpuset",
									},
								},
								// To bypass a potential bug, The below resource will be removed in the future
								// requests.cpu is added as a second item to ensure the DownwardAPI volume
								// is not empty when assigned.cpuset is stripped by dropDisabledAssignedCpuset
								// (when the DownwardAPIAssignedResources feature gate is OFF). Without this,
								// the volume would be removed entirely, leaving the volumeMount orphaned.
								{
									Path: fmt.Sprintf("requests_cpu_%s", containerName),
									ResourceFieldRef: &v1.ResourceFieldSelector{
										ContainerName: containerName,
										Resource:      "requests.cpu",
										Divisor:       resource.MustParse("1"),
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

// CreatePodAndWaitForRunning creates a pod and waits for it to reach Running state.
// Returns the created pod object.
func CreatePodAndWaitForRunning(tCtx ktesting.TContext, pod *v1.Pod) *v1.Pod {
	tCtx.Helper()
	namespace := tCtx.Namespace()

	createdPod, err := tCtx.Client().CoreV1().Pods(namespace).Create(tCtx, pod, metav1.CreateOptions{})
	tCtx.ExpectNoError(err, "create pod %s", pod.Name)
	tCtx.Logf("Created pod %q in namespace %q", createdPod.Name, namespace)

	tCtx.ExpectNoError(e2epod.WaitForPodRunningInNamespace(tCtx, tCtx.Client(), createdPod),
		"wait for pod %q to be Running", createdPod.Name)
	tCtx.Logf("Pod %q is Running", createdPod.Name)

	return createdPod
}

// HasAssignedCpusetDownwardAPIItem checks if the pod spec contains the assigned.cpuset
// DownwardAPI volume file item.
func HasAssignedCpusetDownwardAPIItem(pod *v1.Pod) bool {
	for _, vol := range pod.Spec.Volumes {
		if vol.DownwardAPI != nil {
			for _, item := range vol.DownwardAPI.Items {
				if item.ResourceFieldRef != nil && item.ResourceFieldRef.Resource == "assigned.cpuset" {
					return true
				}
			}
		}
	}
	return false
}

// ExecInContainer executes a command inside a container and returns the stdout output.
func ExecInContainer(tCtx ktesting.TContext, config *rest.Config, pod *v1.Pod, containerName string, command ...string) (string, error) {
	tCtx.Helper()

	client := tCtx.Client()
	req := client.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(pod.Namespace).
		Name(pod.Name).
		SubResource("exec").
		Param("container", containerName).
		Param("stdout", "true").
		Param("stderr", "true")

	for _, cmd := range command {
		req.Param("command", cmd)
	}

	executor, err := remotecommand.NewSPDYExecutor(config, "POST", req.URL())
	if err != nil {
		return "", fmt.Errorf("create SPDY executor: %w", err)
	}

	var stdout, stderr bytes.Buffer
	streamOpts := remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	}

	if err := executor.StreamWithContext(tCtx, streamOpts); err != nil {
		return "", fmt.Errorf("exec in container %s of pod %s: %w (stderr: %s)", containerName, pod.Name, err, stderr.String())
	}

	return stdout.String(), nil
}

// Cpuset verification helpers

// GetAssignedCpusetFromContainer reads the assigned cpuset from the DownwardAPI volume file
// inside the container. It execs into the container and reads the file at /podinfo/assigned_cpuset_<containerName>.
// Returns the cpuset value as a string (e.g. "1-2") or an error if the file is not readable.
func GetAssignedCpusetFromContainer(tCtx ktesting.TContext, config *rest.Config, pod *v1.Pod, containerName string) (string, error) {
	tCtx.Helper()
	filePath := fmt.Sprintf("/podinfo/assigned_cpuset_%s", containerName)
	output, err := ExecInContainer(tCtx, config, pod, containerName, "/bin/sh", "-c", fmt.Sprintf("cat %s", filePath))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

// VerifyCpusetVisible verifies that the assigned.cpuset value is visible (non-empty) in the
// DownwardAPI volume file inside the container. It retries with Eventually because the kubelet
// may need time to update the DownwardAPI volume after the pod starts.
// TODO: Reverify the Timeout value, and Eventually Period
func VerifyCpusetVisible(tCtx ktesting.TContext, config *rest.Config, pod *v1.Pod, containerName string) {
	tCtx.Helper()
	tCtx.Eventually(func(tCtx ktesting.TContext) string {
		cpuset, err := GetAssignedCpusetFromContainer(tCtx, config, pod, containerName)
		if err != nil {
			tCtx.Logf("Error reading cpuset from container %s (will retry): %v", containerName, err)
			return ""
		}
		return cpuset
	}).WithTimeout(2*time.Minute).ShouldNot(gomega.BeEmpty(),
		"assigned.cpuset should be visible in DownwardAPI volume when feature gate is ON")
}

// VerifyCpusetNotVisible verifies that the assigned.cpuset value is NOT visible in the
// DownwardAPI volume file inside the container (file should not exist or be empty).
// It retries with Eventually because the kubelet may need time to reconcile the pod
// after a restart (e.g., after a feature gate toggle).
func VerifyCpusetNotVisible(tCtx ktesting.TContext, config *rest.Config, pod *v1.Pod, containerName string) {
	tCtx.Helper()
	filePath := fmt.Sprintf("/podinfo/assigned_cpuset_%s", containerName)
	tCtx.Eventually(func(tCtx ktesting.TContext) string {
		output, err := ExecInContainer(tCtx, config, pod, containerName, "/bin/sh", "-c",
			fmt.Sprintf("cat %s 2>&1 || true", filePath))
		if err != nil {
			// Kubelet may not have reconciled the pod yet after restart.
			// Return a sentinel value that won't match the success condition,
			// causing Eventually to retry.
			tCtx.Logf("Error execing in container %s (will retry): %v", containerName, err)
			return "__retry__"
		}
		return output
	}).WithTimeout(2*time.Minute).Should(gomega.SatisfyAny(
		gomega.ContainSubstring("No such file"),
		gomega.And(
			gomega.Not(gomega.ContainSubstring("__retry__")),
			gomega.BeEmpty(),
		),
	), "assigned_cpuset file should NOT exist or be empty when feature gate is OFF")
}

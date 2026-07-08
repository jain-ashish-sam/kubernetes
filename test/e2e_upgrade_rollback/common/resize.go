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

package common

import (
	"fmt"
	"strings"
	"time"

	"github.com/onsi/gomega"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	helpers "k8s.io/component-helpers/resource"
	"k8s.io/client-go/rest"
	podutils "k8s.io/kubectl/pkg/util/podutils"
	"k8s.io/utils/cpuset"
	e2epod "k8s.io/kubernetes/test/e2e/framework/pod"
	"k8s.io/kubernetes/test/utils/client-go/ktesting"
)

// HaveContainerCPUsCount verifies that the container has the expected number of CPUs
// in its cgroup cpuset. It execs into the container and reads the cpuset from the
// cgroup (cpuset.cpus.effective for v2, cpuset.cpus for v1), then checks if the CPU
// count matches the expected value.
func HaveContainerCPUsCount(tCtx ktesting.TContext, config *rest.Config, pod *v1.Pod, containerName string, expectedCount int) bool {
	tCtx.Helper()

	// Shell command that tries cgroup v2 first, then falls back to cgroup v1.
	// v2: /sys/fs/cgroup/cpuset.cpus.effective
	// v1: parse /proc/self/cgroup for the cpuset line, extract the path, read cpuset.cpus
	cmd := `cat /sys/fs/cgroup/cpuset.cpus.effective 2>/dev/null || ` +
		`cat /sys/fs/cgroup/cpuset$(grep '^cpuset:' /proc/self/cgroup | cut -d: -f3)/cpuset.cpus 2>/dev/null`

	stdout, stderr, err := e2epod.Exec(tCtx, e2epod.ExecOptions{
		Command:       []string{"/bin/sh", "-c", cmd},
		Namespace:     pod.Namespace,
		PodName:       pod.Name,
		ContainerName: containerName,
		CaptureStdout: true,
		CaptureStderr: true,
	})
	if err != nil {
		tCtx.Logf("Error reading cgroup cpuset from container %q in pod %q: %v (stderr: %s)",
			containerName, pod.Name, err, stderr)
		return false
	}

	cpus, err := cpuset.Parse(strings.TrimSpace(stdout))
	if err != nil {
		tCtx.Logf("Error parsing cpuset %q from container %q: %v", stdout, containerName, err)
		return false
	}

	tCtx.Logf("Container %q in pod %q has cgroup cpuset %q (%d CPUs, expected %d)",
		containerName, pod.Name, cpus.String(), cpus.Size(), expectedCount)
	return cpus.Size() == expectedCount
}

// ReadCgroupCpuset execs into the container and reads the cgroup cpuset, returning the parsed cpuset.
// It tries cgroup v2 (cpuset.cpus.effective) first, then falls back to cgroup v1 (cpuset.cpus).
func ReadCgroupCpuset(tCtx ktesting.TContext, config *rest.Config, pod *v1.Pod, containerName string) cpuset.CPUSet {
	tCtx.Helper()

	cmd := `cat /sys/fs/cgroup/cpuset.cpus.effective 2>/dev/null || ` +
		`cat /sys/fs/cgroup/cpuset$(grep '^cpuset:' /proc/self/cgroup | cut -d: -f3)/cpuset.cpus 2>/dev/null`

	stdout, stderr, err := e2epod.Exec(tCtx, e2epod.ExecOptions{
		Command:       []string{"/bin/sh", "-c", cmd},
		Namespace:     pod.Namespace,
		PodName:       pod.Name,
		ContainerName: containerName,
		CaptureStdout: true,
		CaptureStderr: true,
	})
	if err != nil {
		tCtx.Logf("Error reading cgroup cpuset from container %q in pod %q: %v (stderr: %s)",
			containerName, pod.Name, err, stderr)
		return cpuset.CPUSet{}
	}

	cpus, err := cpuset.Parse(strings.TrimSpace(stdout))
	if err != nil {
		tCtx.Logf("Error parsing cpuset %q from container %q: %v", stdout, containerName, err)
		return cpuset.CPUSet{}
	}
	return cpus
}

// HaveDownwardAPICPUsCount verifies that the DownwardAPI volume inside the container
// reflects the expected number of CPUs. It execs into the container, reads the
// assigned.cpuset value from the DownwardAPI volume file at
// /podinfo/assigned_cpuset_<containerName>, parses it, and checks if the CPU count
// matches the expected value.
//
// This is equivalent to the HaveDownwardAPICPUsCount matcher in test/e2e_node/cpu_manager_test.go,
// but uses e2epod.Exec (which accepts ktesting.TContext) instead of framework.Framework.
func HaveDownwardAPICPUsCount(tCtx ktesting.TContext, config *rest.Config, pod *v1.Pod, containerName string, expectedCount int) bool {
	tCtx.Helper()

	// Exec into the container and read the DownwardAPI volume file.
	filePath := fmt.Sprintf("/podinfo/assigned_cpuset_%s", containerName)
	stdout, stderr, err := e2epod.Exec(tCtx, e2epod.ExecOptions{
		Command:       []string{"/bin/sh", "-c", fmt.Sprintf("cat %s", filePath)},
		Namespace:     pod.Namespace,
		PodName:       pod.Name,
		ContainerName: containerName,
		CaptureStdout: true,
		CaptureStderr: true,
	})
	if err != nil {
		tCtx.Logf("Error reading cpuset from container %q in pod %q: %v (stderr: %s)",
			containerName, pod.Name, err, stderr)
		return false
	}

	// Parse the cpuset string and count the CPUs.
	cpus, err := cpuset.Parse(strings.TrimSpace(stdout))
	if err != nil {
		tCtx.Logf("Error parsing cpuset %q from container %q: %v", stdout, containerName, err)
		return false
	}

	tCtx.Logf("Container %q in pod %q has cpuset %q (%d CPUs, expected %d)",
		containerName, pod.Name, cpus.String(), cpus.Size(), expectedCount)
	return cpus.Size() == expectedCount
}

// HaveDownwardAPICpusetNotVisible verifies that the assigned.cpuset value is NOT visible
// in the DownwardAPI volume file inside the container (file should not exist or be empty).
// This is used when the DownwardAPIAssignedResources feature gate is OFF.
//
// It execs into the container and attempts to read /podinfo/assigned_cpuset_<containerName>.
// Returns true if the file does not exist or is empty.
func HaveDownwardAPICpusetNotVisible(tCtx ktesting.TContext, config *rest.Config, pod *v1.Pod, containerName string) bool {
	tCtx.Helper()

	filePath := fmt.Sprintf("/podinfo/assigned_cpuset_%s", containerName)
	stdout, _, err := e2epod.Exec(tCtx, e2epod.ExecOptions{
		Command:       []string{"/bin/sh", "-c", fmt.Sprintf("cat %s 2>&1 || true", filePath)},
		Namespace:     pod.Namespace,
		PodName:       pod.Name,
		ContainerName: containerName,
		CaptureStdout: true,
		CaptureStderr: true,
	})
	if err != nil {
		tCtx.Logf("Error checking cpuset visibility in container %q of pod %q: %v",
			containerName, pod.Name, err)
		return false
	}

	output := strings.TrimSpace(stdout)
	return output == "" || strings.Contains(output, "No such file")
}

// WaitForPodResizeActuation polls until the pod's resize has been actuated by the kubelet.
// It checks four conditions (same as podresize.WaitForPodResizeActuation):
//  1. Resize is not infeasible (terminal state — fails immediately)
//  2. ObservedGeneration has caught up to Generation
//  3. PodResizePending and PodResizeInProgress conditions are cleared
//  4. Pod is ready
//
// This is equivalent to podresize.WaitForPodResizeActuation in the podresize package,
// but uses ktesting.TContext instead of framework.Framework.
func WaitForPodResizeActuation(tCtx ktesting.TContext, config *rest.Config, pod *v1.Pod, containerName string, expectedCPUCount int) *v1.Pod {
	tCtx.Helper()

	var resizedPod *v1.Pod

	tCtx.Eventually(func(tCtx ktesting.TContext) error {
		// Fetch the latest pod state.
		latestPod, err := tCtx.Client().CoreV1().Pods(pod.Namespace).Get(tCtx, pod.Name, metav1.GetOptions{})
		if err != nil {
			return err // will retry
		}
		resizedPod = latestPod

		// 1. Check if resize is infeasible (terminal state — stop trying).
		if helpers.IsPodResizeInfeasible(latestPod) {
			return gomega.StopTrying("resize is infeasible")
		}

		// 2. Wait for observedGeneration to catch up to generation.
		if latestPod.Status.ObservedGeneration < latestPod.Generation {
			return fmt.Errorf("waiting for observedGeneration (%d) to catch up to generation (%d)",
				latestPod.Status.ObservedGeneration, latestPod.Generation)
		}

		// 3. Wait for kubelet to clear the resize status conditions.
		for _, c := range latestPod.Status.Conditions {
			if c.Type == v1.PodResizePending || c.Type == v1.PodResizeInProgress {
				return fmt.Errorf("resize status %v is still present in the pod status", c)
			}
		}

		// 4. Wait for the pod to be ready.
		if !podutils.IsPodReady(latestPod) {
			return fmt.Errorf("pod is not ready")
		}

		return nil
	}).WithTimeout(2*time.Minute).WithPolling(1*time.Second).Should(gomega.Succeed())

	return resizedPod
}


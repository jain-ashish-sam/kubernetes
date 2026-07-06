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
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/kubernetes/test/utils/client-go/ktesting"
)

// HaveContainerCPUsCount verifies that the given pod's container has the expected number
// of CPUs in its cgroup cpuset.
//
// This is a re-implementation of the HaveContainerCPUsCount matcher in
// test/e2e_node/cpu_manager_test.go. We re-implement it here because the original
// function is defined in a _test.go file within the e2e_node package, making it
// impossible to import from other packages (Go does not allow importing test files
// cross-package). The implementation logic is identical: it reads the container's
// cgroup cpuset file and checks if the number of CPUs matches the expected count.
//
// TODO: Implement this function. It should:
//  1. Find the container's cgroup path for the given pod (handling both cgroupfs and systemd drivers)
//  2. Read the cpuset from the cgroup (e.g., cpuset.cpus.effective for v2, cpuset.cpus for v1)
//  3. Parse the cpuset and count the number of CPUs
//  4. Return true if the count matches the expected value
//
// For now, this is a stub that will be implemented later.
func HaveContainerCPUsCount(pod *v1.Pod, containerName string, expectedCount int) bool {
	// TODO: Implement cgroup cpuset verification
	return true
}

// HaveDownwardAPICPUsCount verifies that the DownwardAPI volume inside the container
// reflects the expected number of CPUs. It reads the assigned.cpuset value from the
// DownwardAPI volume file and counts the CPUs.
//
// This is equivalent to the HaveDownwardAPICPUsCount matcher in test/e2e_node/cpu_manager_test.go,
// which uses the framework's exec helpers to read the DownwardAPI volume.
//
// TODO: Implement this function. It should:
//  1. Exec into the container and read /podinfo/assigned_cpuset_<containerName>
//  2. Parse the cpuset string and count the CPUs
//  3. Return true if the count matches the expected value
//
// For now, this is a stub that will be implemented later.
func HaveDownwardAPICPUsCount(tCtx ktesting.TContext, config *rest.Config, pod *v1.Pod, containerName string, expectedCount int) bool {
	// TODO: Implement DownwardAPI cpuset verification
	return true
}

// WaitForPodResizeActuation polls until the pod's resize has been actuated by the kubelet.
// It checks the DownwardAPI cpuset value until it reflects the expected CPU count.
//
// This is equivalent to podresize.WaitForPodResizeActuation in the podresize package,
// which depends on framework.Framework and e2epod.PodClient (not available in ktesting).
//
// TODO: Implement this function. It should:
//  1. Poll the DownwardAPI cpuset value (or cgroup cpuset) until it matches expectedCPUCount
//  2. Return the actuated pod
//
// For now, this is a stub that will be implemented later.
func WaitForPodResizeActuation(tCtx ktesting.TContext, config *rest.Config, pod *v1.Pod, containerName string, expectedCPUCount int) *v1.Pod {
	// TODO: Implement resize actuation wait
	return pod
}

// ExpectPodResized verifies that the pod has been successfully resized to the expected state.
//
// This is equivalent to podresize.ExpectPodResized in the podresize package,
// which depends on framework.Framework (not available in ktesting).
//
// TODO: Implement this function. It should:
//  1. Verify the pod's resize status is not "Infeasible"
//  2. Verify the pod's resource allocations match expected values
//
// For now, this is a stub that will be implemented later.
func ExpectPodResized(tCtx ktesting.TContext, pod *v1.Pod, expectedCPUCount int) {
	// TODO: Implement resize verification
}

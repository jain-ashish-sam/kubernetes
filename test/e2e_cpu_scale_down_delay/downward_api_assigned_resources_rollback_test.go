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

package e2ecpuscaledowndelay

import (
	"testing"
	"time"

	"github.com/onsi/gomega"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	e2enode "k8s.io/kubernetes/test/e2e/framework/node"
	e2epod "k8s.io/kubernetes/test/e2e/framework/pod"
	e2etestfiles "k8s.io/kubernetes/test/e2e/framework/testfiles"
	"k8s.io/kubernetes/test/utils/client-go/ktesting"
	"k8s.io/kubernetes/test/utils/localupcluster"
)

func init() {
	ktesting.SetDefaultVerbosity(2)
}

var repoRoot = repoRootDefault()

func TestDownwardAPIAssignedResourcesRollback(t *testing.T) {
	// Test-entry-point
	testDownwardAPIAssignedResourcesRollback(ktesting.Init(t))
}

func testDownwardAPIAssignedResourcesRollback(tCtx ktesting.TContext) {
	// Some other things normally done by test/e2e.
	e2etestfiles.AddFileSource(e2etestfiles.RootFileSource{Root: repoRoot})

	gomega.RegisterFailHandler(func(message string, callerSkip ...int) {
		tCtx.Helper()
		tCtx.Fatal(message)
	})

	// Get binary directory from environment
	envName, dir := currentBinDir()
	if dir == "" {
		tCtx.Fatalf("%s must be set", envName)
	}

	// ---- Stage 1: Create cluster with DownwardAPIAssignedResources=true ----
	// and CPU manager static policy (required for GU pods to get exclusive cpusets)
	cluster := localupcluster.New()
	// The cleanup fxn will be automatticaly called after the test exits
	tCtx.CleanupCtx(func(tCtx ktesting.TContext) {
		tCtx.Step("cleanup", cluster.Stop)
	})

	localUpClusterEnv := map[string]string{
		"KUBELET_FLAGS": "--cpu-manager-policy=static --kube-reserved=cpu=1,memory=1Gi --system-reserved=cpu=1,memory=1Gi",
	}

	phase := "phase-0-gate-on"
	cluster.Start(tCtx, phase, dir, localUpClusterEnv, "DownwardAPIAssignedResources=true")

	// Wait for node to be ready
	restConfig := cluster.LoadConfig(tCtx)
	tCtx = tCtx.WithRESTConfig(restConfig).WithNamespace("default")
	// Save restConfig for exec operations (tCtx.WithRESTConfig creates a new context,
	// but we need the raw *rest.Config for remotecommand)
	savedRestConfig := restConfig

	tCtx.Step("wait for node", func(tCtx ktesting.TContext) {
		tCtx.ExpectNoError(e2enode.WaitForAllNodesSchedulable(tCtx, tCtx.Client(), 5*time.Minute))
	})

	tCtx.Log("Stage 1 PASS: Cluster created successfully with DownwardAPIAssignedResources=true and CPU manager static policy")

	// ---- Stage 2: Create a GU pod with assigned.cpuset DownwardAPI ----
	var pod1 *v1.Pod
	tCtx.Step("create-gu-pod-with-downward-api", func(tCtx ktesting.TContext) {
		podSpec := makeGUPodWithDownwardAPI("gu-pod-gate-on", "gu-container", "2")
		pod1 = createPodAndWaitForRunning(tCtx, podSpec)
	})

	// Verify the assigned.cpuset DownwardAPI item is there in the pod spec
	tCtx.Step("verify-downward-api-item-preserved", func(tCtx ktesting.TContext) {
		fetchedPod, err := tCtx.Client().CoreV1().Pods(tCtx.Namespace()).Get(tCtx, pod1.Name, metav1.GetOptions{})
		tCtx.ExpectNoError(err, "get pod %s", pod1.Name)
		tCtx.Expect(hasAssignedCpusetDownwardAPIItem(fetchedPod)).To(gomega.BeTrue(),
			"assigned.cpuset DownwardAPI item should be preserved when feature gate is ON")
		tCtx.Logf("Pod %q has assigned.cpuset DownwardAPI item preserved in spec", pod1.Name)
	})

	tCtx.Log("Stage 2 PASS: GU pod created with assigned.cpuset DownwardAPI, pod is Running")

	// ---- Stage 3: Verify cpuset is visible via exec ----
	tCtx.Step("verify-cpuset-visible-in-downward-api", func(tCtx ktesting.TContext) {
		verifyCpusetVisible(tCtx, savedRestConfig, pod1, "gu-container")
		// Also log the actual cpuset value for debugging
		cpusetValue, err := getAssignedCpusetFromContainer(tCtx, savedRestConfig, pod1, "gu-container")
		tCtx.ExpectNoError(err, "read cpuset value from container")
		tCtx.Logf("Pod %q container %q - assigned.cpuset value: %q", pod1.Name, "gu-container", cpusetValue)
	})

	tCtx.Log("Stage 3 PASS: assigned.cpuset is visible in DownwardAPI volume file")

	// ---- Stage 4: Toggle gate OFF, verify existing pod is tsill running ----
	phase = "phase-1-gate-off"
	tCtx.Step("toggle-feature-gate-off", func(tCtx ktesting.TContext) {
		cluster.ToggleFeatureGates(tCtx, phase, "DownwardAPIAssignedResources=false")
	})

	// Wait for node to be ready again after toggle
	tCtx.Step("wait-for-node-after-toggle", func(tCtx ktesting.TContext) {
		tCtx.ExpectNoError(e2enode.WaitForAllNodesSchedulable(tCtx, tCtx.Client(), 5*time.Minute))
	})

	// Verify existing pod is still Running
	tCtx.Step("verify-existing-pod-still-running", func(tCtx ktesting.TContext) {
		tCtx.ExpectNoError(e2epod.WaitForPodRunningInNamespace(tCtx, tCtx.Client(), pod1),
			"existing pod %q should still be Running after gate toggle", pod1.Name)
		tCtx.Logf("Pod %q is still Running after gate toggle to OFF", pod1.Name)
	})

	// Verify cpusets are still visible in the existing pod (data already on disk)
	tCtx.Step("verify-cpuset-still-visible-after-toggle", func(tCtx ktesting.TContext) {
		verifyCpusetVisible(tCtx, savedRestConfig, pod1, "gu-container")
		cpusetValue, err := getAssignedCpusetFromContainer(tCtx, savedRestConfig, pod1, "gu-container")
		tCtx.ExpectNoError(err, "read cpuset value from container after toggle")
		tCtx.Logf("Pod %q container %q - assigned.cpuset value after toggle: %q", pod1.Name, "gu-container", cpusetValue)
	})

	tCtx.Log("Stage 4 PASS: Existing pod survived gate toggle, cpusets still visible")

	// ---- Stage 5: Deploy new pod with gate OFF ----
	var pod2 *v1.Pod
	tCtx.Step("create-new-pod-with-gate-off", func(tCtx ktesting.TContext) {
		podSpec := makeGUPodWithDownwardAPI("gu-pod-gate-off", "gu-container-2", "2")
		pod2 = createPodAndWaitForRunning(tCtx, podSpec)
	})

	tCtx.Log("Stage 5 PASS: New pod created with gate OFF, pod is Running")

	// ---- Stage 6: Verify assigned.cpuset NOT visible in new pod ----
	// The API server should have dropped the assigned.cpuset DownwardAPI item from the pod spec
	tCtx.Step("verify-cpuset-dropped-from-spec", func(tCtx ktesting.TContext) {
		fetchedPod, err := tCtx.Client().CoreV1().Pods(tCtx.Namespace()).Get(tCtx, pod2.Name, metav1.GetOptions{})
		tCtx.ExpectNoError(err, "get pod %s", pod2.Name)
		tCtx.Expect(hasAssignedCpusetDownwardAPIItem(fetchedPod)).To(gomega.BeFalse(),
			"assigned.cpuset DownwardAPI item should be dropped from pod spec when feature gate is OFF")
		tCtx.Logf("Pod %q - assigned.cpuset DownwardAPI item correctly dropped from spec", pod2.Name)
	})

	// The cpuset file should NOT exist in the container's DownwardAPI volume
	tCtx.Step("verify-cpuset-file-not-visible", func(tCtx ktesting.TContext) {
		verifyCpusetNotVisible(tCtx, savedRestConfig, pod2, "gu-container-2")
		tCtx.Logf("Pod %q container %q - assigned.cpuset file correctly not visible", pod2.Name, "gu-container-2")
	})

	tCtx.Log("Stage 6 PASS: assigned.cpuset correctly not visible in new pod when gate is OFF")
	tCtx.Log("ALL STAGES PASSED: DownwardAPIAssignedResources feature gate rollback test complete")
	// Cleanup will destroy the cluster via tCtx.CleanupCtx
}

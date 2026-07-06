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
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"runtime"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/kubernetes/test/utils/client-go/ktesting"
	"k8s.io/kubernetes/test/utils/localupcluster"
)

// progressReader wraps an io.Reader and logs download progress periodically.
type progressReader struct {
	reader      io.Reader
	total       int64
	downloaded  int64
	tCtx        ktesting.TContext
	lastLogTime time.Time
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.reader.Read(p)
	pr.downloaded += int64(n)
	// Log every 5 seconds, or on EOF, or if total is unknown.
	if pr.total > 0 && (time.Since(pr.lastLogTime) > 5*time.Second || err == io.EOF) {
		percentage := float64(pr.downloaded) / float64(pr.total) * 100
		pr.tCtx.Logf("Downloaded %s / %s (%.1f%%)",
			humanReadableSize(pr.downloaded), humanReadableSize(pr.total), percentage)
		pr.lastLogTime = time.Now()
	} else if pr.total <= 0 && (time.Since(pr.lastLogTime) > 5*time.Second || err == io.EOF) {
		pr.tCtx.Logf("Downloaded %s (total size unknown)", humanReadableSize(pr.downloaded))
		pr.lastLogTime = time.Now()
	}
	return n, err
}

// humanReadableSize converts a byte count to a human-readable string (e.g., "150.2 MB").
func humanReadableSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
}


// ErrHTTP404 is returned by ServerDownloadURL when the version file is not found (HTTP 404).
var ErrHTTP404 = errors.New("resource not found (404)")

// SourceVersion identifies the Kubernetes git version based on hack/print-workspace-status.sh.
//
// Adapted from https://github.com/kubernetes-sigs/kind/blob/3df64e784cc0ea74125b2a2e9877817418afa3af/pkg/build/nodeimage/internal/kube/source.go#L71-L104
func SourceVersion(tCtx ktesting.TContext, kubeRoot string) (gitVersion string, dockerTag string, err error) {
	// Get the version output.
	cmd := exec.CommandContext(tCtx, "hack/print-workspace-status.sh")
	cmd.Dir = kubeRoot
	output, err := cmd.Output()
	if err != nil {
		return "", "", err
	}
	tCtx.Logf("workspace status:\n%s", output)

	// Parse it.
	for _, line := range strings.Split(string(output), "\n") {
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			continue
		}
		switch parts[0] {
		case "gitVersion":
			gitVersion = parts[1]
		case "STABLE_DOCKER_TAG":
			dockerTag = parts[1]
		}
	}
	if gitVersion == "" {
		return "", "", fmt.Errorf("could not obtain Kubernetes git version: %q", string(output))
	}
	if dockerTag == "" {
		return "", "", fmt.Errorf("could not obtain docker tag: %q", string(output))
	}
	return
}

// ServerDownloadURL constructs a download URL for a Kubernetes server tarball based on the given
// prefix, major, and minor version numbers. It performs an HTTP GET request to retrieve the version
// string from a remote text file, then builds the final tarball URL using the retrieved version,
// the current OS, and architecture. If the version file is not found (HTTP 404), it returns
// ErrHTTP404 to allow the caller to try another prefix.
//
// Parameters:
//   - tCtx: a ktesting.TContext used for test context and error handling.
//   - prefix: the release prefix (e.g., "stable", "latest").
//   - major: the major version number.
//   - minor: the minor version number.
//
// Returns:
//   - The constructed tarball download URL as a string.
//   - The version string as retrieved from the remote file.
//   - An error if the request fails, the response is invalid, or the version file is not found.
func ServerDownloadURL(tCtx ktesting.TContext, prefix string, major, minor uint) (string, string, error) {
	tCtx.Helper()
	url := fmt.Sprintf("https://dl.k8s.io/release/%s-%d.%d.txt", prefix, major, minor)
	get, err := http.NewRequestWithContext(tCtx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", fmt.Errorf("constructing GET for %s failed: %w", url, err)
	}
	resp, err := http.DefaultClient.Do(get)
	if err != nil {
		return "", "", fmt.Errorf("downloading %s failed: %w", url, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		// Caller should be able to distinguish HTTP 404
		// to try another prefix (usually 'latest' if 'stable' returns 404)
		return "", "", ErrHTTP404
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("getting %s failed: status code: %d, status: %s", url, resp.StatusCode, resp.Status)
	}
	if resp.Body == nil {
		return "", "", fmt.Errorf("empty response for %s", url)
	}
	defer func() {
		tCtx.ExpectNoError(resp.Body.Close(), "close response body")
	}()
	versionBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("reading response body for %s failed: %w", url, err)
	}
	return fmt.Sprintf("https://dl.k8s.io/release/%s/kubernetes-server-%s-%s.tar.gz", string(versionBytes), runtime.GOOS, runtime.GOARCH), string(versionBytes), nil
}

// DownloadAndUnpackBinaries downloads a Kubernetes server tarball from the given URL and
// extracts the Kubernetes cluster component binaries (kube-apiserver, kube-controller-manager,
// kube-scheduler, kubelet, kube-proxy) into the specified binDir.
func DownloadAndUnpackBinaries(tCtx ktesting.TContext, url, binDir string) {
	tCtx.Helper()
	tCtx.Logf("downloading and unpacking %s to %s", url, binDir)

	req, err := http.NewRequestWithContext(tCtx, http.MethodGet, url, nil)
	tCtx.ExpectNoError(err, "construct request")
	response, err := http.DefaultClient.Do(req)
	tCtx.ExpectNoError(err, "download")
	defer func() {
		_ = response.Body.Close()
	}()

	// Wrap the response body in a progress reader to log download progress.
	totalSize := response.ContentLength
	if totalSize > 0 {
		tCtx.Logf("Total download size: %s", humanReadableSize(totalSize))
	}
	progressReader := &progressReader{
		reader:      response.Body,
		total:       totalSize,
		tCtx:        tCtx,
		lastLogTime: time.Now(),
	}

	decompress, err := gzip.NewReader(progressReader)
	tCtx.ExpectNoError(err, "construct gzip reader")
	unpack := tar.NewReader(decompress)
	for {
		header, err := unpack.Next()
		if err == io.EOF {
			break
		}
		tCtx.ExpectNoError(err, "read tar entry")
		base := path.Base(header.Name)
		if slices.Contains(localupcluster.KubeClusterComponents, localupcluster.ClusterComponentName(base)) {
			data, err := io.ReadAll(unpack)
			tCtx.ExpectNoError(err, fmt.Sprintf("read content of %s", header.Name))
			tCtx.ExpectNoError(os.MkdirAll(binDir, 0755), "create directory for binaries")
			tCtx.ExpectNoError(os.WriteFile(path.Join(binDir, base), data, 0555), fmt.Sprintf("write content of %s", header.Name))
			tCtx.Logf("Extracted %s (%s)", base, humanReadableSize(int64(len(data))))
		}
	}
	tCtx.Logf("Download and unpack complete")
}

// DownloadPreviousReleaseBinaries orchestrates the download of the previous Kubernetes release
// binaries. It determines the current source version, calculates the previous minor version,
// downloads the tarball, and extracts the binaries.
//
// Parameters:
//   - tCtx: a ktesting.TContext used for test context and error handling.
//   - repoRoot: the root directory of the Kubernetes repository.
//   - cacheDir: optional cache directory (from KUBERNETES_SERVER_CACHE_DIR env var). If empty,
//     a temporary directory is used.
//
// Returns:
//   - binDir: the directory where the extracted binaries are stored.
//   - major: the major version number.
//   - previousMinor: the previous minor version number.
//   - gitVersion: the git version string of the current source code.
func DownloadPreviousReleaseBinaries(tCtx ktesting.TContext, repoRoot, cacheDir string) (binDir string, major uint, previousMinor uint, gitVersion string) {
	tCtx.Helper()

	// Determine the current source version.
	tCtx.Step("get source code version", func(tCtx ktesting.TContext) {
		var err error
		gitVersion, _, err = SourceVersion(tCtx, repoRoot)
		tCtx.ExpectNoError(err, "determine source code version for repo root %q", repoRoot)
		ver, err := version.ParseGeneric(gitVersion)
		tCtx.ExpectNoError(err, "parse version %s of repo root %q", gitVersion, repoRoot)
		major, previousMinor = ver.Major(), ver.Minor()-1
		if strings.Contains(gitVersion, "-alpha.0") {
			// All versions up to and including x.y.z-alpha.0 are treated as if we were
			// still the previous minor version x.(y-1).
			previousMinor--
		}
		tCtx.Logf("got version: major: %d, minor: %d, previous minor: %d", major, ver.Minor(), previousMinor)
	})

	// Determine cache directory.
	haveBinaries := false
	if cacheDir != "" {
		binDir = cacheDir
	} else {
		binDir = tCtx.TempDir()
	}

	// Get the previous release download URL.
	var previousURL, previousVersion string
	tCtx.Step("get previous release info", func(tCtx ktesting.TContext) {
		tCtx.Logf("stable release %d.%d", major, previousMinor)
		var err error
		previousURL, previousVersion, err = ServerDownloadURL(tCtx, "stable", major, previousMinor)
		if errors.Is(err, ErrHTTP404) {
			tCtx.Logf("stable doesn't exist, get latest release %d.%d", major, previousMinor)
			previousURL, previousVersion, err = ServerDownloadURL(tCtx, "latest", major, previousMinor)
		}
		tCtx.ExpectNoError(err)
		tCtx.Logf("got previous release version: %s, URL: %s", previousVersion, previousURL)
	})

	if cacheDir != "" {
		binDir = path.Join(binDir, previousVersion)
		_, err := os.Stat(path.Join(binDir, string(localupcluster.KubeClusterComponents[0])))
		if err == nil {
			haveBinaries = true
		}
	}

	if !haveBinaries {
		tCtx.Step(fmt.Sprintf("download and unpack %s", previousURL), func(tCtx ktesting.TContext) {
			DownloadAndUnpackBinaries(tCtx, previousURL, binDir)
		})
	}

	return binDir, major, previousMinor, gitVersion
}

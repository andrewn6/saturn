// Package selfupdate replaces the running binary with the latest GitHub release.
package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const repo = "andrewn6/saturn"

type release struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// Apply fetches the latest release and replaces the running binary.
// Returns (latestTag, didUpdate, err). If currentVersion already matches
// the latest tag, returns didUpdate=false and no error.
func Apply(currentVersion string) (string, bool, error) {
	rel, err := latestRelease()
	if err != nil {
		return "", false, err
	}
	if rel.TagName == currentVersion && currentVersion != "" {
		return rel.TagName, false, nil
	}

	assetName := fmt.Sprintf("saturn_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	var assetURL string
	for _, a := range rel.Assets {
		if a.Name == assetName {
			assetURL = a.URL
			break
		}
	}
	if assetURL == "" {
		return "", false, fmt.Errorf("release %s has no asset %s", rel.TagName, assetName)
	}

	exe, err := os.Executable()
	if err != nil {
		return "", false, err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	binBytes, err := downloadAndExtract(assetURL)
	if err != nil {
		return "", false, err
	}

	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".saturn-update-*")
	if err != nil {
		return "", false, fmt.Errorf("temp file (try sudo?): %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(binBytes); err != nil {
		tmp.Close()
		return "", false, err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return "", false, err
	}
	tmp.Close()

	if err := os.Rename(tmpPath, exe); err != nil {
		return "", false, fmt.Errorf("install (try sudo?): %w", err)
	}
	return rel.TagName, true, nil
}

func latestRelease() (*release, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	req, _ := http.NewRequest("GET",
		"https://api.github.com/repos/"+repo+"/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("github API: %s", resp.Status)
	}
	var rel release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

func downloadAndExtract(url string) ([]byte, error) {
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("download: %s", resp.Status)
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("archive contained no 'saturn' binary")
		}
		if err != nil {
			return nil, err
		}
		if filepath.Base(h.Name) == "saturn" {
			return io.ReadAll(tr)
		}
	}
}

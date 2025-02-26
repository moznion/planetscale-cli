// Package update is checking for a new version of Pscale and informs the user
// to update. Most of the logic is copied from cli/cli:
// https://github.com/cli/cli/blob/trunk/internal/update/update.go and updated
// to our own needs.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/fatih/color"
	"github.com/hashicorp/go-version"
	"github.com/planetscale/cli/internal/cmdutil"
	"github.com/planetscale/cli/internal/config"
	"gopkg.in/yaml.v2"

	exec "golang.org/x/sys/execabs"
)

type UpdateInfo struct {
	Update      bool
	Reason      string
	ReleaseInfo *ReleaseInfo
}

func (ui *UpdateInfo) PrintUpdateHint(buildVersion string) {
	if ui == nil || !ui.Update {
		return
	}

	fmt.Fprintf(color.Error, "\n%s %s → %s\n",
		color.BlueString("A new release of pscale is available:"),
		color.CyanString(buildVersion),
		color.CyanString(ui.ReleaseInfo.Version))

	var binpath string
	if exepath, err := os.Executable(); err == nil {
		binpath = exepath
	} else if path, err := exec.LookPath("pscale"); err == nil {
		binpath = path
	}

	if cmdutil.IsUnderHomebrew(binpath) {
		fmt.Fprintf(os.Stderr, "To upgrade, run: %s\n", "brew update && brew upgrade pscale")
	}
	fmt.Fprintf(color.Error, "%s\n", color.YellowString(ui.ReleaseInfo.URL))
}

// ReleaseInfo stores information about a release
type ReleaseInfo struct {
	Version     string    `json:"tag_name"`
	URL         string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
}

// StateEntry stores the information we have checked for a new version. It's
// used to decide whether to check for a new version or not.
type StateEntry struct {
	CheckedForUpdateAt time.Time   `yaml:"checked_for_update_at"`
	LatestRelease      ReleaseInfo `yaml:"latest_release"`
}

// CheckVersion checks for the given build version whether there is a new
// version of the CLI or not.
func CheckVersion(ctx context.Context, buildVersion string) (*UpdateInfo, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	path, err := stateFilePath()
	if err != nil {
		return nil, err
	}

	updateInfo, err := checkVersion(
		ctx,
		buildVersion,
		path,
		latestVersion,
	)
	if err != nil {
		return nil, fmt.Errorf("skipping update, error: %s", err)
	}
	return updateInfo, nil
}

func checkVersion(
	ctx context.Context,
	buildVersion, path string,
	latestVersionFn func(ctx context.Context, addr string) (*ReleaseInfo, error),
) (*UpdateInfo, error) {
	stateEntry, _ := getStateEntry(path)
	if stateEntry != nil && time.Since(stateEntry.CheckedForUpdateAt).Hours() < 24 {
		return &UpdateInfo{
			Update: false,
			Reason: "Latest version was already checked",
		}, nil
	}

	addr := "https://api.github.com/repos/planetscale/cli/releases/latest"
	info, err := latestVersionFn(ctx, addr)
	if err != nil {
		return nil, err
	}

	err = setStateEntry(path, time.Now(), *info)
	if err != nil {
		return nil, err
	}

	v1, err := version.NewVersion(info.Version)
	if err != nil {
		return nil, err
	}

	v2, err := version.NewVersion(buildVersion)
	if err != nil {
		return nil, err
	}

	if v1.LessThanOrEqual(v2) {
		return &UpdateInfo{
			Update: false,
			Reason: fmt.Sprintf("Latest version (%s) is less than or equal to current build version (%s)",
				info.Version, buildVersion),
			ReleaseInfo: info,
		}, nil
	}

	return &UpdateInfo{
		Update: true,
		Reason: fmt.Sprintf("Latest version (%s) is greater than the current build version (%s)",
			info.Version, buildVersion),
		ReleaseInfo: info,
	}, nil
}

func latestVersion(ctx context.Context, addr string) (*ReleaseInfo, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", addr, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	getToken := func() string {
		if t := os.Getenv("GH_TOKEN"); t != "" {
			return t
		}
		return os.Getenv("GITHUB_TOKEN")
	}

	if token := getToken(); token != "" {
		req.Header.Set("Authorization", fmt.Sprintf("token %s", token))
	}

	client := &http.Client{Timeout: time.Second * 15}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	success := resp.StatusCode >= 200 && resp.StatusCode < 300
	if !success {
		return nil, fmt.Errorf("error fetching latest release: %v", string(out))
	}

	var info *ReleaseInfo
	err = json.Unmarshal(out, &info)
	if err != nil {
		return nil, err
	}

	return info, nil
}

func getStateEntry(stateFilePath string) (*StateEntry, error) {
	content, err := os.ReadFile(stateFilePath)
	if err != nil {
		return nil, err
	}

	var stateEntry StateEntry
	err = yaml.Unmarshal(content, &stateEntry)
	if err != nil {
		return nil, err
	}

	return &stateEntry, nil
}

func setStateEntry(stateFilePath string, t time.Time, r ReleaseInfo) error {
	data := StateEntry{
		CheckedForUpdateAt: t,
		LatestRelease:      r,
	}

	content, err := yaml.Marshal(data)
	if err != nil {
		return err
	}
	_ = os.WriteFile(stateFilePath, content, 0o600)

	return nil
}

func stateFilePath() (string, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(dir, "state.yml"), nil
}

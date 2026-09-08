package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"warpseed/internal/applog"
)

/* Update notification.
 *
 * Notify only. warpseed is a portable single exe with no installer, so a
 * self-replacing updater would mean the app rewriting the file it is running
 * from, in a directory the user chose, possibly on a network share or behind
 * an antivirus that treats self-modification as exactly what it looks like.
 * The failure mode is a corrupted exe and no app. Telling the user and opening
 * the releases page costs one click and cannot break anything.
 *
 * The reason this exists at all: v1.0.0 has more downloads than every release
 * since combined, so most users are on a build that predates the NTFS
 * zero-fill fix and this month's data-safety work, with no way to find out.
 */

const (
	// releases/latest excludes drafts and prereleases by definition, so
	// nothing needs filtering on our side.
	latestReleaseURL = "https://api.github.com/repos/ZyraLabs/warpseed/releases/latest"

	// Deliberately carries no version. GitHub does not show us its logs, so a
	// version here would buy warpseed nothing and reveal strictly more.
	updateUserAgent = "warpseed (+https://github.com/ZyraLabs/warpseed)"

	// The body is about 7 KB. A captive portal or a hijacked response must not
	// be able to balloon memory on a machine left running overnight.
	maxReleaseBody = 1 << 20

	updateCheckTimeout = 10 * time.Second

	// One check per run, after a delay: startup is when the app is busiest and
	// an update is never urgent enough to compete with it.
	updateCheckDelay = 45 * time.Second
)

// Settings keys. updates.check is user-facing and validated; the other two are
// bookkeeping the Go side writes directly.
const (
	setUpdateCheck     = "updates.check"
	setUpdateDismissed = "updates.dismissed"
	setUpdateLastCheck = "updates.last_check"
)

// UpdateInfo is what the banner needs. Available is false whenever the user is
// already current or is AHEAD of the latest release — a dev build must never
// be offered a downgrade.
type UpdateInfo struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	URL       string `json:"url"`
	Available bool   `json:"available"`
	Dismissed bool   `json:"dismissed"`
}

// AppVersion is the running build, read from the embedded manifest. The
// frontend had its own copy of this string and it drifted to 1.1.1 — which
// then went out in the subject line of every emailed bug report — so there is
// exactly one source of it now.
func (a *App) AppVersion() string { return appVersion }

// CheckForUpdate asks GitHub for the latest release. Errors are returned
// rather than swallowed because this is the manual "Check now" path, where
// silence would look like a hang; the automatic check discards them.
func (a *App) CheckForUpdate() (UpdateInfo, error) {
	info := UpdateInfo{Current: appVersion}

	latest, url, err := a.fetchLatestRelease(a.ctx)
	if err != nil {
		return info, err
	}
	info.Latest, info.URL = latest, url

	if a.store != nil {
		if serr := a.store.SetSetting(setUpdateLastCheck, nowRFC3339()); serr != nil {
			log.Printf("update: record check time: %v", serr)
		}
		info.Dismissed = a.store.Setting(setUpdateDismissed, "") == latest
	}
	info.Available = newerVersion(latest, appVersion)
	return info, nil
}

// DismissUpdate silences the banner for ONE version. Dismissing 1.1.9 must not
// also silence 1.2.0 — the whole point is that the next release gets a fresh
// chance to be noticed.
func (a *App) DismissUpdate(version string) error {
	if a.store == nil {
		return errNoStore
	}
	return a.store.SetSetting(setUpdateDismissed, strings.TrimSpace(version))
}

// startUpdateCheck runs one check per launch, in the background, and emits only
// when there is something to say.
//
// Every failure here is silent by design. A check that cannot reach GitHub — no
// network, a captive portal, a corporate proxy, or simply another tool on the
// same IP having spent the unauthenticated hourly budget — is not the user's
// problem and must never produce a banner, a toast, or a log line they would
// read as a fault in warpseed.
func (a *App) startUpdateCheck() {
	if a.store == nil || a.store.Setting(setUpdateCheck, "1") != "1" {
		return
	}
	go func() {
		select {
		case <-a.ctx.Done():
			return
		case <-time.After(updateCheckDelay):
		}
		info, err := a.CheckForUpdate()
		if err != nil {
			applog.Debugf("update: check failed (ignored): %v", err)
			return
		}
		if !info.Available || info.Dismissed {
			return
		}
		a.sink.Emit("update:available", info)
	}()
}

func (a *App) fetchLatestRelease(parent context.Context) (version, url string, err error) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, updateCheckTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestReleaseURL, nil)
	if err != nil {
		return "", "", err
	}
	// GitHub answers 403 to an empty User-Agent, so this header is not
	// optional politeness.
	req.Header.Set("User-Agent", updateUserAgent)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// 403/429 here is the shared unauthenticated rate limit, keyed on the
		// source IP and exhaustible by anything else on the same network.
		// Ordinary failure, no retry.
		return "", "", fmt.Errorf("github returned %s", resp.Status)
	}

	var rel struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxReleaseBody)).Decode(&rel); err != nil {
		return "", "", fmt.Errorf("decode release: %w", err)
	}
	v := strings.TrimPrefix(strings.TrimSpace(rel.TagName), "v")
	if v == "" {
		return "", "", fmt.Errorf("release has no tag")
	}
	return v, rel.HTMLURL, nil
}

// newerVersion reports whether latest is a strictly higher release than
// current, comparing dotted numeric components.
//
// It answers false whenever it cannot be certain — an unparseable component, or
// a current build that is AHEAD of the newest release. That last case is not
// hypothetical: the version is bumped in the commit that ships it, so between a
// bump and its tag every development build is newer than the latest release.
// Offering those a downgrade would be worse than saying nothing.
func newerVersion(latest, current string) bool {
	l, okL := parseVersion(latest)
	c, okC := parseVersion(current)
	if !okL || !okC {
		return false
	}
	for i := 0; i < len(l) || i < len(c); i++ {
		var a, b int
		if i < len(l) {
			a = l[i]
		}
		if i < len(c) {
			b = c[i]
		}
		if a != b {
			return a > b
		}
	}
	return false
}

func parseVersion(v string) ([]int, bool) {
	v = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "v"))
	if v == "" {
		return nil, false
	}
	// Ignore any pre-release or build suffix: 1.2.0-rc1 compares as 1.2.0.
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }

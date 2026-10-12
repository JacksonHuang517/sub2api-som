//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type updateServiceCacheStub struct {
	data string
}

func (s *updateServiceCacheStub) GetUpdateInfo(context.Context) (string, error) {
	if s.data == "" {
		return "", errors.New("cache miss")
	}
	return s.data, nil
}

func (s *updateServiceCacheStub) SetUpdateInfo(_ context.Context, data string, _ time.Duration) error {
	s.data = data
	return nil
}

type updateServiceGitHubClientStub struct {
	release        *GitHubRelease
	recentReleases []*GitHubRelease
	recentErr      error
	latestRepo     string
	recentRepo     string
}

func (s *updateServiceGitHubClientStub) FetchLatestRelease(_ context.Context, repo string) (*GitHubRelease, error) {
	s.latestRepo = repo
	return s.release, nil
}

func (s *updateServiceGitHubClientStub) FetchRecentReleases(_ context.Context, repo string, _ int) ([]*GitHubRelease, error) {
	s.recentRepo = repo
	return s.recentReleases, s.recentErr
}

func (s *updateServiceGitHubClientStub) DownloadFile(context.Context, string, string, int64) error {
	panic("DownloadFile should not be called when no update is available")
}

func (s *updateServiceGitHubClientStub) FetchChecksumFile(context.Context, string) ([]byte, error) {
	panic("FetchChecksumFile should not be called when no update is available")
}

func TestUpdateServicePerformUpdateNoUpdateReturnsSentinel(t *testing.T) {
	svc := NewUpdateService(
		&updateServiceCacheStub{},
		&updateServiceGitHubClientStub{
			release: &GitHubRelease{
				TagName: "v0.1.132",
				Name:    "v0.1.132",
			},
		},
		"0.1.132",
		"release",
	)

	err := svc.PerformUpdate(context.Background())

	require.Error(t, err)
	require.True(t, errors.Is(err, ErrNoUpdateAvailable))
	require.ErrorIs(t, err, ErrNoUpdateAvailable)
}

func newRollbackTestService(current string, releases []*GitHubRelease) *UpdateService {
	return NewUpdateService(
		&updateServiceCacheStub{},
		&updateServiceGitHubClientStub{recentReleases: releases},
		current,
		"release",
	)
}

func TestUpdateServiceListRollbackVersionsFiltersAndCaps(t *testing.T) {
	releases := []*GitHubRelease{
		{TagName: "v0.1.148", PublishedAt: "2026-07-09T00:00:00Z"},                       // newer than current: excluded
		{TagName: "v0.1.147", PublishedAt: "2026-07-08T00:00:00Z"},                       // current: excluded
		{TagName: "v0.1.146-rc1", PublishedAt: "2026-07-07T12:00:00Z", Prerelease: true}, // prerelease: excluded
		{TagName: "v0.1.146", PublishedAt: "2026-07-07T00:00:00Z"},
		{TagName: "v0.1.145", PublishedAt: "2026-07-06T00:00:00Z", Draft: true}, // draft: excluded
		{TagName: "v0.1.144", PublishedAt: "2026-07-05T00:00:00Z"},
		{TagName: "v0.1.144", PublishedAt: "2026-07-05T00:00:00Z"}, // duplicate: excluded
		{TagName: "v0.1.143", PublishedAt: "2026-07-04T00:00:00Z"},
		{TagName: "v0.1.142", PublishedAt: "2026-07-03T00:00:00Z"}, // beyond cap of 3: excluded
	}
	svc := newRollbackTestService("0.1.147", releases)

	versions, err := svc.ListRollbackVersions(context.Background())

	require.NoError(t, err)
	require.Len(t, versions, 3)
	require.Equal(t, "0.1.146", versions[0].Version)
	require.Equal(t, "0.1.144", versions[1].Version)
	require.Equal(t, "0.1.143", versions[2].Version)
}

func TestUpdateServiceListRollbackVersionsSortsUnorderedInput(t *testing.T) {
	releases := []*GitHubRelease{
		{TagName: "v0.1.144"},
		{TagName: "v0.1.146"},
		{TagName: "v0.1.145"},
	}
	svc := newRollbackTestService("0.1.147", releases)

	versions, err := svc.ListRollbackVersions(context.Background())

	require.NoError(t, err)
	require.Len(t, versions, 3)
	require.Equal(t, "0.1.146", versions[0].Version)
	require.Equal(t, "0.1.145", versions[1].Version)
	require.Equal(t, "0.1.144", versions[2].Version)
}

func TestUpdateServiceListRollbackVersionsEmptyWhenNoneOlder(t *testing.T) {
	releases := []*GitHubRelease{
		{TagName: "v0.1.147"},
		{TagName: "v0.1.148"},
	}
	svc := newRollbackTestService("0.1.147", releases)

	versions, err := svc.ListRollbackVersions(context.Background())

	require.NoError(t, err)
	require.Empty(t, versions)
}

func TestUpdateServiceListRollbackVersionsPropagatesFetchError(t *testing.T) {
	svc := NewUpdateService(
		&updateServiceCacheStub{},
		&updateServiceGitHubClientStub{recentErr: errors.New("github unavailable")},
		"0.1.147",
		"release",
	)

	_, err := svc.ListRollbackVersions(context.Background())

	require.Error(t, err)
	require.Contains(t, err.Error(), "github unavailable")
}

func TestUpdateServiceRollbackToVersionRejectsDisallowedTargets(t *testing.T) {
	releases := []*GitHubRelease{
		{TagName: "v0.1.148"},
		{TagName: "v0.1.147"},
		{TagName: "v0.1.146"},
		{TagName: "v0.1.145"},
		{TagName: "v0.1.144"},
		{TagName: "v0.1.143"},
		{TagName: "v0.1.142"},
	}
	svc := newRollbackTestService("0.1.147", releases)

	for _, target := range []string{
		"",         // empty
		"0.1.147",  // current version
		"v0.1.147", // current version with prefix
		"0.1.148",  // newer than current
		"0.1.142",  // older than the 3 most recent
		"9.9.9",    // nonexistent
	} {
		err := svc.RollbackToVersion(context.Background(), target)
		require.ErrorIs(t, err, ErrRollbackVersionNotAllowed, "target %q should be rejected", target)
	}
}

func TestUpdateServiceRollbackToVersionAcceptsVPrefix(t *testing.T) {
	// No platform asset in the release: the target passes the allowlist check
	// and fails later at asset lookup, proving the version itself was accepted.
	releases := []*GitHubRelease{
		{TagName: "v0.1.147"},
		{TagName: "v0.1.146"},
	}
	svc := newRollbackTestService("0.1.147", releases)

	err := svc.RollbackToVersion(context.Background(), "v0.1.146")

	require.Error(t, err)
	require.NotErrorIs(t, err, ErrRollbackVersionNotAllowed)
	require.Contains(t, err.Error(), "no compatible release found")
}

func TestUpdateServiceCustomSourceAndCacheIsolation(t *testing.T) {
	t.Setenv("UPDATE_RELEASE_REPO", "som-owner/som-api")
	client := &updateServiceGitHubClientStub{release: &GitHubRelease{TagName: "v0.2.15-som.2"}}
	cache := &updateServiceCacheStub{}
	svc := NewUpdateService(cache, client, "0.2.15-som.1", "release")
	info, err := svc.CheckUpdate(context.Background(), true)
	require.NoError(t, err)
	require.True(t, info.HasUpdate)
	require.Equal(t, "som-owner/som-api", info.ReleaseRepo)
	require.Equal(t, info.ReleaseRepo, client.latestRepo)
	_, err = svc.ListRollbackVersions(context.Background())
	require.NoError(t, err)
	require.Equal(t, info.ReleaseRepo, client.recentRepo)
	t.Setenv("UPDATE_RELEASE_REPO", "another/repo")
	other := NewUpdateService(cache, client, "0.2.15", "release")
	_, err = other.getFromCache(context.Background())
	require.ErrorContains(t, err, "source mismatch")
}

func TestUpdateServiceInvalidSourceDoesNotFetch(t *testing.T) {
	for _, repo := range []string{"https://github.com/a/b", "a/b/c", "../repo", "owner/repo?x=1", "owner/repo#fragment"} {
		t.Run(repo, func(t *testing.T) {
			t.Setenv("UPDATE_RELEASE_REPO", repo)
			client := &updateServiceGitHubClientStub{}
			svc := NewUpdateService(&updateServiceCacheStub{}, client, "0.2.15", "release")
			_, err := svc.CheckUpdate(context.Background(), true)
			require.ErrorContains(t, err, "invalid UPDATE_RELEASE_REPO")
			_, err = svc.ListRollbackVersions(context.Background())
			require.Error(t, err)
			require.Empty(t, client.latestRepo)
			require.Empty(t, client.recentRepo)
		})
	}
}

func TestUpdateServiceCustomAssetsRequireExactArchiveAndChecksum(t *testing.T) {
	t.Setenv("UPDATE_RELEASE_REPO", "som-owner/som-api")
	svc := NewUpdateService(&updateServiceCacheStub{}, &updateServiceGitHubClientStub{}, "0.2.15", "release")
	expected := "sub2api_0.2.16-som.1_" + svc.getArchiveName() + ".tar.gz"
	for _, name := range []string{"other_" + expected, expected + ".zip", "sub2api_0.2.17_" + svc.getArchiveName() + ".tar.gz"} {
		err := svc.applyReleaseAssets(context.Background(), []Asset{{Name: name}}, "0.2.16-som.1")
		require.ErrorContains(t, err, "no compatible release")
	}
	err := svc.applyReleaseAssets(context.Background(), []Asset{{Name: expected, DownloadURL: "https://github.com/som-owner/som-api/releases/download/v0.2.16-som.1/" + expected}}, "0.2.16-som.1")
	require.ErrorContains(t, err, "requires checksums.txt")
}

func TestCompareVersionsSOMRevisions(t *testing.T) {
	for _, pair := range [][2]string{{"0.2.15", "0.2.15-som.1"}, {"0.2.15-som.2", "0.2.15-som.10"}, {"v0.2.15-som.10", "0.2.16-som.1"}} {
		require.Equal(t, -1, compareVersions(pair[0], pair[1]))
		require.Equal(t, 1, compareVersions(pair[1], pair[0]))
	}
	require.Zero(t, compareVersions("0.2.15-rc.1", "0.2.15"))
	require.Zero(t, compareVersions("0.2.15-som.invalid", "0.2.15"))
}

func TestUpdateServiceLegacyCacheOnlyAcceptedForUpstream(t *testing.T) {
	t.Setenv("UPDATE_RELEASE_REPO", "")
	cache := &updateServiceCacheStub{data: fmt.Sprintf(`{"latest":"0.2.16","timestamp":%d}`, time.Now().Unix())}
	upstream := NewUpdateService(cache, &updateServiceGitHubClientStub{}, "0.2.15", "release")
	info, err := upstream.getFromCache(context.Background())
	require.NoError(t, err)
	require.Equal(t, githubRepo, info.ReleaseRepo)
	t.Setenv("UPDATE_RELEASE_REPO", "som-owner/som-api")
	custom := NewUpdateService(cache, &updateServiceGitHubClientStub{}, "0.2.15", "release")
	_, err = custom.getFromCache(context.Background())
	require.ErrorContains(t, err, "source mismatch")
}

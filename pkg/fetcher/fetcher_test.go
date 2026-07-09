// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package fetcher

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

func TestVerifyChecksum(t *testing.T) {
	t.Parallel()
	sha := func(sum string) *fv1.Checksum { return &fv1.Checksum{Type: fv1.ChecksumTypeSHA256, Sum: sum} }

	require.NoError(t, verifyChecksum(sha("abc"), sha("abc")))
	require.Error(t, verifyChecksum(sha("abc"), sha("def")), "mismatched sums must fail")
	require.Error(t, verifyChecksum(sha("abc"), &fv1.Checksum{Type: "md5", Sum: "abc"}), "unsupported type must fail")
}

func TestWriteSecretOrConfigMap(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	data := map[string][]byte{"a": []byte("alpha"), "b": []byte("beta")}
	require.NoError(t, writeSecretOrConfigMap(data, dir))

	got, err := os.ReadFile(filepath.Join(dir, "a"))
	require.NoError(t, err)
	assert.Equal(t, "alpha", string(got))

	require.Error(t, writeSecretOrConfigMap(data, filepath.Join(dir, "does", "not", "exist")),
		"writing into a missing directory must error")
}

func TestMakeVolumeDir(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "nested", "vol")
	require.NoError(t, makeVolumeDir(dir))
	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestRename(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// rename is confined to the shared volume; src and dst live under it.
	f := &Fetcher{logger: loggerfactory.GetLogger(), sharedVolumePath: dir}
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	require.NoError(t, os.WriteFile(src, []byte("x"), 0600))

	require.NoError(t, f.rename(src, dst))
	_, err := os.Stat(dst)
	assert.NoError(t, err)

	require.Error(t, f.rename(filepath.Join(dir, "ghost"), dst), "renaming a missing file must error")
}

func TestHTTPClientForURL(t *testing.T) {
	t.Parallel()
	storage := &http.Client{}
	general := &http.Client{}
	f := &Fetcher{httpClient: general, storageHTTPClient: storage}

	assert.Same(t, storage, f.httpClientForURL("http://storagesvc/v1/archive?id=1"), "storagesvc archive URL uses the signing client")
	assert.Same(t, general, f.httpClientForURL("http://example.com/code.zip"), "external URL uses the general client")
}

func TestVersionHandler(t *testing.T) {
	t.Parallel()
	f := &Fetcher{logger: loggerfactory.GetLogger()}
	rec := httptest.NewRecorder()
	f.VersionHandler(rec, httptest.NewRequest(http.MethodGet, "/version", nil))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")
	assert.NotEmpty(t, rec.Body.String())
}

func TestFetchHandlerMethodAndBody(t *testing.T) {
	t.Parallel()
	f := &Fetcher{logger: loggerfactory.GetLogger()}

	rec := httptest.NewRecorder()
	f.FetchHandler(rec, httptest.NewRequest(http.MethodGet, "/fetch", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)

	rec = httptest.NewRecorder()
	f.FetchHandler(rec, httptest.NewRequest(http.MethodPost, "/fetch", strings.NewReader("not json")))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestSpecializeHandlerMethodAndBody(t *testing.T) {
	t.Parallel()
	f := &Fetcher{logger: loggerfactory.GetLogger()}

	rec := httptest.NewRecorder()
	f.SpecializeHandler(rec, httptest.NewRequest(http.MethodGet, "/specialize", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)

	rec = httptest.NewRecorder()
	f.SpecializeHandler(rec, httptest.NewRequest(http.MethodPost, "/specialize", strings.NewReader("{bad")))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestFetch(t *testing.T) {
	newFetcher := func(t *testing.T) *Fetcher {
		return &Fetcher{logger: loggerfactory.GetLogger(), sharedVolumePath: t.TempDir()}
	}

	t.Run("literal source archive is written and placed", func(t *testing.T) {
		f := newFetcher(t)
		pkg := &fv1.Package{}
		pkg.Spec.Source = fv1.Archive{Literal: []byte("function-source")}
		req := FunctionFetchRequest{FetchType: fv1.FETCH_SOURCE, Filename: "out"}

		code, err := f.Fetch(t.Context(), pkg, req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, code)

		got, err := os.ReadFile(filepath.Join(f.sharedVolumePath, "out"))
		require.NoError(t, err)
		assert.Equal(t, "function-source", string(got))
	})

	t.Run("unknown fetch type errors", func(t *testing.T) {
		f := newFetcher(t)
		code, err := f.Fetch(t.Context(), &fv1.Package{}, FunctionFetchRequest{FetchType: FetchRequestType(99), Filename: "x"})
		require.Error(t, err)
		assert.Equal(t, http.StatusBadRequest, code)
	})

	t.Run("deployment fetch on an unbuilt package errors", func(t *testing.T) {
		f := newFetcher(t)
		pkg := &fv1.Package{}
		pkg.Status.BuildStatus = fv1.BuildStatusFailed
		code, err := f.Fetch(t.Context(), pkg, FunctionFetchRequest{FetchType: fv1.FETCH_DEPLOYMENT, Filename: "x"})
		require.Error(t, err)
		assert.Equal(t, http.StatusInternalServerError, code)
	})
}

func TestEnvSpecializeRetryDelay(t *testing.T) {
	t.Parallel()
	want := []time.Duration{
		25 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond,
		200 * time.Millisecond, 400 * time.Millisecond, 500 * time.Millisecond,
		500 * time.Millisecond,
	}
	for i, w := range want {
		assert.Equalf(t, w, envSpecializeRetryDelay(i), "attempt %d", i)
	}
	// Large attempt indices must stay capped (no overflow back below the cap).
	assert.Equal(t, 500*time.Millisecond, envSpecializeRetryDelay(1000))
	// The wait budget must cover the previous schedule's worst case
	// (sum of 500*2i ms for i in [0,29) ≈ 406s) so no environment that
	// previously had time to start is now cut off.
	assert.GreaterOrEqual(t, envSpecializeWaitBudget, 406*time.Second)
}

func TestPkgRetrySchedules(t *testing.T) {
	t.Parallel()
	var notFoundTotal time.Duration
	for _, d := range pkgNotFoundRetrySchedule {
		notFoundTotal += d
	}
	// The summed not-found window guards the apiserver create-then-get race;
	// it must cover the previous schedule's 500ms (50+100+150+200).
	assert.GreaterOrEqual(t, notFoundTotal, 500*time.Millisecond)
	// First retry must stay cheap — that's the point of the reshape.
	assert.LessOrEqual(t, pkgNotFoundRetrySchedule[0], 25*time.Millisecond)

	var dialTotal time.Duration
	for _, d := range pkgDialRetrySchedule {
		dialTotal += d
	}
	// The dial schedule exists for istio/envoy warm-up; keep it coarse
	// (≥ the previous 5s total).
	assert.GreaterOrEqual(t, dialTotal, 5*time.Second)
}

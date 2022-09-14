package container

import (
	"fmt"
	"github.com/fission/fission/pkg/utils/loggerfactory"
	"os"
	"testing"
)

func TestGetObjectReaperInterval(t *testing.T) {
	logger := loggerfactory.GetLogger()

	var want int

	// Test default reaper interval
	want = 1
	got := getObjectReaperInterval(logger, want)
	if want != got {
		t.Fatalf(`Get default ObjectReaperInterval failed. Want %d, Got %d`, want, got)
	}

	// Test when only newdeploy reaper interval set
	want = 2
	os.Setenv("CONTAINERMGR_OBJECT_REAPER_INTERVAL", fmt.Sprint(want))
	os.Unsetenv("OBJECT_REAPER_INTERVAL")
	got = getObjectReaperInterval(logger, 5)
	if want != got {
		t.Fatalf(`%d %d`, want, got)
	}

	// Test when only global reaper interval set
	want = 3
	os.Unsetenv("CONTAINERMGR_OBJECT_REAPER_INTERVAL")
	os.Setenv("OBJECT_REAPER_INTERVAL", fmt.Sprint(want))
	got = getObjectReaperInterval(logger, 5)
	if want != got {
		t.Fatalf(`%d %d`, want, got)
	}

	// Test when broken newdeploy reaper interval set
	want = 4
	os.Setenv("CONTAINERMGR_OBJECT_REAPER_INTERVAL", "just some string!")
	os.Unsetenv("OBJECT_REAPER_INTERVAL")
	got = getObjectReaperInterval(logger, want)
	if want != got {
		t.Fatalf(`%d %d`, want, got)
	}

	// Test when empty newdeploy reaper interval set
	want = 5
	os.Setenv("CONTAINERMGR_OBJECT_REAPER_INTERVAL", "")
	os.Unsetenv("OBJECT_REAPER_INTERVAL")
	got = getObjectReaperInterval(logger, 5)
	if want != got {
		t.Fatalf(`%d %d`, want, got)
	}
}

package fscache

import (
	"log"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
)

func panicIf(err error) {
	if err != nil {
		log.Panicf("Error: %v", err)
	}
}

func TestFunctionServiceCache(t *testing.T) {
	config := zap.NewDevelopmentConfig()
	config.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	logger, err := config.Build()
	panicIf(err)

	fsc := MakeFunctionServiceCache(logger)
	require.NotNil(t, fsc)

	var fsvc *FuncSvc
	now := time.Now()

	objects := []apiv1.ObjectReference{
		{
			Kind:       "pod",
			Name:       "xxx",
			APIVersion: "v1",
			Namespace:  "fission-function",
		},
		{
			Kind:       "pod",
			Name:       "xxx2",
			APIVersion: "v1",
			Namespace:  "fission-function",
		},
	}

	fsvc = &FuncSvc{
		Function: &metav1.ObjectMeta{
			Name: "foo",
			UID:  "1212",
		},
		Environment: &fv1.Environment{
			ObjectMeta: metav1.ObjectMeta{
				Name: "foo-env",
				UID:  "2323",
			},
			Spec: fv1.EnvironmentSpec{
				Version: 1,
				Runtime: fv1.Runtime{
					Image: "fission/foo-env",
				},
				Builder: fv1.Builder{},
			},
		},
		Address:           "xxx",
		KubernetesObjects: objects,
		Ctime:             now,
		Atime:             now,
	}
	_, err = fsc.Add(*fsvc)
	require.NoError(t, err)

	_, err = fsc.GetByFunction(fsvc.Function)
	require.NoError(t, err)

	f, err := fsc.GetByFunctionUID(fsvc.Function.UID)
	require.NoError(t, err)

	fsvc.Atime = f.Atime
	fsvc.Ctime = f.Ctime
	require.Equal(t, fsvc.Address, f.Address)

	err = fsc.TouchByAddress(fsvc.Address)
	require.NoError(t, err)

	// TODO: fix flaky test
	// deleted, err := fsc.DeleteOld(fsvc, 0)
	// require.NoError(t, err)
	// require.False(t, deleted)

	_, err = fsc.GetByFunction(fsvc.Function)
	require.NoError(t, err)

	_, err = fsc.GetByFunctionUID(fsvc.Function.UID)
	require.NoError(t, err)
}

func TestFunctionServiceNewCache(t *testing.T) {
	logger, err := zap.NewDevelopment()
	panicIf(err)

	fsc := MakeFunctionServiceCache(logger)
	require.NotNil(t, fsc)

	var fsvc *FuncSvc
	now := time.Now()

	objects := []apiv1.ObjectReference{
		{
			Kind:       "pod",
			Name:       "xxx",
			APIVersion: "v1",
			Namespace:  "fission-function",
		},
		{
			Kind:       "pod",
			Name:       "xxx2",
			APIVersion: "v1",
			Namespace:  "fission-function",
		},
	}

	fsvc = &FuncSvc{
		Function: &metav1.ObjectMeta{
			Name: "foo",
			UID:  "1212",
		},
		Environment: &fv1.Environment{
			ObjectMeta: metav1.ObjectMeta{
				Name: "foo-env",
				UID:  "2323",
			},
			Spec: fv1.EnvironmentSpec{
				Version: 1,
				Runtime: fv1.Runtime{
					Image: "fission/foo-env",
				},
				Builder: fv1.Builder{},
			},
		},
		Address:           "xxx",
		KubernetesObjects: objects,
		CPULimit:          resource.MustParse("5m"),
		Ctime:             now.Add(-2 * time.Minute),
		Atime:             now.Add(-1 * time.Minute),
	}
	fn := &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{
			Name: "foo",
			UID:  "1212",
		},
	}

	ctx := t.Context()

	fsc.AddFunc(ctx, *fsvc, 10, fn.GetRetainPods())
	concurrency := 10
	_, err = fsc.GetFuncSvc(ctx, fsvc.Function, 5, concurrency)
	require.NoError(t, err)

	// key := fmt.Sprintf("%v_%v", cancel.UID, fn.ObjectMeta.ResourceVersion)
	key := crd.CacheKeyURGFromMeta(&fn.ObjectMeta)
	fsc.MarkAvailable(key, fsvc.Address)

	_, err = fsc.GetFuncSvc(ctx, fsvc.Function, 5, concurrency)
	require.NoError(t, err)

	for i := 0; i < 2; i++ {
		fsc.MarkAvailable(key, fsvc.Address)
	}
	vals, err := fsc.ListOldForPool(30 * time.Second)
	require.NoError(t, err)
	require.Equal(t, 0, len(vals))

	vals, err = fsc.ListOldForPool(0)
	require.NoError(t, err)
	require.Equal(t, 1, len(vals))

	fsvc.Address = "xxx2"
	fn.Spec.RetainPods = 2
	fsc.AddFunc(ctx, *fsvc, 10, fn.GetRetainPods())

	vals, err = fsc.ListOldForPool(0)
	require.NoError(t, err)
	require.Equal(t, 0, len(vals))
}

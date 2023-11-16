package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/e2e/framework"
)

func TestPreUpgradeTaskClient(t *testing.T) {
	f := framework.NewFramework()
	defer f.Logger().Sync()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := f.Start(ctx)
	require.NoError(t, err)

	preupgradeClient, err := makePreUpgradeTaskClient(f.ClientGen(), f.Logger())
	require.NoError(t, err)

	crd := preupgradeClient.GetFunctionCRD(ctx)
	require.NotNil(t, crd)

	err = preupgradeClient.LatestSchemaApplied(ctx)
	require.NoError(t, err)

	err = preupgradeClient.VerifyFunctionSpecReferences(ctx)
	require.NoError(t, err)
}

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

	crdBackedClient, err := makePreUpgradeTaskClient(f.ClientGen(), f.Logger())
	require.NoError(t, err)

	crd := crdBackedClient.GetFunctionCRD(ctx)
	require.NotNil(t, crd)

	err = crdBackedClient.LatestSchemaApplied(ctx)
	require.NoError(t, err)

	err = crdBackedClient.VerifyFunctionSpecReferences(ctx)
	require.NoError(t, err)
}

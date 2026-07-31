// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"strconv"

	"sigs.k8s.io/controller-runtime/pkg/manager/signals"

	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// crdsModeHook is the CRDS_MODE value (RFC-0029 §1) that makes this hook
// apply the chart-version CRD bundle before the release manifests roll, which
// also delivers RFC-0028's CRDs-before-controllers ordering. Every other mode
// leaves the CRDs to another writer and this binary only checks presence —
// single-writer by design, because two server-side appliers under different
// field managers is an ownership fight nobody wins.
const crdsModeHook = "hook"

func main() {
	logger := loggerfactory.GetLogger()

	ctx := signals.SetupSignalHandler()

	preupgradeClient, err := makePreUpgradeTaskClient(crd.NewClientGenerator(), logger)
	if err != nil {
		logger.Error(err, "error creating a crd client, please retry helm upgrade")
		os.Exit(1)
	}

	// The internal-auth generator is a separate one-shot mode, not part of the
	// upgrade checks: it runs pre-install only, under a ServiceAccount that can
	// create a Secret and nothing else. It returns rather than falling through.
	if len(os.Args) > 1 && os.Args[1] == "generate-internal-auth" {
		namespace, name, perr := internalAuthGenParams()
		if perr != nil {
			logger.Error(perr, "internal-auth generator misconfigured")
			os.Exit(1)
		}
		if gerr := GenerateInternalAuthSecret(ctx, preupgradeClient.k8sClient, logger, namespace, name); gerr != nil {
			logger.Error(gerr, "failed to generate the internal-auth master")
			os.Exit(1)
		}
		return
	}

	if mode := os.Getenv("CRDS_MODE"); mode == crdsModeHook {
		// Adoption of hand-applied CRDs needs --force-conflicts: they carry
		// managedFields owned by kubectl-create as an Update operation.
		// Parsed strictly: this flag decides whether a conflicting writer is
		// silently overridden, so a typo must not fall through to the
		// destructive side.
		force := true
		if v := os.Getenv("CRDS_FORCE_CONFLICTS"); v != "" {
			parsed, perr := strconv.ParseBool(v)
			if perr != nil {
				logger.Error(perr, "CRDS_FORCE_CONFLICTS must be a boolean", "value", v)
				os.Exit(1)
			}
			force = parsed
		}
		if err := preupgradeClient.ApplyCRDs(ctx, force); err != nil {
			logger.Error(err, "failed to apply CRDs; the release's controllers would run against stale schemas")
			os.Exit(1)
		}
	}

	crd := preupgradeClient.GetFunctionCRD(ctx)
	if crd == nil {
		logger.Info("nothing to do since CRDs are not present on the cluster")
		return
	}
	err = preupgradeClient.LatestSchemaApplied(ctx)
	if err != nil {
		logger.Error(err, "New CRDs are not applied")
		os.Exit(1)
	}
	err = preupgradeClient.VerifyFunctionSpecReferences(ctx)
	if err != nil {
		logger.Error(err, "Function spec references are not valid")
		os.Exit(1)
	}
}

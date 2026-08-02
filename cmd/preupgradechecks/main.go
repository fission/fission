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

	// Stamp retention onto chart-generated Secrets BEFORE Helm applies the
	// release manifests and computes its deletion set — a pre-upgrade hook is
	// the only place that ordering holds. Without it, a template that stops
	// rendering a Secret prunes the live one, which for the internal-auth
	// master wedges every control-plane pod (see adoptsecrets.go).
	if names := adoptSecretNames(os.Getenv("ADOPT_SECRETS")); len(names) > 0 {
		// A namespace SET, not just the release namespace — the master is
		// replicated under static tenancy and the copies prune too. See
		// AdoptSecretsForKeep for why missing them fails silently.
		namespaces := adoptSecretNames(os.Getenv("ADOPT_SECRET_NAMESPACES"))
		if len(namespaces) == 0 {
			logger.Error(nil, "ADOPT_SECRETS set but ADOPT_SECRET_NAMESPACES is empty; cannot adopt")
			os.Exit(1)
		}
		if err := AdoptSecretsForKeep(ctx, preupgradeClient.k8sClient, logger, namespaces, names); err != nil {
			logger.Error(err, "failed to annotate secrets for retention; a later upgrade could prune them")
			os.Exit(1)
		}
	}

	// Generate the internal-auth master in-cluster when the chart asks for it
	// (internalAuth.autoGenerate). Generating here rather than in the template
	// is what makes the value survive a GitOps renderer: `lookup` returns empty
	// under `helm template`, so a templated value is re-minted on every sync.
	//
	// GENERATE_AUTH_SECRET_NAMESPACES is computed by the chart, not derived
	// here, because the set is tenancy-dependent — release namespace only under
	// dynamic/cluster tenancy, plus the replicated namespaces under static.
	// See GenerateAuthSecret.
	if name := os.Getenv("GENERATE_AUTH_SECRET"); name != "" {
		namespaces := adoptSecretNames(os.Getenv("GENERATE_AUTH_SECRET_NAMESPACES"))
		if len(namespaces) == 0 {
			logger.Error(nil, "GENERATE_AUTH_SECRET set but GENERATE_AUTH_SECRET_NAMESPACES is empty; refusing to guess which namespaces need the master")
			os.Exit(1)
		}
		if err := GenerateAuthSecret(ctx, preupgradeClient.k8sClient, logger, namespaces, name); err != nil {
			logger.Error(err, "failed to provision the internal-auth master; control-plane pods would start unsigned")
			os.Exit(1)
		}
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

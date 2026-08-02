# SPDX-FileCopyrightText: The Fission Authors
#
# SPDX-License-Identifier: Apache-2.0

# Fission via Terraform, using the Kubernetes provider's generic
# `kubernetes_manifest` resource against Fission's CRDs (RFC-0029 §5).
#
# There is deliberately no Fission Terraform provider. A hand-maintained one
# drifts from the CRDs; a generated one is worth building only if the friction
# below proves real in practice. See README.md for the criteria.
#
# The declarative path is additive: everything here can equally be applied with
# `kubectl apply`, Argo, or Flux. Terraform is one renderer among several, not a
# separate management system.

terraform {
  required_version = ">= 1.5"
  required_providers {
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = ">= 2.30"
    }
  }
}

provider "kubernetes" {
  config_path = var.kubeconfig
}

locals {
  # One place to change the namespace for every object below. Fission requires
  # a Function's Package, Secrets and ConfigMaps to live in the function's own
  # namespace — kubelet cannot resolve a cross-namespace secretKeyRef, and the
  # webhook rejects cross-namespace references outright.
  namespace = var.namespace
}

# ---------------------------------------------------------------------------
# Environment — the language runtime a function executes in.
# ---------------------------------------------------------------------------
resource "kubernetes_manifest" "environment" {
  manifest = {
    apiVersion = "fission.io/v1"
    kind       = "Environment"
    metadata = {
      name      = var.environment_name
      namespace = local.namespace
    }
    spec = {
      # version 2 is the current environment interface; version 1 predates
      # builders and is not accepted for anything in these examples.
      version = 2
      runtime = {
        image = var.runtime_image
      }
      poolsize = var.poolsize
    }
  }
}

# ---------------------------------------------------------------------------
# Package — the code. Pinned by DIGEST, which is what makes this declarative.
# ---------------------------------------------------------------------------
#
# A tag is mutable: re-running Terraform with an unchanged tag whose image has
# been rebuilt produces no diff, so nothing converges and the cluster silently
# runs different code from the one Git describes. A digest changes when the
# bytes change, which is exactly when a rollout should happen.
#
# The intended loop: CI builds and pushes the image, then commits the new
# digest here. Terraform (or Argo/Flux) applies it, and Fission converges on
# the content change — no CLI involved.
resource "kubernetes_manifest" "package" {
  manifest = {
    apiVersion = "fission.io/v1"
    kind       = "Package"
    metadata = {
      name      = var.function_name
      namespace = local.namespace
    }
    spec = {
      environment = {
        name      = var.environment_name
        namespace = local.namespace
      }
      deployment = {
        type = "oci"
        oci = {
          image  = var.function_image
          digest = var.function_image_digest
        }
      }
    }
  }

  depends_on = [kubernetes_manifest.environment]
}

# ---------------------------------------------------------------------------
# Function
# ---------------------------------------------------------------------------
resource "kubernetes_manifest" "function" {
  manifest = {
    apiVersion = "fission.io/v1"
    kind       = "Function"
    metadata = {
      name      = var.function_name
      namespace = local.namespace
    }
    spec = {
      environment = {
        name      = var.environment_name
        namespace = local.namespace
      }
      package = {
        functionName = var.entrypoint
        packageref = {
          name      = var.function_name
          namespace = local.namespace
        }
      }
      # NOTE the capitalisation: these two keys are `InvokeStrategy` and
      # `ExecutionStrategy`, not camelCase. They are serialised that way in the
      # CRD, and a lowercase spelling is silently dropped as an unknown field —
      # leaving the function on executor defaults rather than failing.
      InvokeStrategy = {
        StrategyType = "execution"
        ExecutionStrategy = {
          ExecutorType = var.executor_type
          MinScale     = var.min_scale
          MaxScale     = var.max_scale
        }
      }
    }
  }

  depends_on = [kubernetes_manifest.package]
}

# ---------------------------------------------------------------------------
# HTTPTrigger — the route.
# ---------------------------------------------------------------------------
resource "kubernetes_manifest" "http_trigger" {
  manifest = {
    apiVersion = "fission.io/v1"
    kind       = "HTTPTrigger"
    metadata = {
      name      = "${var.function_name}-route"
      namespace = local.namespace
    }
    spec = {
      prefix  = var.route_prefix
      methods = var.route_methods
      functionref = {
        type            = "name"
        name            = var.function_name
        functionweights = {}
      }
    }
  }

  depends_on = [kubernetes_manifest.function]
}

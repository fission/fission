<!--
SPDX-FileCopyrightText: The Fission Authors

SPDX-License-Identifier: Apache-2.0
-->

# Fission with Terraform

Manage Fission resources as Terraform-managed Kubernetes objects, using the Kubernetes provider's generic `kubernetes_manifest` against Fission's CRDs.

This is one of several declarative renderers, not a separate management system.
The same objects apply identically with `kubectl apply`, Argo CD, or Flux — the CRDs are the interface, and the CLI is a convenience over them rather than the source of truth.

## Why there is no Fission Terraform provider

A hand-maintained provider drifts from the CRDs: every new field needs a second implementation, and the two disagree exactly when it matters.
`kubernetes_manifest` reads the cluster's own OpenAPI schema, so it is always current by construction.

A *generated* provider is a reasonable future step, but only against evidence.
The criteria, so it is a decision rather than a preference:

- users repeatedly hit `kubernetes_manifest`'s requirement that the CRD exist at plan time, in a way documentation does not resolve;
- plan-time diffs on Fission objects prove misleading often enough to cause real incidents;
- or type-safety complaints on these examples arrive from more than a handful of installs.

None of those is currently evidenced.
If one becomes so, the provider is generated from the published schemas — never hand-written.

## Prerequisites

The CRDs must exist **before** `terraform plan`, because `kubernetes_manifest` validates against the live schema at plan time.
Install Fission first (the chart installs CRDs by default — see `crds.mode`), then apply this.

This is the one genuine sharp edge of the `kubernetes_manifest` approach, and it is worth knowing up front rather than discovering it in a broken pipeline.

## Usage

```bash
terraform init
terraform apply \
  -var 'function_image=ghcr.io/example/hello-fn:v1' \
  -var 'function_image_digest=sha256:...'
```

## Pin images by digest

`function_image_digest` is required, and deliberately so.

A tag is mutable.
Re-running Terraform with an unchanged tag whose image has been rebuilt produces **no diff**, so nothing converges and the cluster quietly runs different code from the one Git describes.
A digest changes exactly when the bytes change, which is exactly when a rollout should happen.

The intended loop:

1. CI builds and pushes the function image.
2. CI commits the new digest into your Terraform variables.
3. Terraform applies it; Fission converges on the content change.

No CLI runs anywhere in that loop.

## Everything shares one namespace

Fission requires a Function's Package, Secrets and ConfigMaps to live in the Function's own namespace.
Cross-namespace references are rejected at admission, and kubelet cannot resolve a cross-namespace `secretKeyRef` in any case.
`var.namespace` therefore applies to every object here.

## Field-name gotcha

`InvokeStrategy` and `ExecutionStrategy` are capitalised exactly that way in the CRD — they are not camelCase like their neighbours.

A lowercase spelling is **silently dropped as an unknown field**, leaving the function on executor defaults instead of failing.
If a function ignores your `MinScale`/`MaxScale`, check the capitalisation first.

## Pulumi

Pulumi users generate typed CRD classes from the same schemas with [`crd2pulumi`](https://github.com/pulumi/crd2pulumi):

```bash
crd2pulumi --nodejsPath ./fission-types crds/v1/*.yaml
```

That yields real types over the identical objects, so the modelling above carries across unchanged — only the syntax differs.

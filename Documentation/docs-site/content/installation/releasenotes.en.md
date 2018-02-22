---
title: "Release notes"
draft: false
weight: 22
---

- The fission team is on http://slack.fission.io if you have any questions.

### 0.4.0

- This release is compatible with Kubernetes 1.7 onwards. 
- We switched from ThirdPartyResources to CustomResourceDefinitions. ThirdPartyResources are removed in Kubernetes 1.8, so upgrade with caution, using the upgrade guide below.
- Upgrade guides:
  - [Upgrade guide from 0.3.0](../upgrade/upgrade-from-v0.3)
  - To upgrade from 0.2.1, please upgrade to 0.3.0 first, following the upgrade guide in the 0.3.0 release.

### 0.3.0

Note: This release is incompatible with Kubernetes 1.8 (Because it uses ThirdPartyResources; see #314)

This release introduces:

- Build pipeline. Currently, only the Python environment supports this.
- Workflow engine support (compatible with fission-workflows 0.1.1)

### v0.2.1

Lots of big changes in this release!

- Most importantly, the API has changed a lot. We switched to Kubernetes
ThirdPartyResources, and improved various pieces of the API to support
new environments.

- The old API was too different from widely used Kubernetes patterns,
and so we decided to fully break compatibility for this release. We're still in
alpha, so you should expect the occasional API breakage; we'll be
better at preserving compatibility once we reach beta.

- The CLI is still compatible. Environments are also still compatible --
environment images that worked before continue to work.

- We're creating an upgrade tool to help migrate; if you're upgrading
v0.1.0 and can't do a fresh install, wait for the upgrade tool.

- We now use Helm for installation instead of a set of YAML files.

- The Fission "controller" is now stateless. Fission's etcd deployment
is removed, since Fission stores state in ThirdPartyResources. Large
function files are stored in a new function storage service, which
uses a persistent volume.

- And, we've started a new docs site; for now it's just the installation
and upgrade guides, but we'll be writing more docs soon.
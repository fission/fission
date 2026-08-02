# SPDX-FileCopyrightText: The Fission Authors
#
# SPDX-License-Identifier: Apache-2.0

variable "kubeconfig" {
  description = "Path to the kubeconfig for the target cluster."
  type        = string
  default     = "~/.kube/config"
}

variable "namespace" {
  description = "Namespace for the Environment, Package, Function and HTTPTrigger. They must share one: Fission rejects cross-namespace references."
  type        = string
  default     = "default"
}

variable "environment_name" {
  type    = string
  default = "nodejs"
}

variable "runtime_image" {
  description = "Environment runtime image."
  type        = string
  default     = "ghcr.io/fission/node-env-22"
}

variable "poolsize" {
  description = "Warm pool size for the poolmgr executor. Ignored by newdeploy and container."
  type        = number
  default     = 3
}

variable "function_name" {
  type    = string
  default = "hello"
}

variable "entrypoint" {
  description = "Environment-specific entrypoint, e.g. \"hello.main\" for python or \"\" to accept the runtime default."
  type        = string
  default     = ""
}

variable "function_image" {
  description = "OCI image holding the function's deployment archive."
  type        = string
  default     = "ghcr.io/example/hello-fn:v1"
}

variable "function_image_digest" {
  description = <<-EOT
    Digest of function_image, e.g. "sha256:abc...". Pinning by digest is what
    makes this declarative: a tag is mutable, so a rebuilt image behind an
    unchanged tag produces no Terraform diff and the cluster silently diverges
    from Git. Have CI commit this value when it pushes the image.
  EOT
  type        = string
}

variable "executor_type" {
  description = "poolmgr (warm pool), newdeploy (per-function Deployment), or container (your own image)."
  type        = string
  default     = "poolmgr"

  validation {
    condition     = contains(["poolmgr", "newdeploy", "container"], var.executor_type)
    error_message = "executor_type must be poolmgr, newdeploy or container."
  }
}

variable "min_scale" {
  type    = number
  default = 1
}

variable "max_scale" {
  type    = number
  default = 3
}

variable "route_prefix" {
  type    = string
  default = "/hello"
}

variable "route_methods" {
  type    = list(string)
  default = ["GET"]
}

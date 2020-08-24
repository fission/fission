/*
Copyright 2016 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package crd

import (
	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
)

var (
	functionSchemaProps = map[string]apiextensionsv1beta1.JSONSchemaProps{
		"spec": {
			Type:        "object",
			Description: "Specification of the desired behaviour of the Function",
			Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
				"environment": environmentSchema,
				"package":     packageSchema,
				"secrets":     secretReferenceSchema,
				"configmaps":  configMapReferenceSchema,
				"resources": {
					Type:        "object",
					Description: "ResourceRequirements describes the compute resource requirements. This is only for newdeploy to set up resource limitation when creating deployment for a function.",
				},
				"invokeStrategy": {
					Type:        "object",
					Description: "InvokeStrategy is a set of controls which affect how function executes",
				},
				"functionTimeout": {
					Type:        "integer",
					Description: " FunctionTimeout provides a maximum amount of duration within which a request for a particular function execution should be complete.\nThis is optional. If not specified default value will be taken as 60s",
				},
				"idletimeout": {
					Type:        "object",
					Description: "IdleTimeout specifies the length of time that a function is idle before the function pod(s) are eligible for deletion. If no traffic to the function is detected within the idle timeout, the executor will then recycle the function pod(s) to release resources.",
				},
			},
		},
	}
	functionSchema = apiextensionsv1beta1.JSONSchemaProps{
		Type:        "object",
		Description: "Function in fission is something that Fission executes. It’s usually a module with one entry point, and that entry point is a function with a certain interface.",
		Properties:  functionSchemaProps,
	}
	functionValidation = &apiextensionsv1beta1.CustomResourceValidation{
		OpenAPIV3Schema: &functionSchema,
	}
)

var (
	environmentSchemaProps = map[string]apiextensionsv1beta1.JSONSchemaProps{
		"spec": {
			Type:        "object",
			Description: "Specification of the desired behaviour of the Environment",
			Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
				"version": {
					Type:        "integer",
					Description: "Version is the Environment API version",
				},
				"runtime": {
					Type:        "object",
					Description: "Runtime is configuration for running function, like container image etc.",
				},
				"builder": {
					Type:        "object",
					Description: "Builder is configuration for builder manager to launch environment builder to build source code into deployable binary.",
				},
				"allowedFunctionsPerContainer": {
					Type:        "object",
					Description: "Allowed functions per container. Allowed Values: single, multiple",
				},
				"allowAccessToExternalNetwork": {
					Type:        "boolean",
					Description: "To enable accessibility of external network for builder/function pod, set to 'true'.",
				},
				"resources": {
					Type:        "object",
					Description: "The request and limit CPU/MEM resource setting for poolmanager to set up pods in the pre-warm pool.",
				},
				"poolsize": {
					Type:        "integer",
					Description: "The initial pool size for environment",
				},
				"terminationGracePeriod": {
					Type:        "integer",
					Description: "The grace time for pod to perform connection draining before termination. The unit is in seconds.",
				},
				"keeparchive": {
					Type:        "boolean",
					Description: "KeepArchive is used by fetcher to determine if the extracted archive or unarchived file should be placed, which is then used by specialize handler.",
				},
				"imagepullsecret": {
					Type:        "string",
					Description: "ImagePullSecret is the secret for Kubernetes to pull an image from a private registry.",
				},
			},
		},
	}

	environmentSchema = apiextensionsv1beta1.JSONSchemaProps{
		Type:        "object",
		Description: "Environments are the language-specific parts of Fission. An Environment contains just enough software to build and run a Fission Function.",
		Properties:  environmentSchemaProps,
	}
	environmentValidation = &apiextensionsv1beta1.CustomResourceValidation{
		OpenAPIV3Schema: &environmentSchema,
	}
)

var (
	packageSchemaProps = map[string]apiextensionsv1beta1.JSONSchemaProps{
		"spec": {
			Type:        "object",
			Description: "Specification of the desired behaviour of the package",
			Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
				"environment": environmentSchema,
				"source":      archiveSchema,
				"deployment":  archiveSchema,
				"configmaps":  configMapReferenceSchema,
				"buildcmd": {
					Type:        "string",
					Description: "BuildCommand is a custom build command that builder used to build the source archive.",
				},
			},
		},
	}
	packageSchema = apiextensionsv1beta1.JSONSchemaProps{
		Type:        "object",
		Description: "A Package is a Fission object containing a Deployment Archive and a Source Archive (if any). A Package also references a certain environment.",
		Properties:  packageSchemaProps,
	}

	packageValidation = &apiextensionsv1beta1.CustomResourceValidation{
		OpenAPIV3Schema: &packageSchema,
	}
)

var (
	checksumSchemaProps = map[string]apiextensionsv1beta1.JSONSchemaProps{
		"sum": {
			Type:        "string",
			Description: " Sum is hex encoded chechsum value.",
		},
		"type": {
			Type:        "string",
			Description: "ChecksumType specifies the checksum algorithm, such as sha256, used for a checksum.",
		},
	}
	checksumSchema = apiextensionsv1beta1.JSONSchemaProps{
		Type:        "object",
		Description: "Checksum of package contents when the contents are stored outside the Package struct. Type is the checksum algorithm;  sha256 is the only currently supported one. Sum is hex  encoded.",
		Properties:  checksumSchemaProps,
	}
)

var (
	archiveSchemaProps = map[string]apiextensionsv1beta1.JSONSchemaProps{
		"type": {
			Type:        "string",
			Description: "Type defines how the package is specified: literal or url.",
		},
		"literal": {
			Type:        "string",
			Format:      "byte",
			Description: "Literal contents of the package.",
		},
		"url": {
			Type:        "string",
			Description: "URL references a package.",
		},
		"checksum": checksumSchema,
	}
	archiveSchema = apiextensionsv1beta1.JSONSchemaProps{
		Type:        "object",
		Description: "Package contains or references a collection of source or binary files.",
		Properties:  archiveSchemaProps,
	}
)

var (
	secretReferenceSchemaProps = map[string]apiextensionsv1beta1.JSONSchemaProps{
		"namespace": {
			Type:        "string",
			Description: "Namespace for corresponding secret",
		},
		"name": {
			Type:        "string",
			Description: "Name of the secret to use",
		},
	}
	secretReferenceObjectSchema = apiextensionsv1beta1.JSONSchemaProps{
		Type:        "object",
		Description: "Reference to a Kubernetes secret.",
		Properties:  secretReferenceSchemaProps,
	}
	secretReferenceSchema = apiextensionsv1beta1.JSONSchemaProps{
		Type: "array",
		Items: &apiextensionsv1beta1.JSONSchemaPropsOrArray{
			Schema: &secretReferenceObjectSchema,
		},
	}
)

var (
	configMapReferenceSchemaProps = map[string]apiextensionsv1beta1.JSONSchemaProps{
		"namespace": {
			Type:        "string",
			Description: "Namespace for corresponding ConfigMap",
		},
		"name": {
			Type:        "string",
			Description: "Name of the ConfigMap to use",
		},
	}
	configMapReferenceObjectSchema = apiextensionsv1beta1.JSONSchemaProps{
		Type:        "object",
		Description: "Reference to a Kubernetes ConfigMap.",
		Properties:  configMapReferenceSchemaProps,
	}

	configMapReferenceSchema = apiextensionsv1beta1.JSONSchemaProps{
		Type: "array",
		Items: &apiextensionsv1beta1.JSONSchemaPropsOrArray{
			Schema: &configMapReferenceObjectSchema,
		},
	}
)

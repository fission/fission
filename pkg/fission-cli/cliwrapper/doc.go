/*
Copyright 2019 The Fission Authors.

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

// Package cliwrapper is a wrapper that allowing functions to access flag value in an
// identical way no matter what underlying CLI package used. It brings couple benefits
// for doing this:
// 1. Separate CLI function and CLI package.
// 2. Easier to write test no matter what CLI package actually used.
// 3. Migrate to new CLI package without changing the way for CLI function to access flag value.
package cliwrapper

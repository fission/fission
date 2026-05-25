// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package cliwrapper is a wrapper that allowing functions to access flag value in an
// identical way no matter what underlying CLI package used. It brings couple benefits
// for doing this:
// 1. Separate CLI function and CLI package.
// 2. Easier to write test no matter what CLI package actually used.
// 3. Migrate to new CLI package without changing the way for CLI function to access flag value.
package cliwrapper

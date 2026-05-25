// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package maps

import "maps"

func CopyStringMap(m map[string]string) map[string]string {
	n := make(map[string]string)
	maps.Copy(n, m)
	return n
}

func MergeStringMap(targetMap map[string]string, sourceMap map[string]string) {
	maps.Copy(targetMap, sourceMap)
}

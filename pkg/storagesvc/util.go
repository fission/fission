// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package storagesvc

import (
	"fmt"
	"net/url"
)

func getQueryParamValue(urlString string, queryParam string) (string, error) {
	url, err := url.Parse(urlString)
	if err != nil {
		return "", fmt.Errorf("error parsing URL string %q into URL: %w", urlString, err)
	}
	return url.Query().Get(queryParam), nil
}

func getDifferenceOfLists(firstList []string, secondList []string) []string {
	tempMap := make(map[string]int)
	differenceList := make([]string, 0)

	for _, item := range firstList {
		tempMap[item] = 1
	}

	for _, item := range secondList {
		_, ok := tempMap[item]
		if ok {
			delete(tempMap, item)
		}
	}

	for k := range tempMap {
		differenceList = append(differenceList, k)
	}

	return differenceList
}

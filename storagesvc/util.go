package storagesvc


import (
	"net/url"
	"log"
	"fmt"
)

func utilGetQueryParamValue(urlString string, queryParam string) string {
	url, err := url.Parse(urlString)
	if err != nil {
		log.Printf("Error parsing URL string: %s into URL", urlString)
	}
	return url.Query().Get(queryParam)
}

func utilGetDifferenceOfLists(firstList []string, secondList []string) []string {
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

func utilDumpListContents(list []string, listDescription string) {
	log.Printf("Dumping list %s", listDescription)
	var dump string
	for _, item := range list {
		dump = fmt.Sprintf("%s ", item)
	}
	log.Printf(dump)
}
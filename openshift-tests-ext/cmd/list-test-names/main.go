package main

import (
	"fmt"
	"sort"

	"github.com/openshift/cluster-network-operator/openshift-tests-ext/test"
)

func main() {
	var allTests []string
	allTests = append(allTests, test.InformingTests...)
	allTests = append(allTests, test.BlockingTests...)
	sort.Strings(allTests)

	for _, testName := range allTests {
		fmt.Println(testName)
	}
}

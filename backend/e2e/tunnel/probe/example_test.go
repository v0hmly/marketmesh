package probe_test

import (
	"fmt"

	"github.com/v0hmly/marketmesh/e2e/tunnel/probe"
)

func ExampleNewInstanceDirectory() {
	directory, err := probe.NewInstanceDirectory([]probe.Instance{
		{Source: "fake-a-1", DataCenter: probe.DataCenterA},
		{Source: "fake-b-1", DataCenter: probe.DataCenterB},
	})
	if err != nil {
		panic(err)
	}

	dataCenter, found := directory.Resolve("fake-b-1")
	fmt.Println(dataCenter, found)

	// Output: dc-b true
}

package main

import (
	"flag"
	"fakemachine"
)

func main() {
	flag.Parse()

	m := fakemachine.Machine{}
	m.Run()
}

package main

import (
	"fmt"
	"github.com/jessevdk/go-flags"
	"github.com/sjoerdsimons/fakemachine"
	"os"
	"strings"
)

type Options struct {
	Volumes []string `short:"v" long:"volume" description:"volume to mount"`
}

var options Options
var parser = flags.NewParser(&options, flags.Default)

func SetupVolumes(m *fakemachine.Machine, options Options) {
	for _, v := range options.Volumes {
		parts := strings.Split(v, ":")

		switch len(parts) {
		case 1:
			m.AddVolume(parts[0])
		case 2:
			m.AddVolumeAt(parts[0], parts[1])
		default:
			fmt.Fprintln(os.Stderr, "Failed to parse volume: %s", v)
			os.Exit(1)
		}
	}
}

func main() {
	args, err := parser.Parse()
	if err != nil {
		flagsErr, ok := err.(*flags.Error)
		if ok && flagsErr.Type == flags.ErrHelp {
			os.Exit(0)
		} else {
			os.Exit(1)
		}
	}

	m := fakemachine.NewMachine()
	SetupVolumes(m, options)

	if len(args) > 0 {
		m.Command = strings.Join(args, " ")
	}
	os.Exit(m.Run())
}

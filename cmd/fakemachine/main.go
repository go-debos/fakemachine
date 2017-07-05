package main

import (
	"fmt"
	"github.com/jessevdk/go-flags"
	"github.com/docker/go-units"
	"github.com/sjoerdsimons/fakemachine"
	"os"
	"strings"
)

type Options struct {
	Volumes []string `short:"v" long:"volume" description:"volume to mount"`
	Images []string `short:"i" long:"image" description:"image to add"`
	Memory int `short:"m" long:"memory" description:"Amount of memory for the fakemachine"`
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

func SetupImages(m *fakemachine.Machine, options Options) {
	for _, i := range options.Images {
		parts := strings.Split(i, ":")
		var err error

		switch len(parts) {
		case 1:
			err = m.CreateImage(parts[0], -1)
		case 2:
			var size int64
			size, err = units.FromHumanSize(parts[1])
			if err != nil {
				break
			}
			err = m.CreateImage(parts[0], size)
		default:
			fmt.Fprintf(os.Stderr, "Failed to parse image: %s\n", i)
			os.Exit(1)
		}

		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create image: %s %v\n", i, err)
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
	SetupImages(m, options)

	if options.Memory > 0 {
		m.SetMemory(options.Memory)
	}

	command := "/bin/bash"
	if len(args) > 0 {
		command = strings.Join(args, " ")
	}
	os.Exit(m.Run(command))
}

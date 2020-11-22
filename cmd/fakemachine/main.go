package main

import (
	"fmt"
	"github.com/docker/go-units"
	"github.com/go-debos/fakemachine"
	"github.com/jessevdk/go-flags"
	"os"
	"strings"
)

type Options struct {
	Backend	    string   `short:"b" long:"backend" description:"Virtualisation backend to use" default:"auto"`
	Volumes     []string `short:"v" long:"volume" description:"volume to mount"`
	Images      []string `short:"i" long:"image" description:"image to add"`
	Memory      int      `short:"m" long:"memory" description:"Amount of memory for the fakemachine in megabytes"`
	CPUs        int      `short:"c" long:"cpus" description:"Number of CPUs for the fakemachine"`
	ScratchSize string   `short:"s" long:"scratchsize" description:"On-disk scratch space size (with a unit suffix, e.g. 4G); if unset, memory backed scratch space is used"`
	ShowBoot    bool     `long:"show-boot" description:"Show boot/console messages from the fakemachine"`
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
			fmt.Fprintln(os.Stderr, "Failed to parse volume:", v)
			os.Exit(1)
		}
	}
}

func SetupImages(m *fakemachine.Machine, options Options) {
	for _, i := range options.Images {
		parts := strings.Split(i, ":")
		var err error
		var l string

		switch len(parts) {
		case 1:
			l, err = m.CreateImage(parts[0], -1)
		case 2:
			var size int64
			size, err = units.FromHumanSize(parts[1])
			if err != nil {
				break
			}
			l, err = m.CreateImage(parts[0], size)
		default:
			fmt.Fprintf(os.Stderr, "Failed to parse image: %s\n", i)
			os.Exit(1)
		}

		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create image: %s %v\n", i, err)
			os.Exit(1)
		}

		fmt.Printf("Exposing %s as %s\n", parts[0], l)
	}
}

func main() {
	// append the list of available backends to the commandline argument parser
	opt := parser.FindOptionByLongName("backend")
	opt.Choices = fakemachine.BackendNames()

	args, err := parser.Parse()
	if err != nil {
		flagsErr, ok := err.(*flags.Error)
		if ok && flagsErr.Type == flags.ErrHelp {
			os.Exit(0)
		} else {
			os.Exit(1)
		}
	}

	m, err := fakemachine.NewMachineWithBackend(options.Backend)
	if err != nil {
		fmt.Printf("fakemachine: %v\n", err)
		os.Exit(1)
	}

	m.SetShowBoot(options.ShowBoot)
	SetupVolumes(m, options)
	SetupImages(m, options)

	if options.ScratchSize != "" {
		size, err := units.FromHumanSize(options.ScratchSize)
		if err != nil {
			fmt.Fprintf(os.Stderr, "fakemachine: Couldn't parse scratch size: %v\n", err)
			os.Exit(1)
		}
		m.SetScratch(size, "")
	}

	if options.Memory > 0 {
		m.SetMemory(options.Memory)
	}

	if options.CPUs > 0 {
		m.SetNumCPUs(options.CPUs)
	}

	command := "/bin/bash"
	if len(args) > 0 {
		command = strings.Join(args, " ")
	}

	ret, err := m.Run(command)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fakemachine: %v\n", err)
	}
	os.Exit(ret)
}

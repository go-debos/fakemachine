package main

import (
	"al.essio.dev/pkg/shellescape"
	"errors"
	"fmt"
	"github.com/docker/go-units"
	"github.com/go-debos/fakemachine"
	"github.com/jessevdk/go-flags"
	"os"
	"runtime/debug"
	"strings"
)

var Version string

type Options struct {
	Backend     string            `short:"b" long:"backend" description:"Virtualisation backend to use" default:"auto"`
	Volumes     []string          `short:"v" long:"volume" description:"volume to mount"`
	Images      []string          `short:"i" long:"image" description:"image to add"`
	EnvironVars map[string]string `short:"e" long:"environ-var" description:"Environment variables (use -e VARIABLE:VALUE syntax)"`
	Memory      int               `short:"m" long:"memory" description:"Amount of memory for the fakemachine in megabytes"`
	CPUs        int               `short:"c" long:"cpus" description:"Number of CPUs for the fakemachine"`
	SectorSize  int               `short:"S" long:"sectorsize" description:"Override image sector size"`
	ScratchSize string            `short:"s" long:"scratchsize" description:"On-disk scratch space size (with a unit suffix, e.g. 4G); if unset, memory backed scratch space is used"`
	ShowBoot    bool              `long:"show-boot" description:"Show boot/console messages from the fakemachine"`
	Quiet       bool              `short:"q" long:"quiet" description:"Don't show logs from fakemachine or the backend; only print the command's stdout/stderr"`
	Version     bool              `long:"version" description:"Print fakemachine version"`
}

var options Options
var parser = flags.NewParser(&options, flags.Default)

func GetDeterminedVersion(version string) string {
	DeterminedVersion := "unknown"

	// Use the injected Version from build system if any.
	// Otherwise try to determine the best version string from debug info.
	if len(version) > 0 {
		DeterminedVersion = version
	} else {
		info, ok := debug.ReadBuildInfo()
		if ok {
			// Try vcs version first as it will only be set on a local build
			var revision *string
			var modified *string
			for _, s := range info.Settings {
				if s.Key == "vcs.revision" {
					revision = &s.Value
				}
				if s.Key == "vcs.modified" {
					modified = &s.Value
				}
			}
			if revision != nil {
				DeterminedVersion = *revision
				if modified != nil && *modified == "true" {
					DeterminedVersion += "-dirty"
				}
			} else {
				DeterminedVersion = info.Main.Version
			}
		}
	}

	return DeterminedVersion
}

func warnLocalhost(variable string, value string) {
	message := `WARNING: Environment variable %[1]s contains a reference to
		    localhost. This may not work when running from fakemachine.
		    Consider using an address that is valid on your network.`

	if strings.Contains(value, "localhost") ||
		strings.Contains(value, "127.0.0.1") ||
		strings.Contains(value, "::1") {
		fmt.Printf(message, variable)
	}
}

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

		if !options.Quiet {
			fmt.Printf("Exposing %s as %s\n", parts[0], l)
		}
	}
}

func SetupEnviron(m *fakemachine.Machine, options Options) {
	// Initialize environment variables map
	EnvironVars := make(map[string]string)

	// These are the environment variables that will be detected on the
	// host and propagated to fakemachine. These are listed lower case, but
	// they are detected and configured in both lower case and upper case.
	var environVars = [...]string{
		"http_proxy",
		"https_proxy",
		"ftp_proxy",
		"rsync_proxy",
		"all_proxy",
		"no_proxy",
	}

	// First add variables from host
	for _, e := range environVars {
		lowerVar := strings.ToLower(e) // lowercase not really needed
		lowerVal := os.Getenv(lowerVar)
		if lowerVal != "" {
			EnvironVars[lowerVar] = lowerVal
		}

		upperVar := strings.ToUpper(e)
		upperVal := os.Getenv(upperVar)
		if upperVal != "" {
			EnvironVars[upperVar] = upperVal
		}
	}

	// Then add/overwrite with variables from command line
	for k, v := range options.EnvironVars {
		// Allows the user to unset environ variables with -e
		if v == "" {
			delete(EnvironVars, k)
		} else {
			EnvironVars[k] = v
		}
	}

	// Puts in a format that is compatible with output of os.Environ()
	EnvironString := []string{}
	for k, v := range EnvironVars {
		warnLocalhost(k, v)
		EnvironString = append(EnvironString, fmt.Sprintf("%s=%s", k, v))
	}
	m.SetEnviron(EnvironString) // And save the resulting environ vars on m
}

func main() {
	// append the list of available backends to the commandline argument parser
	opt := parser.FindOptionByLongName("backend")
	opt.Choices = fakemachine.BackendNames()

	args, err := parser.Parse()
	if err != nil {
		var flagsErr *flags.Error
		if errors.As(err, &flagsErr) && flagsErr.Type == flags.ErrHelp {
			os.Exit(0)
		}
		os.Exit(1)
	}

	if options.Version {
		fmt.Printf("fakemachine %v\n", GetDeterminedVersion(Version))
		return
	}

	m, err := fakemachine.NewMachineWithBackend(options.Backend)
	if err != nil {
		fmt.Printf("fakemachine: %v\n", err)
		os.Exit(1)
	}

	m.SetShowBoot(options.ShowBoot)
	m.SetQuiet(options.Quiet)
	SetupVolumes(m, options)
	SetupImages(m, options)
	SetupEnviron(m, options)

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

	if options.SectorSize > 0 {
		m.SetSectorSize(options.SectorSize)
	}

	command := "/bin/bash"
	if len(args) > 0 {
		command = shellescape.QuoteCommand(args)
	}

	ret, err := m.Run(command)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fakemachine: %v\n", err)
	}
	os.Exit(ret)
}

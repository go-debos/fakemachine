# fakemachine - fake a machine

Creates a virtual machine based on the currently running system.

## Synopsis

```
fakemachine [options] <command to run inside machine>
fakemachine [--help]
```

Application Options:
```
  -b, --backend=[auto|kvm|uml|qemu] Virtualisation backend to use (default: auto)
  -v, --volume=                     volume to mount
  -i, --image=                      image to add
  -e, --environ-var=                Environment variables (use -e VARIABLE:VALUE syntax)
  -m, --memory=                     Amount of memory for the fakemachine in megabytes
  -c, --cpus=                       Number of CPUs for the fakemachine
  -s, --scratchsize=                On-disk scratch space size (with a unit suffix, e.g. 4G); if unset,
                                    memory backed scratch space is used
      --show-boot                   Show boot/console messages from the fakemachine

Help Options:
  -h, --help                        Show this help message
```

## Installation

```
$ export GOPATH=~/go
$ export PATH=$PATH:~/go/bin
$ go install github.com/go-debos/fakemachine/cmd/fakemachine@latest
```

## Usage

```
$ fakemachine echo test
Running echo test using kvm backend
test
```

## Contributing

To contribute to fakemachine, see the dedicated [contributing documentation](CONTRIBUTING.md).

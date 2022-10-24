# fakemachine - fake a machine

Creates a vm based on the currently running system.

## Synopsis

  fakemachine [OPTIONS]

```
Application Options:
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

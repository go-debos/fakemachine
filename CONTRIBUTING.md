# Contributing to fakemachine

:+1::tada: Firstly, thank you for taking the time to contribute! :tada::+1:

The following is a set of guidelines for contributing to fakemachine, which is hosted on [GitHub](https://github.com/go-debos/fakemachine).

These are mostly guidelines and not rules. Use your best judgement and feel free to propose changes to this document in a pull request.


## Get in touch!

💬 Join us on Matrix at [#debos:matrix.debian.social](https://matrix.to/#/#debos:matrix.debian.social)
to chat about usage or development of debos.

🪲 To report a bug, issue or feature request, create a new
[GitHub Issue](https://github.com/go-debos/fakemachine/issues).

❓ Please use the [GitHub Discussion forum](https://github.com/orgs/go-debos/discussions)
to ask questions about how to use fakemachine.


## Maintainers

 - [Sjoerd Simons - @sjoerdsimons](https://github.com/sjoerdsimons)
 - [Christopher Obbard - @obbardc](https://github.com/obbardc)
 - [Dylan Aïssi - @daissi](https://github.com/daissi)


## Code of conduct

Be kind, constructive and respectful.  


## Ways to contribute

- **Report bugs** and regressions
- **Improve documentation** (README, man pages, comments)
- **Add tests** or extend existing ones
- **Implement small features** or refactorings that improve maintainability

If you're planning a larger change, please open an issue first so it can be discussed with maintainers before investing a lot of time.


## Reporting bugs

Please create a [GitHub Issue](https://github.com/go-debos/fakemachine/issues) and include:

- fakemachine version (`fakemachine --version` if available, or git commit/tag)
- Host distribution and version (e.g. Debian 12, Ubuntu 24.04)
- Architecture (e.g. `amd64`, `arm64`)
- Backend you're using (e.g `kvm`, `qemu`)
- Steps to reproduce (ideally a minimal command line)
- What you expected to happen
- What actually happened (including **full** error output)

Logs and small reproducer scripts/commands are very welcome.


## Development setup

Prerequisites:

- A recent Go toolchain (matching the `go` version in `go.mod`)
- A POSIX shell and basic build tools
- Optional but recommended: ability to use `/dev/kvm` on your host

Clone your fork:

```sh
git clone https://github.com/<your-username>/fakemachine.git
cd fakemachine
```

Run the tests:

```sh
go test ./...
```

Run the linters:

```sh
docker run --rm -it -v $(pwd):/app -w /app golangci/golangci-lint:latest golangci-lint run
```


## Coding style

fakemachine is written in Go. Please follow the usual Go conventions:

* Format code with `gofmt` (or `go fmt ./...`)
* Keep changes **small and focused** where possible
* Prefer clear, simple code over clever one-liners
* Add or update tests when fixing bugs or adding behaviour

If you touch existing code, try to follow the style of the surrounding code.


## Submitting changes (Pull Requests)

1. Fork the repository and create a feature branch:

```sh
git checkout -b wip/my-username/my-feature
```

2. Make your changes and keep the commit history reasonably clean
   (small, logical commits are easier to review).

3. Ensure tests pass locally:

```sh
go test ./...
```

4. Ensure lint tests pass locally:

```sh
docker run --rm -it -v $(pwd):/app -w /app golangci/golangci-lint:latest golangci-lint run
```

5. Push your branch and open a **Pull Request** against `main`:

   * Use a clear PR title (e.g. `backend_qemu: fix scratch size handling`)
   * Describe *what* you changed and *why*
   * Mention any relevant issue numbers (e.g. `Fixes: #123`)
   * Call out any behaviour changes or backward-incompatible changes

Reviewers may ask for small adjustments - this is a normal part of the process.

Ensure your pull request is always rebased on top the latest fakemachine main branch.


## License and copyright

fakemachine is licensed under the **Apache-2.0** License.
By submitting a pull request, you agree that your contributions will be licensed under the same terms.


Thanks again for helping improve fakemachine!

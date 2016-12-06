goversion is a tool to install and use multiple Go versions.

For example, to use it to try out the first 1.8 beta on your tests:

```bash
$ go get -u github.com/josharian/goversion
$ goversion install 1.8beta1
$ goversion 1.8beta1 test ./...
```

MIT license.

# l15h

[![GoDoc](https://godoc.org/github.com/sb10/l15h?status.svg)](https://godoc.org/github.com/sb10/l15h)
[![Go Report Card](https://goreportcard.com/badge/github.com/sb10/l15h)](https://goreportcard.com/report/github.com/sb10/l15h)
[![Build Status](https://travis-ci.org/sb10/l15h.svg?branch=master)](https://travis-ci.org/sb10/l15h)
[![Coverage Status](https://coveralls.io/repos/github/sb10/l15h/badge.svg?branch=master)](https://coveralls.io/github/sb10/l15h?branch=master)

l15h provides some useful Handlers and logging methods for use with log15:
https://github.com/inconshreveable/log15

log15 is a levelled logger with these levels:

* `Debug("debug msg")`
* `Info("info msg")`
* `Warn("warning msg")`
* `Error("error msg")`
* `Crit("critical msg")`

l15h provides these convenience methods which log at the Crit level:

* `Panic("panic msg")`
* `PanicContext(logger, "panic msg")`
* `Fatal("fatal msg")`
* `FatalContext(logger, "fatal msg")`

log15 log messages can have structure by supplying key/value pairs to the above
methods, eg. `Debug("my msg", "url", "google.com", "retries", 3, "err", err)`

log15 loggers can have and inherit key/value context:

    pkgLog := log15.New("pkg", "mypkg")
    structLog := pkgLog.New("struct", "mystruct", "id", struct.ID)
    structLog.Info("started", "foo", "bar")

Which would give output like:

    INFO[06-17|21:58:10] started     pkg=mypkg struct=mystruct id=1 foo=bar

One of log15's best features is the ability to set composable Handlers that
determine where log output goes and what it looks like. l15h provides these
additional handlers:

* StoreHandler
* CallerInfoHandler
* ChangeableHandler

l15h also provides a convenience method for adding a new handler to a logger's
existing handler(s):

* `AddHandler(logger, newHandler)`

See the GoDoc for usage examples.

## Installation

    go get github.com/sb10/l15h

## Importing

```go
import "github.com/sb10/l15h"
```

## Versioning

The API of the master branch of l15h will probably remain backwards compatible,
but this is not guaranteed. If you want to rely on a stable API, you must vendor
the library.

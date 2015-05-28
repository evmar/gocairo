.PHONY: all cairo example

all: cairo example

cairo: cairo/cairo.go cairo/*.go
	go install github.com/martine/gocairo/cairo

example: cairo example/*/*
	go install github.com/martine/gocairo/example/basic
	go install github.com/martine/gocairo/example/error

cairo.h:
	gcc -E /usr/include/cairo/cairo.h > $@

cairo/cairo.go: $(GOPATH)/bin/gen cairo.h
	$(GOPATH)/bin/gen cairo.h $@

$(GOPATH)/bin/gen: gen/*
	go install github.com/martine/gocairo/gen

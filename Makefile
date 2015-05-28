.PHONY: all cairo example

all: cairo example

cairo: cairo/cairo.go cairo/*.go
	go install github.com/martine/gocairo/cairo

example: cairo example/*/*
	go install github.com/martine/gocairo/example/basic
	go install github.com/martine/gocairo/example/error

cairo.h:
	gcc -E /usr/include/cairo/cairo.h > $@

cairo/cairo.go: gen/gen cairo.h
	gen/gen cairo.h $@

gen/gen: gen/gen.go
	cd gen && go build

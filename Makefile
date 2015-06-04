.PHONY: all cairo example

all: cairo example

cairo: cairo/cairo.go cairo/*.go
	go install github.com/martine/gocairo/cairo

example: cairo example/*
	go run example/basic.go
	go run example/error.go
	go run example/lines.go

cairo-preprocessed.h:
	sed -e 's/<X11\/Xlib\.h>/"fake-xlib.h"/' /usr/include/cairo/cairo-xlib.h | \
	gcc -E `pkg-config --cflags cairo cairo-xlib` - > $@

cairo/cairo.go: gen/gen cairo-preprocessed.h
	gen/gen cairo-preprocessed.h $@

gen/gen: gen/gen.go
	cd gen && go build

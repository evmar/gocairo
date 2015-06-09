// Copyright 2015 Google Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cairo

import (
	"io"
	"reflect"
	"unsafe"
)

import "C"

// sliceBytes returns a pointer to the bytes of the data in a slice.
func sliceBytes(p unsafe.Pointer) unsafe.Pointer {
	hdr := (*reflect.SliceHeader)(p)
	return unsafe.Pointer(hdr.Data)
}

// toError converts a Status into a Go error.
func (s Status) toError() error {
	if s == StatusSuccess {
		return nil
	}
	return s
}

type writeClosure struct {
	w   io.Writer
	err error
}

//export gocairoWriteFunc
func gocairoWriteFunc(closure unsafe.Pointer, data unsafe.Pointer, clength C.uint) bool {
	writeClosure := (*writeClosure)(closure)
	length := uint(clength)
	slice := ((*[1 << 30]byte)(data))[:length:length]
	_, writeClosure.err = writeClosure.w.Write(slice)
	return writeClosure.err == nil
}

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

package main

import (
	"bytes"
	"fmt"
	"go/format"
	"io"
	"log"
	"os"
	"strings"

	"rsc.io/c2go/cc"
)

// intentionalSkip maps C names to the reason why they're left out
// when we intentionally don't generate bindings for them.
var intentionalSkip = map[string]string{
	"cairo_bool_t":           "mapped to bool",
	"cairo_user_data_key_t":  "type only used as a placeholder in C",
	"cairo_matrix_init":      "just the same thing as creating the struct yourself",
	"cairo_status_to_string": "mapped to the error interface, use .Error()",

	"cairo_surface_write_to_png":        "specially implemented to work with io.Writer",
	"cairo_surface_write_to_png_stream": "specially implemented to work with io.Writer",

	"cairo_glyph_allocate": "manage memory on the Go side",
	"cairo_glyph_free":     "manage memory on the Go side",

	"cairo_path_data_t": "used internally in path iteration",

	// These are fake types defined in fake-xlib.h.
	"Drawable": "",
	"Pixmap":   "",
	"Display":  "",
	"Visual":   "",
	"Screen":   "",
}

// skipUnhandled maps C names to the excuse why we haven't wrapped them yet.
var skipUnhandled = map[string]string{
	"cairo_pattern_get_rgba":                   "mix of out params and status",
	"cairo_pattern_get_color_stop_rgba":        "mix of out params and status",
	"cairo_pattern_get_color_stop_count":       "mix of out params and status",
	"cairo_pattern_get_linear_points":          "mix of out params and status",
	"cairo_pattern_get_radial_circles":         "mix of out params and status",
	"cairo_mesh_pattern_get_patch_count":       "mix of out params and status",
	"cairo_mesh_pattern_get_corner_color_rgba": "mix of out params and status",
	"cairo_mesh_pattern_get_control_point":     "mix of out params and status",

	"cairo_scaled_font_text_to_glyphs": "fancy font APIs",
	"cairo_surface_get_mime_data":      "mime functions",
	"cairo_surface_set_mime_data":      "mime functions",
	"cairo_pattern_get_surface":        "need to figure out refcounting",
}

var typeTodoList = map[string]string{
	"cairo_rectangle_int_t":  "hard to wrap API",
	"cairo_rectangle_list_t": "hard to wrap API",

	// Fancy font APIs -- TODO.
	"cairo_text_cluster_t": "needs work",

	// Raster sources -- TODO.
	"cairo_raster_source_acquire_func_t":  "callbacks",
	"cairo_raster_source_snapshot_func_t": "callbacks",
	"cairo_raster_source_copy_func_t":     "callbacks",
	"cairo_raster_source_finish_func_t":   "callbacks",
}

var manualImpl = map[string]string{}

// outParams maps a function name to a per-parameter bool of whether it's
// an output-only param.
var outParams = map[string][]bool{
	"cairo_clip_extents":                  {false, true, true, true, true},
	"cairo_fill_extents":                  {false, true, true, true, true},
	"cairo_path_extents":                  {false, true, true, true, true},
	"cairo_stroke_extents":                {false, true, true, true, true},
	"cairo_recording_surface_ink_extents": {false, true, true, true, true},

	"cairo_get_current_point":               {false, true, true},
	"cairo_surface_get_device_scale":        {false, true, true},
	"cairo_surface_get_device_offset":       {false, true, true},
	"cairo_surface_get_fallback_resolution": {false, true, true},

	// TODO
	// "cairo_pattern_get_rgba":            {false, true, true, true, true},
	// "cairo_pattern_get_color_stop_rgba": {false, false, true, true, true, true, true},
	// "cairo_pattern_get_color_stop_count": {false, true},
}

var arrayParams = map[string]int{
	"cairo_set_dash": 1,

	"cairo_show_glyphs":               1,
	"cairo_glyph_path":                1,
	"cairo_glyph_extents":             1,
	"cairo_scaled_font_glyph_extents": 1,
}

// sharedTypes has the Go type for C types where we just cast a
// pointer across directly.
var sharedTypes = map[string]string{
	"double": "float64",
	// More structs are added as we parse the header.
}

var subTypes = []struct {
	sub, super string
}{
	{"ImageSurface", "Surface"},
	{"RecordingSurface", "Surface"},
	{"SurfaceObserver", "Surface"},
	{"ToyFontFace", "FontFace"},
	{"MeshPattern", "Pattern"},

	{"XlibSurface", "Surface"},
	{"XlibDevice", "Device"},
}

var rawCTypes = map[string]bool{
	"Display":  true,
	"Drawable": true,
	"Visual":   true,
	"Pixmap":   true,
	"Screen":   true,
}

var acronyms = map[string]bool{
	"argb":   true,
	"argb32": true,
	"bgr":    true,
	"cogl":   true,
	"ctm":    true,
	"drm":    true,
	"png":    true,
	"rgb":    true,
	"rgb16":  true,
	"rgb24":  true,
	"rgb30":  true,
	"rgba":   true,
	"vbgr":   true,
	"vrgb":   true,
	"xcb":    true,
	"xml":    true,
	"xor":    true,
}

type Writer struct {
	bytes.Buffer
}

func (w *Writer) Print(format string, a ...interface{}) {
	fmt.Fprintf(w, format+"\n", a...)
}

func (w *Writer) Source() []byte {
	src, err := format.Source(w.Bytes())
	if err != nil {
		log.Printf("gofmt failed: %s", err)
		log.Printf("using unformatted source to enable debugging")
		return w.Bytes()
	}
	return src
}

func cNameToGo(name string, upper bool) string {
	switch name {
	case "int":
		return name
	case "double":
		return "float64"
	case "ulong":
		return "uint32"
	case "uint":
		// This is used in contexts where int is fine.
		return "int"
	case "cairo_t":
		return "Context"
	}

	parts := strings.Split(name, "_")
	out := ""
	for _, p := range parts {
		switch p {
		case "cairo", "t":
			// skip
		default:
			if upper || out != "" {
				if acronyms[p] {
					out += strings.ToUpper(p)
				} else {
					out += strings.Title(p)
				}
			} else {
				out += p
			}
		}
	}
	return out
}

func cNameToGoUpper(name string) string {
	return cNameToGo(name, true)
}

func cNameToGoLower(name string) string {
	return cNameToGo(name, false)
}

type typeMap struct {
	goType string
	cToGo  func(in string) string
	goToC  func(in string) (string, string)
	method string
}

func cTypeToMap(typ *cc.Type) *typeMap {
	switch typ.Kind {
	case cc.Ptr:
		str := typ.Base.String()
		switch str {
		case "char":
			return &typeMap{
				goType: "string",
				cToGo: func(in string) string {
					return fmt.Sprintf("C.GoString(%s)", in)
				},
				goToC: func(in string) (string, string) {
					cvar := fmt.Sprintf("c_%s", in)
					return cvar, fmt.Sprintf("%s := C.CString(%s); defer C.free(unsafe.Pointer(%s))", cvar, in, cvar)
				},
			}
		case "uchar", "void":
			log.Printf("TODO %s: in type blacklist (TODO: add reasoning)", str)
			return nil
		}

		if goType, ok := sharedTypes[str]; ok {
			// TODO: it appears *Rectangle might only be used for out params.
			return &typeMap{
				goType: "*" + goType,
				cToGo: func(in string) string {
					return fmt.Sprintf("(*%s)(unsafe.Pointer(%s))", goType, in)
				},
				goToC: func(in string) (string, string) {
					return fmt.Sprintf("(*C.%s)(unsafe.Pointer(%s))", str, in), ""
				},
				method: goType,
			}
		}

		if rawCTypes[str] {
			return &typeMap{
				goType: "unsafe.Pointer",
				cToGo: func(in string) string {
					return fmt.Sprintf("unsafe.Pointer(%s)", in)
				},
				goToC: func(in string) (string, string) {
					return fmt.Sprintf("(*C.%s)(%s)", str, in), ""
				},
			}
		}

		goName := cNameToGoUpper(str)
		if reason, ok := typeTodoList[str]; ok {
			log.Printf("TODO %s: %s", str, reason)
			return nil
		}
		return &typeMap{
			goType: "*" + goName,
			cToGo: func(in string) string {
				return fmt.Sprintf("wrap%s(%s)", goName, in)
			},
			goToC: func(in string) (string, string) {
				return fmt.Sprintf("%s.Ptr", in), ""
			},
			method: goName,
		}
	case cc.Void:
		return &typeMap{
			goType: "",
			cToGo:  nil,
			goToC:  nil,
		}
	}

	// Otherwise, it's a basic non-pointer type.
	cName := typ.String()
	if reason, ok := typeTodoList[cName]; ok {
		log.Printf("TODO %s: %s", cName, reason)
		return nil
	}

	switch cName {
	case "cairo_bool_t":
		return &typeMap{
			goType: "bool",
			cToGo: func(in string) string {
				return fmt.Sprintf("%s != 0", in)
			},
			goToC: func(in string) (string, string) {
				return fmt.Sprintf("C.%s(%s)", cName, in), ""
			},
		}
	case "cairo_status_t":
		return &typeMap{
			goType: "error",
			cToGo: func(in string) string {
				return fmt.Sprintf("Status(%s).toError()", in)
			},
			goToC: nil,
		}
	case "Drawable", "Pixmap":
		return &typeMap{
			goType: "uint64",
			cToGo: func(in string) string {
				return fmt.Sprintf("uint64(%s)", in)
			},
			goToC: func(in string) (string, string) {
				return fmt.Sprintf("C.%s(%s)", cName, in), ""
			},
		}
	}

	goName := cNameToGoUpper(cName)
	m := &typeMap{
		goType: goName,
		cToGo: func(in string) string {
			return fmt.Sprintf("%s(%s)", goName, in)
		},
		goToC: func(in string) (string, string) {
			return fmt.Sprintf("C.%s(%s)", cName, in), ""
		},
	}
	if goName == "Format" {
		// Attempt to put methods on our "Format" type.
		m.method = goName
	}
	return m
}

func (w *Writer) genTypeDef(d *cc.Decl) {
	w.Print("// See %s.", d.Name)
	goName := cNameToGoUpper(d.Name)

	switch d.Type.Kind {
	case cc.Struct:
		if d.Type.Decls == nil || goName == "Path" {
			// Opaque typedef.
			w.Print(`type %s struct {
Ptr *C.%s
}`, goName, d.Name)
			w.Print("func wrap%s(p *C.%s) *%s {", goName, d.Name, goName)
			w.Print("// TODO: finalizer")
			w.Print("return &%s{p}", goName)
			w.Print("}")
		} else {
			sharedTypes[d.Name] = goName
			w.Print("type %s struct {", goName)
			for _, d := range d.Type.Decls {
				typ := cTypeToMap(d.Type)
				w.Print("%s %s", cNameToGoUpper(d.Name), typ.goType)
			}
			w.Print("}")
		}
	case cc.Enum:
		w.Print("type %s int", goName)
		w.Print("const (")
		for _, d := range d.Type.Decls {
			constName := d.Name
			if strings.HasPrefix(constName, "CAIRO_") {
				constName = constName[len("CAIRO_"):]
			}
			constName = cNameToGoUpper(strings.ToLower(d.Name))
			w.Print("%s %s = C.%s", constName, goName, d.Name)
		}
		w.Print(")")
	default:
		panic("unhandled decl " + d.String())
	}
}

func shouldBeMethod(goName string, goType string) (string, string) {
	if goType == "Context" {
		return goName, ""
	}
	for _, t := range subTypes {
		if strings.HasPrefix(goName, t.sub) && goType == t.super {
			return goName[len(t.sub):], "*" + t.sub
		}
	}
	if goType != "" && strings.HasPrefix(goName, goType) {
		return goName[len(goType):], ""
	}
	return "", ""
}

func (w *Writer) genFunc(f *cc.Decl) bool {
	name := cNameToGoUpper(f.Name)

	retType := cTypeToMap(f.Type.Base)
	if retType == nil {
		return false
	}
	var retTypeSigs []string
	var retVals []string
	if f.Type.Base.Kind == cc.Void {
		retType = nil
	} else {
		goType := retType.goType

		// If the function looks like one that returns a subtype
		// (e.g. ImageSurfaceCreate), adjust the return type code.
		for _, t := range subTypes {
			if retType.goType == "*"+t.super &&
				(strings.HasPrefix(name, t.sub) ||
					(name == "SurfaceCreateObserver" && t.sub == "SurfaceObserver")) {
				goType = "*" + t.sub
				inner := retType
				retType = &typeMap{
					cToGo: func(in string) string {
						return fmt.Sprintf("&%s{%s}", t.sub, inner.cToGo(in))
					},
					method: inner.method,
				}
				break
			}
		}
		retTypeSigs = append(retTypeSigs, goType)
	}

	outs := outParams[f.Name]
	if outs != nil {
		if len(outs) != len(f.Type.Decls) {
			panic("outParams mismatch for " + f.Name)
		}
		if retTypeSigs != nil {
			panic(f.Name + ": outParams and return type")
		}
	}
	arrayParam := -1
	if n, ok := arrayParams[f.Name]; ok {
		arrayParam = n
	}

	var inArgs []string
	var inArgTypes []string
	var callArgs []string
	var getErrorCall string
	var methodSig string
	var preCall string

	for i := 0; i < len(f.Type.Decls); i++ {
		d := f.Type.Decls[i]
		if i == 0 && d.Type.Kind == cc.Void {
			// This is a function that accepts (void).
			continue
		}

		outParam := outs != nil && outs[i]

		argName := cNameToGoLower(d.Name)
		argType := cTypeToMap(d.Type)
		if argType == nil {
			return false
		}

		methName, methType := shouldBeMethod(name, argType.method)
		if i == 0 && methName != "" {
			name = methName
			if name == "Status" {
				name = "status"
			}
			if methType == "" {
				methType = argType.goType
			}
			methodSig = fmt.Sprintf("(%s %s)", argName, methType)
			if name != "status" && methType != "Format" && methType != "*Matrix" {
				getErrorCall = fmt.Sprintf("%s.status()", argName)
			}
		} else if outParam {
			if d.Type.Kind != cc.Ptr {
				panic("non-ptr outparam")
			}
			baseType := cTypeToMap(d.Type.Base)
			argType = &typeMap{
				goType: baseType.goType,
				cToGo: func(in string) string {
					return fmt.Sprintf("%s(%s)", baseType.goType, in)
				},
				goToC: func(in string) (string, string) {
					return "&" + in, ""
				},
			}
			preCall += fmt.Sprintf("var %s C.%s\n", argName, d.Type.Base)
			retTypeSigs = append(retTypeSigs, fmt.Sprintf(argType.goType))
			retVals = append(retVals, argType.cToGo(cNameToGoLower(d.Name)))
		} else if i == arrayParam {
			baseType := cTypeToMap(d.Type.Base)
			inArgs = append(inArgs, argName)
			inArgTypes = append(inArgTypes, "[]"+baseType.goType)
			callArgs = append(callArgs, fmt.Sprintf("(*C.%s)(sliceBytes(unsafe.Pointer(&%s)))", d.Type.Base.String(), argName))
			callArgs = append(callArgs, fmt.Sprintf("C.int(len(%s))", argName))
			i++
			continue
		} else {
			inArgs = append(inArgs, argName)
			inArgTypes = append(inArgTypes, argType.goType)
		}
		if argType.goToC == nil {
			panic("in " + name + " need goToC for " + argName)
		}
		toC, varExtra := argType.goToC(argName)
		callArgs = append(callArgs, toC)
		preCall += varExtra
	}

	argSig := ""
	for i := range inArgs {
		if i > 0 {
			argSig += ", "
		}
		argSig += inArgs[i]
		if i+1 >= len(inArgTypes) || inArgTypes[i] != inArgTypes[i+1] {
			argSig += " " + inArgTypes[i]
		}
	}

	retTypeSig := strings.Join(retTypeSigs, ", ")
	if len(retTypeSigs) > 1 {
		retTypeSig = "(" + retTypeSig + ")"
	}

	w.Print("// See %s().", f.Name)
	w.Print("func %s %s(%s) %s {", methodSig, name, argSig, retTypeSig)
	if preCall != "" {
		w.Print("%s", preCall)
	}
	call := fmt.Sprintf("C.%s(%s)", f.Name, strings.Join(callArgs, ", "))

	if retType != nil {
		w.Print("ret := %s", retType.cToGo(call))
		if getErrorCall == "" && retType.method != "" {
			getErrorCall = "ret.status()"
		}
	} else {
		w.Print("%s", call)
	}

	if getErrorCall != "" {
		w.Print("if err := %s; err != nil { panic(err) }", getErrorCall)
	}

	if retTypeSigs != nil {
		if retVals != nil {
			w.Print("return %s", strings.Join(retVals, ", "))
		} else {
			w.Print("return ret")
		}
	}
	w.Print("}")
	return true
}

func (w *Writer) process(decls []*cc.Decl) {
	w.Print(`// Copyright 2015 Google Inc. All Rights Reserved.
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

// Autogenerated by gen.go, do not edit.

package cairo

import (
	"io"
	"unsafe"
)

/*
#cgo pkg-config: cairo
#include <cairo.h>
#include <cairo-xlib.h>
#include <stdlib.h>

// A cairo_write_func_t for use in cairo_surface_write_to_png.
cairo_status_t gocairo_write_func(void *closure,
                                  const unsigned char *data,
                                  unsigned int length) {
  return gocairoWriteFunc(closure, data, length)
    ? CAIRO_STATUS_SUCCESS
    : CAIRO_STATUS_WRITE_ERROR;
}
*/
import "C"

// Error implements the error interface.
func (s Status) Error() string {
	return C.GoString(C.cairo_status_to_string(C.cairo_status_t(s)))
}

// WriteToPNG encodes a Surface to an io.Writer as a PNG file.
func (surface *Surface) WriteToPNG(w io.Writer) error {
	data := writeClosure{w: w}
	status := C.cairo_surface_write_to_png_stream((*C.cairo_surface_t)(surface.Ptr),
		(C.cairo_write_func_t)(unsafe.Pointer(C.gocairo_write_func)),
		unsafe.Pointer(&data))
    // TODO: which should we prefer between writeClosure.err and status?
    // Perhaps test against CAIRO_STATUS_WRITE_ERROR?  Needs a test case.
	return Status(status).toError()
}

// PathIter creates an iterator over the segments within the path.
func (p *Path) Iter() *PathIter {
	return &PathIter{path:p, i:0}
}

// PathIter iterates a Path.
type PathIter struct {
	path *Path
	i    C.int
}

// Next returns the next PathSegment, or returns nil at the end of the path.
func (pi *PathIter) Next() *PathSegment {
	if pi.i >= pi.path.Ptr.num_data {
		return nil
	}
	// path.data is an array of cairo_path_data_t, but the union makes
	// things complicated.
	dataArray := (*[1<<30]C.cairo_path_data_t)(unsafe.Pointer(pi.path.Ptr.data))
	seg, ofs := decodePathSegment(unsafe.Pointer(&dataArray[pi.i]))
	pi.i += C.int(ofs)
	return seg
}
`)
	for _, t := range subTypes {
		w.Print(`type %s struct {
*%s
}`, t.sub, t.super)
	}

	intentionalSkips := 0
	todoSkips := 0
	for _, d := range decls {
		if reason, ok := intentionalSkip[d.Name]; ok {
			if reason != "" {
				log.Printf("skipped %s: %s", d.Name, reason)
			}
			intentionalSkips++
			continue
		}
		if reason, ok := typeTodoList[d.Name]; ok {
			log.Printf("TODO %s: %s", d.Name, reason)
			todoSkips++
			continue
		}
		if reason, ok := skipUnhandled[d.Name]; ok {
			log.Printf("TODO %s: %s", d.Name, reason)
			todoSkips++
			continue
		}

		if strings.HasSuffix(d.Name, "_func") ||
			strings.HasSuffix(d.Name, "_func_t") ||
			strings.HasSuffix(d.Name, "_callback") ||
			strings.HasSuffix(d.Name, "_callback_data") ||
			strings.HasSuffix(d.Name, "_callback_t") {
			log.Printf("TODO %s: callbacks back into Go", d.Name)
			todoSkips++
			continue
		}
		if strings.HasSuffix(d.Name, "_user_data") {
			log.Printf("skipped %s: closures mean you don't need user data(?)", d.Name)
			intentionalSkips++
			continue
		}
		if strings.HasSuffix(d.Name, "_reference") ||
			strings.HasSuffix(d.Name, "_destroy") ||
			strings.HasSuffix(d.Name, "_get_reference_count") {
			log.Printf("skipped %s: Go uses GC instead of refcounting", d.Name)
			intentionalSkips++
			continue
		}
		if d.Name == "" {
			log.Printf("skipped %s: anonymous type", d)
			intentionalSkips++
			continue
		}

		if impl, ok := manualImpl[d.Name]; ok {
			w.Print("// See %s().", d.Name)
			w.Print("%s", impl)
		} else if d.Storage == cc.Typedef {
			w.genTypeDef(d)
		} else if d.Type.Kind == cc.Func {
			if !w.genFunc(d) {
				intentionalSkips++
			}
		} else {
			log.Printf("unhandled decl: %#v", d)
			log.Printf("type %s %#v", d.Type, d.Type)
			log.Printf("type kind %s", d.Type.Kind)
			log.Printf("storage %s", d.Storage)
		}
		w.Print("")
	}
	log.Printf("%d decls total, %d skipped intentionally / %d TODO", len(decls), intentionalSkips, todoSkips)
}

func main() {
	if len(os.Args) < 3 {
		log.Printf("need two paths")
		os.Exit(1)
	}
	inpath := os.Args[1]
	outpath := os.Args[2]

	f, err := os.Open(inpath)
	if err != nil {
		log.Printf("open %q: %s", inpath, err)
		os.Exit(1)
	}

	prog, err := cc.Read(inpath, f)
	if err != nil {
		log.Printf("read %q: %s", inpath, err)
		os.Exit(1)
	}

	w := &Writer{}
	w.process(prog.Decls)

	var outf io.Writer
	if outpath == "-" {
		outf = os.Stdout
		outpath = "<stdout>"
	} else {
		outf, err = os.Create(outpath)
		if err != nil {
			log.Printf("open %q: %s", outpath, err)
			os.Exit(1)
		}
	}

	_, err = outf.Write(w.Source())
	if err != nil {
		log.Printf("write %q: %s", outpath, err)
		os.Exit(1)
	}
}

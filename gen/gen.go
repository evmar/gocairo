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

// intentionalSkip maps C names to true when we intentionally don't generate
// bindings for them.  See the comments for reasoning.
var intentionalSkip = map[string]bool{
	// Type only used as a placeholder in C.
	"cairo_user_data_key_t": true,

	// Just the same thing as creating the struct yourself.
	"cairo_matrix_init": true,

	// Fancy clip rectangle business -- TODO.
	"cairo_copy_clip_rectangle_list": true,

	// Fancy font APIs -- TODO.
	"cairo_glyph_allocate":             true,
	"cairo_glyph_free":                 true,
	"cairo_glyph_path":                 true,
	"cairo_glyph_extents":              true,
	"cairo_show_glyphs":                true,
	"cairo_show_text_glyphs":           true,
	"cairo_text_cluster_allocate":      true,
	"cairo_text_cluster_free":          true,
	"cairo_scaled_font_glyph_extents":  true,
	"cairo_scaled_font_text_to_glyphs": true,

	// Data structure with a hard-to-wrap API -- TODO.
	"cairo_path_t":      true,
	"cairo_path_data_t": true,

	"cairo_rectangle_int_t":  true,
	"cairo_rectangle_list_t": true,

	// Raster sources -- TODO.
	"cairo_raster_source_acquire_func_t":  true,
	"cairo_raster_source_snapshot_func_t": true,
	"cairo_raster_source_copy_func_t":     true,
	"cairo_raster_source_finish_func_t":   true,

	// Mime functions -- TODO.
	"cairo_surface_get_mime_data": true,
	"cairo_surface_set_mime_data": true,

	// Need to figure out refcounting -- TODO.
	"cairo_pattern_get_surface": true,

	// Use status.String() instead.
	"cairo_status_to_string": true,
}

var manualImpl = map[string]string{
	"cairo_set_dash": `func (cr *Context) SetDash(dashes []float64, offset float64) {
C.cairo_set_dash(cr.Ptr, (*C.double)(sliceBytes(unsafe.Pointer(&dashes))), C.int(len(dashes)), C.double(offset))
}`,
}

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
	{"ToyFontFace", "FontFace"},
	{"MeshPattern", "Pattern"},
}

type Writer struct {
	bytes.Buffer
}

func (w *Writer) Print(format string, a ...interface{}) {
	fmt.Fprintf(w, format+"\n", a...)
}

func (w *Writer) Source() ([]byte, error) {
	return format.Source(w.Bytes())
}

func cNameToGo(name string) string {
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
	for i, p := range parts {
		if p == "cairo" || p == "t" {
			p = ""
		}
		parts[i] = strings.Title(p)
	}
	return strings.Join(parts, "")
}

func cNameToGoPrivate(name string) string {
	name = cNameToGo(name)
	return strings.ToLower(name[:1]) + name[1:]
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
		goName := cNameToGo(str)
		if intentionalSkip[str] {
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
	default:
		cName := typ.String()
		goName := cNameToGo(cName)
		if intentionalSkip[cName] {
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
		}

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
}

func (w *Writer) genTypeDef(d *cc.Decl) {
	switch d.Name {
	case "cairo_bool_t":
		return
	}

	w.Print("// See %s.", d.Name)
	goName := cNameToGo(d.Name)

	switch d.Type.Kind {
	case cc.Struct:
		if d.Type.Decls == nil {
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
				w.Print("%s %s", cNameToGo(d.Name), typ.goType)
			}
			w.Print("}")
		}
	case cc.Enum:
		w.Print("type %s int", goName)
		w.Print("const (")
		for i, d := range d.Type.Decls {
			constName := d.Name
			if strings.HasPrefix(constName, "CAIRO_") {
				constName = constName[len("CAIRO_"):]
			}
			constName = cNameToGo(strings.ToLower(d.Name))
			if i == 0 {
				w.Print("%s %s = C.%s", constName, goName, d.Name)
			} else {
				w.Print("%s = C.%s", constName, d.Name)
			}
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
	if goType != "" && strings.HasPrefix(goName, goType) {
		return goName[len(goType):], ""
	}
	for _, t := range subTypes {
		if strings.HasPrefix(goName, t.sub) && goType == t.super {
			return goName[len(t.sub):], "*" + t.sub
		}
	}
	return "", ""
}

func (w *Writer) genFunc(f *cc.Decl) bool {
	name := cNameToGo(f.Name)

	retType := cTypeToMap(f.Type.Base)
	if retType == nil {
		log.Printf("skipped %s due to %s", f.Name, f.Type.Base)
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
			if strings.HasPrefix(name, t.sub) && retType.goType == "*"+t.super {
				goType = "*" + t.sub
				inner := retType.cToGo
				retType = &typeMap{
					cToGo: func(in string) string {
						return fmt.Sprintf("&%s{%s}", t.sub, inner(in))
					},
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

	var inArgs []string
	var callArgs []string
	var getErrorCall string
	var methodSig string
	var preCall string

	for i, d := range f.Type.Decls {
		if i == 0 && d.Type.Kind == cc.Void {
			// This is a function that accepts (void).
			continue
		}

		outParam := outs != nil && outs[i]

		argName := cNameToGoPrivate(d.Name)
		argType := cTypeToMap(d.Type)
		if argType == nil {
			log.Printf("skipped %s due to %s", f.Name, d.Type)
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
			retVals = append(retVals, argType.cToGo(cNameToGoPrivate(d.Name)))
		} else {
			inArgs = append(inArgs, fmt.Sprintf("%s %s", argName, argType.goType))
		}
		if argType.goToC == nil {
			panic("in " + name + " need goToC for " + argName)
		}
		toC, varExtra := argType.goToC(argName)
		callArgs = append(callArgs, toC)
		preCall += varExtra
	}

	retTypeSig := strings.Join(retTypeSigs, ", ")
	if len(retTypeSigs) > 1 {
		retTypeSig = "(" + retTypeSig + ")"
	}

	w.Print("// See %s().", f.Name)
	w.Print("func %s %s(%s) %s {", methodSig, name, strings.Join(inArgs, ", "), retTypeSig)
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
	skipUnhandled := map[string]string{
		"cairo_pattern_get_rgba":                   "mix of out params and status",
		"cairo_pattern_get_color_stop_rgba":        "mix of out params and status",
		"cairo_pattern_get_color_stop_count":       "mix of out params and status",
		"cairo_pattern_get_linear_points":          "mix of out params and status",
		"cairo_pattern_get_radial_circles":         "mix of out params and status",
		"cairo_mesh_pattern_get_patch_count":       "mix of out params and status",
		"cairo_mesh_pattern_get_corner_color_rgba": "mix of out params and status",
		"cairo_mesh_pattern_get_control_point":     "mix of out params and status",
	}
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

import "unsafe"

/*
#cgo pkg-config: cairo
#include <cairo.h>
#include <stdlib.h>
*/
import "C"

// Error implements the Error interface.
func (s Status) Error() string {
	return C.GoString(C.cairo_status_to_string(C.cairo_status_t(s)))
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
		if intentionalSkip[d.Name] {
			intentionalSkips++
			continue
		}
		if strings.HasSuffix(d.Name, "_func") ||
			strings.HasSuffix(d.Name, "_func_t") ||
			strings.HasSuffix(d.Name, "_callback") ||
			strings.HasSuffix(d.Name, "_callback_data") ||
			strings.HasSuffix(d.Name, "_callback_t") {
			// We don't expose callbacks back into Go.
			intentionalSkips++
			continue
		}
		if strings.HasSuffix(d.Name, "_user_data") {
			// Go datatypes like closures make user data less useful.
			intentionalSkips++
			continue
		}
		if strings.HasSuffix(d.Name, "_reference") ||
			strings.HasSuffix(d.Name, "_destroy") ||
			strings.HasSuffix(d.Name, "_get_reference_count") {
			// We don't need refcounting when we have GC.
			intentionalSkips++
			continue
		}
		if d.Name == "" {
			log.Printf("skipping %s (anonymous)", d)
			intentionalSkips++
			continue
		}

		if reason, ok := skipUnhandled[d.Name]; ok {
			log.Printf("TODO: %s unhandled -- %s", d.Name, reason)
			todoSkips++
			continue
		}

		if impl, ok := manualImpl[d.Name]; ok {
			w.Print("// %s", d.Name) // TODO: docstring
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
	log.Printf("%d decls total, %d skipped intentionally", len(decls), intentionalSkips)
}

func main() {
	if len(os.Args) < 3 {
		log.Printf("need two paths\n")
		return
	}
	inpath := os.Args[1]
	outpath := os.Args[2]

	f, err := os.Open(inpath)
	if err != nil {
		panic(err)
	}

	prog, err := cc.Read(inpath, f)
	if err != nil {
		panic(err)
	}

	w := &Writer{}
	w.process(prog.Decls)

	var outf io.Writer
	if outpath == "-" {
		outf = os.Stdout
	} else {
		outf, err = os.Create(outpath)
		if err != nil {
			panic(err)
		}
	}

	src, err := w.Source()
	if err != nil {
		log.Printf("gofmt failed: %s", err)
		log.Printf("writing unformatted source for debugging")
		src = w.Bytes()
	}
	_, err = outf.Write(src)
	if err != nil {
		panic(err)
	}
}

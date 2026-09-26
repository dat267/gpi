package interactive

import (
	"go/ast"
	"reflect"
	"testing"
)

// TestAppExportedSurface pins App's cross-package surface. cmd is the only
// consumer outside this package, and it calls Run and StopMode on the app it
// built with NewApp, plus Init and Close. Everything else App offers is
// in-package state: the wirings and the interactive tests are all in this
// package, so an exported member buys nothing and misleads a reader into
// treating it as API.
//
// This file exists because app.go is the most frequently changed file in the
// repository — and its surface was the largest. The guard is the point: the
// surface cannot quietly grow back one field at a time.
func TestAppExportedSurface(t *testing.T) {
	allowed := map[string]bool{"Init": true, "Run": true, "Close": true, "StopMode": true}

	// The pointer type for methods: every App method has a pointer receiver, and
	// the value type's method set is empty here — a guard that cannot fail.
	methodSet := reflect.TypeOf((*App)(nil))
	for i := 0; i < methodSet.NumMethod(); i++ {
		if name := methodSet.Method(i).Name; !allowed[name] {
			t.Errorf("App.%s is exported; the cross-package surface is exactly Init, Run, Close and StopMode", name)
		}
	}
	appType := reflect.TypeOf(App{})
	for i := 0; i < appType.NumField(); i++ {
		field := appType.Field(i)
		// An embedded type's name is its type's, not App's own surface.
		if !field.Anonymous && ast.IsExported(field.Name) {
			t.Errorf("App.%s is exported; App's fields are in-package state", field.Name)
		}
	}
}

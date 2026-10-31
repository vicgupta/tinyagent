package tools

import (
	"bytes"
	"reflect"
	"testing"
)

// fillNonZero sets every exported field of e that it can to a non-zero
// value, so a test can see which fields a child copies.
func fillNonZero(t *testing.T, e *Executor) {
	t.Helper()
	v := reflect.ValueOf(e).Elem()
	for i := 0; i < v.NumField(); i++ {
		f := v.Type().Field(i)
		if !f.IsExported() {
			continue
		}
		fv := v.Field(i)
		switch fv.Kind() {
		case reflect.String:
			fv.SetString("x-" + f.Name)
		case reflect.Bool:
			fv.SetBool(true)
		case reflect.Int, reflect.Int64:
			fv.SetInt(int64(i + 1))
		case reflect.Pointer:
			fv.Set(reflect.New(fv.Type().Elem()))
		case reflect.Slice:
			fv.Set(reflect.MakeSlice(fv.Type(), 1, 1))
		case reflect.Func:
			fv.Set(reflect.MakeFunc(fv.Type(), func([]reflect.Value) []reflect.Value { return nil }))
		case reflect.Interface:
			for _, c := range []any{Local{}, &bytes.Buffer{}} {
				if reflect.TypeOf(c).Implements(fv.Type()) {
					fv.Set(reflect.ValueOf(c))
					break
				}
			}
		}
		if fv.IsZero() {
			t.Fatalf("fillNonZero: cannot set Executor.%s (%s); teach the test this kind", f.Name, fv.Kind())
		}
	}
}

func sameValue(a, b reflect.Value) bool {
	if a.Kind() == reflect.Func {
		return a.Pointer() == b.Pointer()
	}
	return reflect.DeepEqual(a.Interface(), b.Interface())
}

func TestChildPolicyNamesRealFields(t *testing.T) {
	typ := reflect.TypeOf(Executor{})
	for _, set := range []map[string]bool{childOverridden, childReset} {
		for name := range set {
			if f, ok := typ.FieldByName(name); !ok || !f.IsExported() {
				t.Errorf("child policy names %q, which is not an exported Executor field", name)
			}
			if childOverridden[name] && childReset[name] {
				t.Errorf("%q is both overridden and reset", name)
			}
		}
	}
}

// TestChildInheritsByDefault fills every exported field of a parent and
// checks that each field not on the overridden or reset lists reaches the
// child. A newly added Executor field is covered without editing any
// child call site.
func TestChildInheritsByDefault(t *testing.T) {
	parent := &Executor{}
	fillNonZero(t, parent)
	child := parent.Child(ChildSpec{})
	pv, cv := reflect.ValueOf(parent).Elem(), reflect.ValueOf(child).Elem()
	for i := 0; i < pv.NumField(); i++ {
		f := pv.Type().Field(i)
		if !f.IsExported() {
			continue
		}
		switch {
		case childReset[f.Name]:
			if !cv.Field(i).IsZero() {
				t.Errorf("reset field %s was copied to the child", f.Name)
			}
		case f.Name == "Only":
			if child.Only != nil {
				t.Errorf("Only was copied to the child; it is a filter set only from ChildSpec")
			}
		case !sameValue(pv.Field(i), cv.Field(i)):
			t.Errorf("field %s was not inherited by the child", f.Name)
		}
	}
	if child == parent {
		t.Fatal("Child returned the parent")
	}
}

func TestChildOverrides(t *testing.T) {
	parentFA := &FileAccess{}
	parent := &Executor{Root: "/p", OutputFiles: &OutputFiles{}, FileAccess: parentFA, Env: Local{}, Only: []string{"bash"}}

	same := parent.Child(ChildSpec{ReadOnly: true})
	if same.Root != "/p" || !same.ReadOnly || same.OutputFiles != parent.OutputFiles || same.FileAccess != parentFA || same.Only != nil {
		t.Errorf("same-root child: %+v", same)
	}
	if parent.ReadOnly {
		t.Error("Child changed the parent")
	}

	own := &FileAccess{}
	moved := parent.Child(ChildSpec{Root: "/w", FileAccess: own, Only: []string{"read_file"}})
	if moved.Root != "/w" || moved.ReadOnly || moved.OutputFiles != nil || moved.FileAccess != own || !reflect.DeepEqual(moved.Only, []string{"read_file"}) {
		t.Errorf("moved child: %+v", moved)
	}
	if m := parent.Child(ChildSpec{Root: "/w"}); m.FileAccess != nil {
		t.Error("a moved child reused the parent's file_access rules")
	}

	ro := &Executor{Root: "/p", ReadOnly: true}
	if !ro.Child(ChildSpec{}).ReadOnly {
		t.Error("a child of a read-only parent gained write access")
	}
}

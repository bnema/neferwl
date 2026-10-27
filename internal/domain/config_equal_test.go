package domain

import (
	"image"
	"reflect"
	"regexp"
	"testing"
)

// Walk every configuration leaf, including elements of non-comparable fields.
// A new collection or scalar must affect equality without updating this test.
func TestConfigEqualCoversFields(t *testing.T) {
	var walk func(reflect.Type, []int, string)
	walk = func(typ reflect.Type, path []int, name string) {
		if typ.Kind() == reflect.Struct {
			for i := 0; i < typ.NumField(); i++ {
				walk(typ.Field(i).Type, append(append([]int(nil), path...), i), name+"."+typ.Field(i).Name)
			}
			return
		}
		t.Run(name, func(t *testing.T) {
			var a, b Config
			field := reflect.ValueOf(&b).Elem().FieldByIndex(path)
			populateConfigField(t, field)
			if a.Equal(b) || b.Equal(a) {
				t.Fatal("changed field ignored")
			}
			if !b.Equal(b) {
				t.Fatal("equal config differs")
			}
		})
	}
	walk(reflect.TypeFor[Config](), nil, "Config")
}

func populateConfigField(t *testing.T, v reflect.Value) {
	t.Helper()
	switch v.Kind() {
	case reflect.String:
		v.SetString("changed")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1)
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		populateConfigField(t, v.Index(0))
	case reflect.Array:
		populateConfigField(t, v.Index(0))
	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))
		key, value := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
		populateConfigField(t, key)
		populateConfigField(t, value)
		v.SetMapIndex(key, value)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			populateConfigField(t, v.Field(i))
		}
	case reflect.Pointer:
		if v.Type() == reflect.TypeFor[*regexp.Regexp]() {
			v.Set(reflect.ValueOf(regexp.MustCompile("changed")))
			return
		}
		v.Set(reflect.New(v.Type().Elem()))
		populateConfigField(t, v.Elem())
	default:
		t.Fatalf("unhandled config field %s", v.Type())
	}
}

func TestConfigEqualNestedValues(t *testing.T) {
	a := Config{Workspaces: []WorkspaceConfig{{Slots: []SlotConfig{{Argv: []string{"terminal"}}}}}}
	b := Config{Workspaces: []WorkspaceConfig{{Slots: []SlotConfig{{Argv: []string{"other"}}}}}}
	if a.Equal(b) {
		t.Fatal("slot argv ignored")
	}
	p, q := image.Pt(2, 3), image.Pt(2, 3)
	a, b = Config{Outputs: []OutputConfig{{Pos: &p}}}, Config{Outputs: []OutputConfig{{Pos: &q}}}
	if !a.Equal(b) {
		t.Fatal("positions compared by address")
	}
	q.X++
	if a.Equal(b) {
		t.Fatal("position value ignored")
	}
	yes, otherYes := true, true
	a, b = Config{Rules: []WindowRule{{AppID: regexp.MustCompile("app"), Floating: &yes}}}, Config{Rules: []WindowRule{{AppID: regexp.MustCompile("app"), Floating: &otherYes}}}
	if !a.Equal(b) {
		t.Fatal("rules compared by address")
	}
	b.Rules[0].AppID = regexp.MustCompile("other")
	if a.Equal(b) {
		t.Fatal("rule regexp ignored")
	}
	var empty Config
	populated := Config{Binds: map[string]string{}, Startup: [][]string{}, Outputs: []OutputConfig{}}
	if !empty.Equal(populated) {
		t.Fatal("nil and empty collections differ")
	}
}

package rt

import (
	"reflect"
	"sync"
)

// A canonical field plan contains TYPE METADATA ONLY. It never stores a
// reflect.Value, model, supplied tuple args, converted result, eligibility or
// enum/owner metadata. All fields are immutable after publication.
type codecAutoFieldPlanKey struct {
	type_ reflect.Type
	snake bool
}

type codecAutoField struct {
	index    int
	key      string
	declared string
}

type codecAutoFieldPlan struct{ fields []codecAutoField }

var codecAutoFieldPlans sync.Map // codecAutoFieldPlanKey -> *codecAutoFieldPlan

func codecAutoFields(t reflect.Type, snake bool) *codecAutoFieldPlan {
	key := codecAutoFieldPlanKey{type_: t, snake: snake}
	if cached, ok := codecAutoFieldPlans.Load(key); ok {
		return cached.(*codecAutoFieldPlan)
	}
	plan := &codecAutoFieldPlan{}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" {
			continue
		}
		name := skyColKey(field, snake)
		plan.fields = append(plan.fields, codecAutoField{
			index: i, key: name, declared: skyTagType(field),
		})
	}
	actual, loaded := codecAutoFieldPlans.LoadOrStore(key, plan)
	if !loaded {
		// Only the winning published immutable TYPE plan contributes names.
		// Concurrent readers before registration simply use canonical misses.
		for _, field := range plan.fields {
			codecRegisterSchemaKey(field.key)
		}
	}
	return actual.(*codecAutoFieldPlan)
}

// Each invocation owns its mutable ordered-object slices. In particular, a
// caller cannot mutate a returned object's keys and corrupt the shared plan.
// Preserve the reference empty object's nil slices.
func (p *codecAutoFieldPlan) newObject() jsonOrderedObject {
	if len(p.fields) == 0 {
		return jsonOrderedObject{}
	}
	return jsonOrderedObject{keys: make([]string, len(p.fields)), vals: make([]any, len(p.fields))}
}

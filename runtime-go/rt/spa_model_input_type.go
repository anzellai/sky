package rt

import "reflect"

// SpaModelInputType is compiler-owned transport for the actual Full parameter
// ABI. Returning any keeps the compiler IR honest without a reflect import in
// generated applications. The token carries no model/purity/codec certificate.
func SpaModelInputType[T any]() any { return reflect.TypeOf((*T)(nil)).Elem() }

// Match exactly the successful v.(T) subset of original Coerce[T], before its
// structural bridges can normalize records, containers or aliases. A concrete
// named/unnamed AssignableTo pair is insufficient: its assertion can fail.
func spaEncoderModelMatches(inputType, model any) bool {
	target, ok := inputType.(reflect.Type)
	if !ok || target == nil || model == nil {
		return false
	}
	actual := reflect.TypeOf(model)
	return actual == target || (target.Kind() == reflect.Interface && actual.AssignableTo(target))
}

// HIR literal provenance and runtime closed-domain eligibility are independent.
// A literal payload-ADT blank can pass the former and must use mixed execution.
func spaClosedEncoderModelsEligible(generated bool, inputType, prev, next any) bool {
	return generated && spaEncoderModelMatches(inputType, prev) && spaEncoderModelMatches(inputType, next) && spaModelEncodingEligible(prev) && spaModelEncodingEligible(next)
}

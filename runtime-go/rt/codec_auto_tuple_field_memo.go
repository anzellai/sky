package rt

import "reflect"

// At most one immutable schema plan, local to an active list invocation.
// No values, args, encodings, callbacks, eligibility or registry epochs enter it.
type codecTupleFieldMemo struct {
	type_ reflect.Type
	snake bool
	plan  *codecAutoFieldPlan
}

func codecAutoTupleFieldsMemo(t reflect.Type, snake bool, memo *codecTupleFieldMemo) *codecAutoFieldPlan {
	if memo != nil && memo.plan != nil && memo.type_ == t && memo.snake == snake {
		return memo.plan
	}
	// Filled lazily at the original TupleAt schema-lookup position, after all
	// canonical dispatch/owner guards. Never preclassify slice element values.
	plan := codecAutoFields(t, snake)
	if memo != nil {
		memo.type_, memo.snake, memo.plan = t, snake, plan
	}
	return plan
}

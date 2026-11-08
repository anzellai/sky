package reusedbuffers

// Reusing private storage between calls is ordinary Go code. No function
// mutates an argument supplied by Sky; the ownership boundary must snapshot
// convertible return data before this package's next call.
var shared = []int{0}
var nested = [][]int{shared}

type Holder struct {
	Values  [][]int
	private int
}

var holder = &Holder{Values: nested}

type NamedList []int

func Read(n int) []int         { shared[0] = n; return shared }
func ReadNested(n int) [][]int { shared[0] = n; return nested }
func GetHolder(n int) *Holder  { shared[0] = n; return holder }
func Named(n int) NamedList    { shared[0] = n; return NamedList(shared) }

func Feed(callback func([]int) int) int {
	shared[0] = 1
	callback(shared)
	shared[0] = 2
	return callback(shared)
}

var originalFunction = func(values []int) int { values[0] += 5; return values[0] }

func Function() func([]int) int { return originalFunction }
func FunctionIsOriginal(value any) bool {
	function, ok := value.(func([]int) int)
	if !ok {
		return false
	}
	values := []int{0}
	return function(values) == 5 && values[0] == 5
}

// A callback result is newly owned by Go. This does not mutate any Sky slice
// supplied as a function argument.
func Consume(callback func(int) []int) int {
	result := callback(0)
	result[0] = 99
	return result[0]
}

func NilFunction() func([]int) int { return nil }
func NilFunctionAny() any          { var function func([]int) int; return function }
func FunctionIsNil(value any) bool {
	if value == nil {
		return true
	}
	function, ok := value.(func([]int) int)
	return ok && function == nil
}

// Fixed-size arrays select the generated any/any wrapper. The returned array
// itself is a value, but its list element still aliases our private buffer.
func ReadArray(n int) [1][]int { shared[0] = n; return [1][]int{shared} }

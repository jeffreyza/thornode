package mapiter

func BadMapIter() {
	m := map[string]int{"a": 1}
	for k, v := range m { // want "found map iteration"
		_ = k
		_ = v
	}
}

func IgnoredMapIter() {
	m := map[string]int{"a": 1}
	// analyze-ignore(map-iteration)
	for k, v := range m {
		_ = k
		_ = v
	}
}

func SliceIter() {
	s := []int{1, 2, 3}
	for _, v := range s {
		_ = v
	}
}

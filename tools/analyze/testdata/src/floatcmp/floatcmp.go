package floatcmp

func BadFloatCompare() {
	a := 1.0
	b := 2.0
	if a == b { // want "found float comparison"
		_ = a
	}
}

func IgnoredFloatCompare() {
	a := 1.0
	b := 2.0
	// analyze-ignore(float-comparison)
	if a == b {
		_ = a
	}
}

func IntCompare() {
	a := 1
	b := 2
	if a == b {
		_ = a
	}
}

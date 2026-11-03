package randuse

func RandInt() int { return 42 }

func BadRandUse() {
	_ = RandInt() // want `use of functions with "Rand" in name is prohibited`
}

func SafeUse() {
	_ = 42
}

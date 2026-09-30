package demo

func divcheck(x int) int {
	return 10 / x
}

func guarded(x int) int {
	if x != 0 {
		return 10 / x
	}
	return 0
}

func loopIndex(n int) int {
	s := 0
	for i := 0; i < n; i++ {
		s += i
	}
	a := [4]int{1, 2, 3, 4}
	return a[s]
}

func safeArray(i int) int {
	a := [4]int{1, 2, 3, 4}
	if i >= 0 && i < 4 {
		return a[i]
	}
	return 0
}

func overflow() int {
	x := int(9223372036854775807)
	return x + 1
}

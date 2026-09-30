package demo

func boundedLoop() int {
	a := [4]int{1, 2, 3, 4}
	s := 0
	for i := 0; i < 4; i++ {
		s += a[i]
	}
	return s
}

func unboundedLoop(n int) int {
	a := [4]int{}
	t := 0
	for j := 0; j < n; j++ {
		t += a[j]
	}
	return t
}

func negativeIdx(k int) int {
	a := [4]int{}
	if k < 0 {
		return a[k]
	}
	return 0
}

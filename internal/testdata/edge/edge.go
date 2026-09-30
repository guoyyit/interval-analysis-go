package edge

func modSafe(x int) int {
	if x != 0 {
		return 17 % x
	}
	return 0
}

func unsigned(u uint8) uint8 {
	if u < 10 {
		return u + 1 // safe: u in [0,9] -> [1,10]
	}
	return 0
}

func unsignedOverflow(u uint8) uint8 {
	return u + 1 // u full [0,255] -> may overflow
}

func nestedGuard(x int) int {
	if x > 0 {
		if x < 100 {
			return 1000 / x // x in [1,99], safe
		}
	}
	return 0
}

func constIndex() int {
	a := [3]int{9, 8, 7}
	return a[2] // safe constant index
}

func minMaxUse(a, b int) int {
	m := min(a, b)
	if m > 0 {
		return 10 / m
	}
	return 0
}

func unsignedUnderflow(u uint8) uint8 {
	return u - 1 // u includes 0 -> underflow
}

func sliceNeg(s []int, i int) int {
	if i < 0 {
		return s[i] // negative index always panics
	}
	return 0
}

func uintGuard(u uint8) uint8 {
	if u >= 1 {
		return u - 1 // u in [1,255] -> [0,254], no underflow
	}
	return 0
}

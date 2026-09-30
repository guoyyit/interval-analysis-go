package edge

func isEven(n int) bool {
	if n == 0 {
		return true
	}
	return isOdd(n - 1)
}

func isOdd(n int) bool {
	if n == 0 {
		return false
	}
	return isEven(n - 1)
}

func selfRec(x int) int {
	if x <= 0 {
		return 0
	}
	return selfRec(x - 1)
}

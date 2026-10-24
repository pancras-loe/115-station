package api

import "testing"

// 放行后的包装目录在宽限期内算「还没落地」：115 转存是异步的，放行后马上那次整理看到的是空目录
func TestSubWrapperLanding(t *testing.T) {
	holdSubWrapper("w1")
	if subWrapperLanding("w1") {
		t.Fatal("还压着的不算落地中")
	}
	releaseSubWrappers("w1", "never-held")
	if subWrapperHeld("w1") {
		t.Fatal("放行后不该再压着")
	}
	if !subWrapperLanding("w1") {
		t.Fatal("刚放行的包装目录应在宽限期内")
	}
	if subWrapperLanding("never-held") {
		t.Fatal("没压过的目录不算包装目录")
	}
	releaseSubWrappers() // 空调用不出错
}

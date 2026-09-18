package api

import (
	"fmt"
	"strings"
)

// ==================== 115 pickcode ⇄ id 本地换算 ====================
//
// 115 的 pickcode 是 id 的可逆编码，不需要调接口就能互转：
//
//	pickcode = 前缀 + 加密(36进制的id) + 加密(不动点)
//
//   - 前缀：文件为 a/b/c/d/e，目录为 fa/fb/fc/fd/fe，它唯一确定所用的替换表
//   - 不动点：同一个账号固定的 4 位串（首字符恒为 '0'），可从任意一个已知
//     pickcode 反推——快速同步用第一页文件里的 pc 现推，不额外发请求
//   - 中缀：id 的 36 进制表示，经替换表加密
//
// 快速全量同步靠这套换算从媒体库 cid 直接算出目录 pickcode，
// 再去打 /app/chrome/downfolders 拿整棵子树的目录表。
// 算法参考 p115pickcode（GPLv3），此处为等价的 Go 实现。

// pcAlphabet pickcode 只含这 36 个字符
const pcAlphabet = "0123456789abcdefghijklmnopqrstuvwxyz"

// pcPlainTab 前缀 → 明文字符表。表中第 i 个字符加密后得到 pcAlphabet[i]
var pcPlainTab = map[string]string{
	"a":  "fuln1ytpj3smg8d5a094qh7cxkbi62zvewro",
	"b":  "sk721n9a0emlfpcrzbqdw3gjh6ty5xui48vo",
	"c":  "ywcz3hite6f1j0guoakvdb2ns7p8qr9ml5x4",
	"d":  "rq2vl5o7wsken9u8tp4jg3zbyc6xmhifd01a",
	"e":  "ljm9eqbcfhw7ktv3x1dgp5ua8y6s4znr2io0",
	"fa": "fumk0ytpj3sng8d5a194qh7cxlbi62zvewro",
	"fb": "sk732o9a1enmfpcrzbqdw4gjh6ty5xui08vl",
	"fc": "ywcz6hite9f4j3gup2kvdb5osal0qr1nm8x7",
	"fd": "on6vl0r2wpkeq9u3ts8jg7zbyc1xmhifd45a",
	"fe": "ljm0es2cfhwakqv6x4dgp8r1by9u7znt5io3",
}

// pcDirPrefix 目录 pickcode 的前缀（快速同步只需要生成目录 pickcode）
const pcDirPrefix = "fa"

var (
	// pcEncTab[前缀] 明文 → 密文
	pcEncTab = map[string]map[byte]byte{}
	// pcDecTab[前缀] 密文 → 明文
	pcDecTab = map[string]map[byte]byte{}
	// pcSuffixHead[密文首位] → 前缀。不动点首字符恒为 '0'，
	// 加密后落在 pickcode[len-4]，据此可反推该 pickcode 用的是哪张表
	pcSuffixHead = map[byte]string{}
)

func init() {
	for prefix, plain := range pcPlainTab {
		enc := make(map[byte]byte, 36)
		dec := make(map[byte]byte, 36)
		for i := 0; i < len(plain); i++ {
			enc[plain[i]] = pcAlphabet[i]
			dec[pcAlphabet[i]] = plain[i]
		}
		pcEncTab[prefix] = enc
		pcDecTab[prefix] = dec
		pcSuffixHead[enc['0']] = prefix
	}
}

// pcTranslate 按替换表逐字符转换，表里没有的字符原样保留
func pcTranslate(s string, tab map[byte]byte) string {
	b := []byte(s)
	for i := range b {
		if v, ok := tab[b[i]]; ok {
			b[i] = v
		}
	}
	return string(b)
}

// pcB36Encode 数字转 36 进制小写串
func pcB36Encode(n uint64) string {
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 13)
	for n > 0 {
		buf = append(buf, pcAlphabet[n%36])
		n /= 36
	}
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}

// pcB36Decode 36 进制小写串转数字
func pcB36Decode(s string) (uint64, error) {
	if s == "" {
		return 0, fmt.Errorf("空的 36 进制串")
	}
	var n uint64
	for i := 0; i < len(s); i++ {
		d := strings.IndexByte(pcAlphabet, s[i])
		if d < 0 {
			return 0, fmt.Errorf("非法字符 %q", s[i])
		}
		n = n*36 + uint64(d)
	}
	return n, nil
}

// pcPrefixOf 判断 pickcode 用的是哪张替换表
func pcPrefixOf(pickcode string) (string, error) {
	n := len(pickcode)
	if n >= 6 {
		if p := pickcode[:2]; pcPlainTab[p] != "" {
			return p, nil
		}
	}
	if n >= 5 {
		if p := pickcode[:1]; pcPlainTab[p] != "" {
			return p, nil
		}
	}
	if n >= 4 {
		if p, ok := pcSuffixHead[pickcode[n-4]]; ok {
			return p, nil
		}
	}
	return "", fmt.Errorf("无法识别 pickcode 前缀: %s", truncateStr(pickcode, 24))
}

// stablePoint115 从任意一个 pickcode 反推该账号的不动点（4 位，首字符恒为 '0'）
func stablePoint115(pickcode string) (string, error) {
	pickcode = strings.TrimSpace(strings.ToLower(pickcode))
	if len(pickcode) < 6 {
		return "", fmt.Errorf("pickcode 太短，无法推导不动点: %q", pickcode)
	}
	prefix, err := pcPrefixOf(pickcode)
	if err != nil {
		return "", err
	}
	sp := pcTranslate(pickcode[len(pickcode)-4:], pcDecTab[prefix])
	if sp[0] != '0' {
		// 不动点首字符必为 '0'，不是就说明前缀判错或 pickcode 有问题
		return "", fmt.Errorf("推导出的不动点非法（%s），pickcode=%s", sp, truncateStr(pickcode, 24))
	}
	return sp, nil
}

// pickcodeToID115 pickcode 解出 id
func pickcodeToID115(pickcode string) (uint64, error) {
	pickcode = strings.TrimSpace(strings.ToLower(pickcode))
	if pickcode == "" {
		return 0, nil // 根目录 id 0 对应空 pickcode
	}
	prefix, err := pcPrefixOf(pickcode)
	if err != nil {
		return 0, err
	}
	if len(pickcode) <= len(prefix)+4 {
		return 0, fmt.Errorf("pickcode 长度不足: %s", truncateStr(pickcode, 24))
	}
	cipher := pickcode[len(prefix) : len(pickcode)-4]
	return pcB36Decode(pcTranslate(cipher, pcDecTab[prefix]))
}

// dirPickcode115 由目录 id + 账号不动点算出目录 pickcode（零请求）
func dirPickcode115(id uint64, stablePoint string) (string, error) {
	if id == 0 {
		return "", fmt.Errorf("根目录没有 pickcode")
	}
	if len(stablePoint) != 4 || stablePoint[0] != '0' {
		return "", fmt.Errorf("不动点非法: %q", stablePoint)
	}
	tab := pcEncTab[pcDirPrefix]
	return pcDirPrefix + pcTranslate(pcB36Encode(id), tab) + pcTranslate(stablePoint, tab), nil
}

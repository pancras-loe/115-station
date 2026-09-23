package api

// 媒体库封面的绘图工具：模糊、圆角卡片、柔和投影、渐变、颗粒、文字投影、取色。
// 早先五套版式只有「贴图 + 半透明纯色矩形」两种手段，成品发平、到处是硬边，
// 竖版海报被拉满横幅时还会放大出马赛克 —— 这些都靠下面的工具收拾。

import (
	"image"
	"image/color"
	"image/draw"
	"math"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/math/f64"
	"golang.org/x/image/math/fixed"
)

// coverUnits 返回按 1280×720 设计稿换算坐标的函数；三档分辨率都是 16:9，横纵比例一致。
func coverUnits(img *image.RGBA) (px, py func(int) int, s float64) {
	sx := float64(img.Bounds().Dx()) / 1280
	sy := float64(img.Bounds().Dy()) / 720
	return func(v int) int { return int(math.Round(float64(v) * sx)) },
		func(v int) int { return int(math.Round(float64(v) * sy)) }, sy
}

func coverClamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

func coverSmooth(e0, e1, v float64) float64 {
	t := coverClamp01((v - e0) / (e1 - e0))
	return t * t * (3 - 2*t)
}

// coverBlurPlane 对交错存储的 ch 通道平面做三遍盒式模糊，效果近似半径 r 的高斯。
func coverBlurPlane(pix []uint8, w, h, stride, ch, r int) {
	if r < 1 || w < 1 || h < 1 {
		return
	}
	buf := make([]int, max(w, h)*ch)
	for pass := 0; pass < 3; pass++ {
		for y := 0; y < h; y++ {
			coverBlurLine(pix[y*stride:], w, ch, ch, r, buf)
		}
		for x := 0; x < w; x++ {
			coverBlurLine(pix[x*ch:], h, stride, ch, r, buf)
		}
	}
}

func coverBlurLine(p []uint8, n, step, ch, r int, buf []int) {
	for i := 0; i < n; i++ {
		for c := 0; c < ch; c++ {
			buf[i*ch+c] = int(p[i*step+c])
		}
	}
	div := 2*r + 1
	for c := 0; c < ch; c++ {
		at := func(i int) int { return buf[max(0, min(n-1, i))*ch+c] }
		sum := 0
		for i := -r; i <= r; i++ {
			sum += at(i)
		}
		for i := 0; i < n; i++ {
			p[i*step+c] = uint8(sum / div)
			sum += at(i+r+1) - at(i-r)
		}
	}
}

// coverBackdrop 把海报铺满 w×h 并做大半径模糊。先缩到 1/4 处理再放大：
// 模糊本来就丢高频，低分辨率算出来几乎一样，耗时只剩 1/16，还顺带抹掉放大带来的马赛克。
func coverBackdrop(src image.Image, w, h, radius int) *image.RGBA {
	const k = 4
	small := image.NewRGBA(image.Rect(0, 0, max(1, w/k), max(1, h/k)))
	coverCrop(small, src, small.Bounds())
	coverBlurPlane(small.Pix, small.Rect.Dx(), small.Rect.Dy(), small.Stride, 4, max(1, radius/k))
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.BiLinear.Scale(out, out.Bounds(), small, small.Bounds(), draw.Src, nil)
	return out
}

// coverRoundMask 生成带抗锯齿的圆角矩形遮罩。
func coverRoundMask(w, h int, r float64) *image.Alpha {
	m := image.NewAlpha(image.Rect(0, 0, w, h))
	r = math.Min(r, float64(min(w, h))/2)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			fx, fy := float64(x)+.5, float64(y)+.5
			dx := fx - math.Max(r, math.Min(float64(w)-r, fx))
			dy := fy - math.Max(r, math.Min(float64(h)-r, fy))
			a := 1.0
			if d := math.Hypot(dx, dy); d > 0 {
				a = coverClamp01(r - d + .5)
			}
			m.Pix[y*m.Stride+x] = uint8(a*255 + .5)
		}
	}
	return m
}

// coverRoundFill 用圆角矩形填色，给印章、齿孔这类小图形用。
func coverRoundFill(img *image.RGBA, rect image.Rectangle, r float64, c color.Color) {
	if rect.Empty() {
		return
	}
	draw.DrawMask(img, rect, image.NewUniform(c), image.Point{}, coverRoundMask(rect.Dx(), rect.Dy(), r), image.Point{}, draw.Over)
}

// coverShadow 在 rect 下方垫一层柔和投影。原先是整块黑色矩形右下偏移，
// 看上去像贴纸而不是悬浮的卡片。
func coverShadow(dst *image.RGBA, rect image.Rectangle, radius float64, blur, dy int, opacity float64) {
	if rect.Empty() || opacity <= 0 {
		return
	}
	pad := blur * 2
	m := image.NewAlpha(image.Rect(0, 0, rect.Dx()+2*pad, rect.Dy()+2*pad))
	draw.Draw(m, image.Rect(pad, pad, pad+rect.Dx(), pad+rect.Dy()), coverRoundMask(rect.Dx(), rect.Dy(), radius), image.Point{}, draw.Src)
	coverBlurPlane(m.Pix, m.Rect.Dx(), m.Rect.Dy(), m.Stride, 1, max(1, blur/2))
	for i, v := range m.Pix {
		m.Pix[i] = uint8(float64(v) * opacity)
	}
	at := image.Rect(rect.Min.X-pad, rect.Min.Y-pad+dy, rect.Max.X+pad, rect.Max.Y+pad+dy)
	draw.DrawMask(dst, at, image.NewUniform(color.Black), image.Point{}, m, image.Point{}, draw.Over)
}

// coverCard 画一张圆角海报：投影 → 贴图（dim 为压暗比例，给景深靠后的卡片用）→ 一圈极淡的亮边。
// 深色海报放在深色底上时，全靠这圈亮边才看得出轮廓。
func coverCard(dst *image.RGBA, src image.Image, rect image.Rectangle, radius, shadow, dim float64) {
	if src == nil || rect.Empty() {
		return
	}
	if shadow > 0 {
		coverShadow(dst, rect, radius, max(4, rect.Dy()/16), max(2, rect.Dy()/40), shadow)
	}
	w, h := rect.Dx(), rect.Dy()
	tmp := image.NewRGBA(image.Rect(0, 0, w, h))
	coverCrop(tmp, src, tmp.Bounds())
	if dim > 0 {
		draw.Draw(tmp, tmp.Bounds(), image.NewUniform(color.NRGBA{A: uint8(coverClamp01(dim) * 255)}), image.Point{}, draw.Over)
	}
	outer := coverRoundMask(w, h, radius)
	draw.DrawMask(dst, rect, tmp, image.Point{}, outer, image.Point{}, draw.Over)
	if w > 4 && h > 4 {
		inner := coverRoundMask(w-2, h-2, math.Max(0, radius-1))
		edge := image.NewAlpha(outer.Rect)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				in := uint8(0)
				if x > 0 && y > 0 && x < w-1 && y < h-1 {
					in = inner.Pix[(y-1)*inner.Stride+x-1]
				}
				edge.Pix[y*edge.Stride+x] = uint8(max(0, int(outer.Pix[y*outer.Stride+x])-int(in)) * 40 / 255)
			}
		}
		draw.DrawMask(dst, rect, image.NewUniform(color.White), image.Point{}, edge, image.Point{}, draw.Over)
	}
}

// coverShade 在 rect 内叠一层从 a0 过渡到 a1 的颜色，horizontal 决定方向。
// 用 smoothstep 而不是线性：线性渐变在暗部能看出一道道台阶。
func coverShade(img *image.RGBA, rect image.Rectangle, c color.RGBA, a0, a1 float64, horizontal bool) {
	rect = rect.Intersect(img.Bounds())
	if rect.Empty() {
		return
	}
	span := float64(rect.Dy())
	if horizontal {
		span = float64(rect.Dx())
	}
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			t := float64(y-rect.Min.Y) / span
			if horizontal {
				t = float64(x-rect.Min.X) / span
			}
			t = coverSmooth(0, 1, t)
			coverBlendPixel(img, x, y, c, uint8(255*coverClamp01(a0+(a1-a0)*t)))
		}
	}
}

// coverSpot 以 (cx,cy) 为中心叠一层椭圆形的色块，中心浓、边缘淡；用于标题背后的压暗和投影机光晕。
func coverSpot(img *image.RGBA, cx, cy, rx, ry int, c color.RGBA, strength float64) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		dy := float64(y-cy) / float64(ry)
		for x := b.Min.X; x < b.Max.X; x++ {
			dx := float64(x-cx) / float64(rx)
			if a := strength * (1 - coverSmooth(0, 1, dx*dx+dy*dy)); a > .004 {
				coverBlendPixel(img, x, y, c, uint8(255*a))
			}
		}
	}
}

// coverVignette 压暗四角，把视线收回画面中央。
func coverVignette(img *image.RGBA, strength float64) {
	b := img.Bounds()
	cx, cy := float64(b.Dx())/2, float64(b.Dy())/2
	for y := b.Min.Y; y < b.Max.Y; y++ {
		dy := (float64(y) - cy) / cy
		for x := b.Min.X; x < b.Max.X; x++ {
			dx := (float64(x) - cx) / cx
			if a := strength * coverSmooth(.35, 1.45, dx*dx*.8+dy*dy); a > .004 {
				coverBlendPixel(img, x, y, color.RGBA{A: 255}, uint8(255*a))
			}
		}
	}
}

// coverGrain 叠一层固定种子的细颗粒：打散大面积渐变的色带，也让纯色底有纸/胶片的质感。
// 种子只和坐标有关，同一张封面每次生成结果一致。
func coverGrain(img *image.RGBA, amount int) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			n := uint32(x)*374761393 + uint32(y)*668265263
			n = (n ^ n>>13) * 1274126177
			d := int((n^n>>16)%uint32(2*amount+1)) - amount
			i := img.PixOffset(x, y)
			for c := 0; c < 3; c++ {
				img.Pix[i+c] = uint8(max(0, min(255, int(img.Pix[i+c])+d)))
			}
		}
	}
}

// coverDarken 把整块区域按 k 压暗并向 tint 靠拢 mix 的比例，给模糊背景做统一调色。
func coverDarken(img *image.RGBA, k float64, tint color.RGBA, mix float64) {
	for i := 0; i+3 < len(img.Pix); i += 4 {
		for c, t := range [3]uint8{tint.R, tint.G, tint.B} {
			v := float64(img.Pix[i+c])*(1-mix) + float64(t)*mix
			img.Pix[i+c] = uint8(math.Min(255, v*k))
		}
	}
}

func coverMix(a, b color.RGBA, t float64) color.RGBA {
	f := func(x, y uint8) uint8 { return uint8(float64(x)*(1-t) + float64(y)*t + .5) }
	return color.RGBA{f(a.R, b.R), f(a.G, b.G), f(a.B, b.B), 255}
}

func coverAlpha(c color.RGBA, a float64) color.NRGBA {
	return color.NRGBA{R: c.R, G: c.G, B: c.B, A: uint8(255 * coverClamp01(a))}
}

// coverTrackedWidth 与 coverDrawTracked 配对：tracking 为 0 时整串绘制，保留字体自带的字偶距。
func coverTrackedWidth(s string, size, tracking float64, family coverFont) int {
	if tracking == 0 {
		return coverTextWidthFor(s, size, family)
	}
	w, n := 0, 0
	for _, r := range s {
		w += coverTextWidthFor(string(r), size, family)
		n++
	}
	return w + int(tracking)*max(0, n-1)
}

func coverDrawTracked(dst draw.Image, s string, size, tracking float64, x, y int, c color.Color, family coverFont) {
	if tracking == 0 {
		coverDrawTextFor(dst, s, size, x, y, c, family)
		return
	}
	coverDrawTrackedText(dst, s, size, tracking, x, y, c, family)
}

// coverTextShadow 在文字下垫一层模糊投影。标题压在照片上时，描边或偏移重绘都会让笔画发糊变粗，
// 柔和投影只抬对比、不改字形。
func coverTextShadow(dst *image.RGBA, s string, size, tracking float64, x, y int, family coverFont, blur int, opacity float64) {
	face := coverFaceFor(size, family)
	if face == nil || opacity <= 0 {
		return
	}
	defer face.Close()
	pad := blur*2 + 2
	tw := coverTrackedWidth(s, size, tracking, family)
	region := image.Rect(x-pad, y-int(size*1.1)-pad, x+tw+pad, y+int(size*.35)+pad)
	m := image.NewAlpha(region)
	if tracking == 0 {
		(&font.Drawer{Dst: m, Src: image.Opaque, Face: face, Dot: fixed.P(x, y)}).DrawString(s)
	} else {
		coverDrawTrackedText(m, s, size, tracking, x, y, color.White, family)
	}
	coverBlurPlane(m.Pix, region.Dx(), region.Dy(), m.Stride, 1, max(1, blur/2))
	for i, v := range m.Pix {
		m.Pix[i] = uint8(math.Min(255, float64(v)*opacity))
	}
	draw.DrawMask(dst, region, image.NewUniform(color.Black), image.Point{}, m, region.Min, draw.Over)
}

// coverAccent 从海报里挑点缀色：按饱和度加权求平均，再把饱和度和亮度拉进固定区间。
// 直接取均值会得到一团灰褐；写死的颜色（原先的粉线、铜线）又和海报毫无关系。
func coverAccent(posters []image.Image, fallback color.RGBA, light float64) color.RGBA {
	var r, g, b, wsum float64
	for i, p := range posters {
		if i == 3 {
			break
		}
		if p == nil {
			continue
		}
		bd := p.Bounds()
		step := max(1, min(bd.Dx(), bd.Dy())/40)
		for y := bd.Min.Y; y < bd.Max.Y; y += step {
			for x := bd.Min.X; x < bd.Max.X; x += step {
				rr, gg, bb, _ := p.At(x, y).RGBA()
				_, sat, l := coverHSL(float64(rr>>8)/255, float64(gg>>8)/255, float64(bb>>8)/255)
				// 过暗过亮的像素饱和度不可信，按亮度再压一次权重。
				wt := sat * sat * (1 - math.Abs(2*l-1))
				r += float64(rr>>8) * wt
				g += float64(gg>>8) * wt
				b += float64(bb>>8) * wt
				wsum += wt
			}
		}
	}
	base := fallback
	if wsum > 1 {
		base = color.RGBA{uint8(r / wsum), uint8(g / wsum), uint8(b / wsum), 255}
	}
	h, sat, _ := coverHSL(float64(base.R)/255, float64(base.G)/255, float64(base.B)/255)
	return coverFromHSL(h, math.Max(.42, math.Min(.72, sat*1.3)), light)
}

func coverHSL(r, g, b float64) (h, s, l float64) {
	hi, lo := math.Max(r, math.Max(g, b)), math.Min(r, math.Min(g, b))
	l = (hi + lo) / 2
	if hi == lo {
		return 0, 0, l
	}
	d := hi - lo
	if l > .5 {
		s = d / (2 - hi - lo)
	} else {
		s = d / (hi + lo)
	}
	switch hi {
	case r:
		h = math.Mod((g-b)/d+6, 6)
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	return h / 6, s, l
}

func coverFromHSL(h, s, l float64) color.RGBA {
	q := l + s - l*s
	if l < .5 {
		q = l * (1 + s)
	}
	p := 2*l - q
	f := func(t float64) uint8 {
		t = math.Mod(t+1, 1)
		v := p
		switch {
		case t < 1./6:
			v = p + (q-p)*6*t
		case t < .5:
			v = q
		case t < 2./3:
			v = p + (q-p)*(2./3-t)*6
		}
		return uint8(coverClamp01(v)*255 + .5)
	}
	return color.RGBA{f(h + 1./3), f(h), f(h - 1./3), 255}
}

// coverRotate 把 layer 旋转 deg 度（正值为顺时针）后贴到 dst，中心对准 (cx, cy)；
// shadow>0 时先按旋转后的轮廓垫一层柔和投影。layer 四周要留 2px 透明边：
// Transform 在源图边界外不采样，贴边的像素会转出锯齿。
func coverRotate(dst, layer *image.RGBA, cx, cy int, deg, shadow float64) {
	lw, lh := layer.Rect.Dx(), layer.Rect.Dy()
	blur := max(4, min(lw, lh)/14)
	side := int(math.Hypot(float64(lw), float64(lh))) + 4*blur
	out := image.NewRGBA(image.Rect(0, 0, side, side))
	sin, cos := math.Sincos(deg * math.Pi / 180)
	c, lx, ly := float64(side)/2, float64(lw)/2, float64(lh)/2
	m := f64.Aff3{cos, -sin, c - cos*lx + sin*ly, sin, cos, c - sin*lx - cos*ly}
	xdraw.BiLinear.Transform(out, m, layer, layer.Bounds(), draw.Over, nil)
	at := image.Rect(cx-side/2, cy-side/2, cx-side/2+side, cy-side/2+side)
	if shadow > 0 {
		mask := image.NewAlpha(out.Rect)
		for i := range mask.Pix {
			mask.Pix[i] = uint8(float64(out.Pix[i*4+3]) * shadow)
		}
		coverBlurPlane(mask.Pix, side, side, mask.Stride, 1, max(1, blur/2))
		draw.DrawMask(dst, at.Add(image.Pt(0, blur/2)), image.NewUniform(color.Black), image.Point{}, mask, image.Point{}, draw.Over)
	}
	draw.Draw(dst, at, out, image.Point{}, draw.Over)
}

// coverPolaroid 做一张拍立得相纸：四边白框、底边加宽，照片按 pw×ph 裁切。
func coverPolaroid(src image.Image, pw, ph, border, bottom int) *image.RGBA {
	const pad = 2
	w, h := pw+2*border, ph+border+bottom
	layer := image.NewRGBA(image.Rect(0, 0, w+2*pad, h+2*pad))
	coverRoundFill(layer, image.Rect(pad, pad, pad+w, pad+h), 3, color.RGBA{R: 246, G: 243, B: 236, A: 255})
	photo := image.Rect(pad+border, pad+border, pad+border+pw, pad+border+ph)
	coverCrop(layer, src, photo)
	// 照片上沿一道细阴影，像相纸框压在照片上，而不是照片浮在框外。
	coverShade(layer, image.Rect(photo.Min.X, photo.Min.Y, photo.Max.X, photo.Min.Y+max(2, ph/60)), color.RGBA{A: 255}, .25, 0, false)
	return layer
}

// coverReflect 在 rect 正下方画海报的倒影：上下翻转、由浓到淡，高度占卡片的 ratio。
// dim 与卡片本身的压暗一致，否则靠后的暗卡片会配一个比自己还亮的倒影。
func coverReflect(dst *image.RGBA, src image.Image, rect image.Rectangle, gap int, ratio, strength, dim float64) {
	if src == nil || rect.Empty() {
		return
	}
	w, h := rect.Dx(), rect.Dy()
	tmp := image.NewRGBA(image.Rect(0, 0, w, h))
	coverCrop(tmp, src, tmp.Bounds())
	rh := int(float64(h) * ratio)
	for y := 0; y < rh; y++ {
		t := 1 - float64(y)/float64(rh)
		a := uint8(255 * strength * t * t * (1 - dim))
		for x := 0; x < w; x++ {
			pt := image.Pt(rect.Min.X+x, rect.Max.Y+gap+y)
			if !pt.In(dst.Rect) {
				continue
			}
			i := tmp.PixOffset(x, h-1-y)
			coverBlendPixel(dst, pt.X, pt.Y, color.RGBA{R: tmp.Pix[i], G: tmp.Pix[i+1], B: tmp.Pix[i+2], A: 255}, a)
		}
	}
}

// coverOutline 画一个细线框。
func coverOutline(img *image.RGBA, r image.Rectangle, t int, c color.Color) {
	coverFill(img, image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+t), c)
	coverFill(img, image.Rect(r.Min.X, r.Max.Y-t, r.Max.X, r.Max.Y), c)
	coverFill(img, image.Rect(r.Min.X, r.Min.Y+t, r.Min.X+t, r.Max.Y-t), c)
	coverFill(img, image.Rect(r.Max.X-t, r.Min.Y+t, r.Max.X, r.Max.Y-t), c)
}

// coverVerticalText 把一行字转 90° 竖排，中心对准 (cx, cy)，从上往下读。
func coverVerticalText(dst *image.RGBA, s string, size, tracking float64, cx, cy int, c color.Color, family coverFont) {
	tw := coverTrackedWidth(s, size, tracking, family)
	layer := image.NewRGBA(image.Rect(0, 0, tw+8, int(size*1.5)+4))
	coverDrawTracked(layer, s, size, tracking, 4, int(size*1.1), c, family)
	coverRotate(dst, layer, cx, cy, 90, 0)
}

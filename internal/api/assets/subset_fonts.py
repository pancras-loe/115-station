"""把媒体库海报用的三款字体裁成子集，再嵌进二进制（covergen.go 的 //go:embed）。

为什么要裁：原版霞鹜文楷收了 2 万多个汉字，单个文件 25MB，三款加起来 34MB，
而海报上只画媒体库名和几行固定英文。子集保留：
  - 汉字：得意黑 ∪ 站酷小薇 两款自带的字（≈《通用规范汉字表》∪ GB2312，八千出头），
    三款字体因此覆盖一致，换样式不会出现一款能显示、另一款变豆腐块；
  - ASCII / Latin-1、常用标点、全角符号、罗马数字、带圈数字、假名。
同时去掉 TrueType hinting（海报是大字号栅格化，用不上）、字形名与竖排度量。

改名：三款字体的 OFL 都声明了保留字体名，子集属于 Modified Version，
不得沿用原名作为主名称，因此把 name 表的家族名改成 StrmStation Cover *，
版权（ID 0）与许可证（ID 13/14）条目原样保留。

用法（需要 pip install fonttools）：
    python subset_fonts.py <LXGWWenKai-Medium.ttf> <SmileySans-Oblique.ttf> <ZCOOLXiaoWei-Regular.ttf>
原版字体从各自上游仓库取，输出直接覆盖本目录下的三个 .ttf。
"""

import os
import sys

from fontTools import subset
from fontTools.ttLib import TTFont

HERE = os.path.dirname(os.path.abspath(__file__))

# (输出文件名, 改名后的家族名)
TARGETS = [
    ("lxgwwenkai-medium.ttf", "StrmStation Cover WK"),
    ("smileysans-oblique.ttf", "StrmStation Cover SM"),
    ("zcoolxiaowei-regular.ttf", "StrmStation Cover XW"),
]

EXTRA_RANGES = [
    (0x0020, 0x007E),  # ASCII
    (0x00A0, 0x00FF),  # Latin-1
    (0x2000, 0x206F),  # 通用标点（—、…、“” 等）
    (0x2150, 0x218F),  # 罗马数字 Ⅰ Ⅱ Ⅲ
    (0x2460, 0x24FF),  # 带圈数字
    (0x3000, 0x303F),  # 中日韩标点（、。「」【】）
    (0x3040, 0x30FF),  # 平假名 / 片假名
    (0xFF00, 0xFFEF),  # 全角 ASCII 与标点
]


def is_han(cp):
    return 0x3400 <= cp <= 0x9FFF or 0xF900 <= cp <= 0xFAFF


def main(srcs):
    if len(srcs) != 3:
        sys.exit(__doc__)
    fonts = [TTFont(p) for p in srcs]

    keep = set()
    for f in fonts[1:]:  # 得意黑、站酷小薇
        keep |= {cp for cp in f.getBestCmap() if is_han(cp)}
    for lo, hi in EXTRA_RANGES:
        keep |= set(range(lo, hi + 1))

    opts = subset.Options()
    opts.hinting = False
    opts.glyph_names = False
    opts.drop_tables += ["vhea", "vmtx", "DSIG"]
    opts.name_IDs = ["*"]
    opts.name_languages = ["*"]
    opts.notdef_outline = True

    for font, (out, family) in zip(fonts, TARGETS):
        sub = subset.Subsetter(opts)
        sub.populate(unicodes=keep)
        sub.subset(font)
        rename(font, family)
        font.save(os.path.join(HERE, out))
        print(out, os.path.getsize(os.path.join(HERE, out)))


def rename(font, family):
    style = font["name"].getDebugName(2) or "Regular"
    ps = family.replace(" ", "") + "-" + style.replace(" ", "")
    values = {1: family, 3: ps, 4: f"{family} {style}", 6: ps}
    name = font["name"]
    # 16/17（典型家族名）与各语言本地化名里同样带着保留名，一并删掉
    name.names = [n for n in name.names if n.nameID not in (1, 3, 4, 6, 16, 17)]
    for nid, val in values.items():
        name.setName(val, nid, 3, 1, 0x409)
        name.setName(val, nid, 1, 0, 0)


if __name__ == "__main__":
    main(sys.argv[1:])

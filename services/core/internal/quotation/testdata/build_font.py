"""Rebuild the synthetic PDF test font with fonttools==4.67.0; not a runtime font."""

import hashlib
import sys
from pathlib import Path

from fontTools import subset
from fontTools.ttLib import TTFont
from fontTools.varLib.instancer import instantiateVariableFont

source = Path(sys.argv[1])
if hashlib.sha256(source.read_bytes()).hexdigest() != (
    "a3041811a78c361b1de50f953c805e0244951c21c5bd412f7232ef0d899af0da"
):
    raise ValueError("source font digest mismatch")
font = TTFont(source, recalcTimestamp=False)
font = instantiateVariableFont(font, {"wght": 400}, inplace=True)
text = "".join(chr(code) for code in range(32, 127))
text += "正式报价单未用章生成文件不代表获准对外发送客户币种金额位元含税费产品数量标准行总证据引用编号内容摘要目录规则排版版本合测试公司设备甲乙：；，。"
options = subset.Options()
options.name_IDs = ["*"]
options.name_legacy = True
options.name_languages = ["*"]
subsetter = subset.Subsetter(options=options)
subsetter.populate(text=text)
subsetter.subset(font)
for record in font["name"].names:
    if record.nameID in (1, 4, 6):
        name = "AgentOSQuoteFixture" if record.nameID == 6 else "AgentOS Quote Fixture"
        record.string = name.encode(record.getEncoding())
font.save(Path(__file__).with_name("quote-fixture.ttf"))

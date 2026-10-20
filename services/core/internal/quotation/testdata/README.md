# 合成 PDF 字体夹具

`quote-fixture.ttf` 仅覆盖测试和版式文本、ASCII，不是可部署的完整中文字体。运行库须由可信配置注入具有分发/嵌入许可的完整 TTF 并固定 SHA-256；缺字拒绝生成。

上游为 Google Fonts 的 Noto Sans SC，固定提交 `a85815a42757630ce188fdad368c2dfc444d4773`：

- [字体](https://raw.githubusercontent.com/google/fonts/a85815a42757630ce188fdad368c2dfc444d4773/ofl/notosanssc/NotoSansSC%5Bwght%5D.ttf)
- [OFL 原文](https://raw.githubusercontent.com/google/fonts/a85815a42757630ce188fdad368c2dfc444d4773/ofl/notosanssc/OFL.txt)，随夹具保存为 OFL.txt。
- 原始 SHA-256：`a3041811a78c361b1de50f953c805e0244951c21c5bd412f7232ef0d899af0da`。
- 夹具 SHA-256：`797af7333c9a1cde588f599ef2a03ec3a6de35f9b3c4f5a11ce4b94cab8f409c`。

使用 `fonttools==4.67.0` 实例化 wght=400、按 build_font.py 中字符集裁剪，改名为 AgentOS Quote Fixture，保留原版权及许可。复现：`uv run --no-project --with fonttools==4.67.0 python build_font.py <原始字体文件>`。生成脚本不在运行路径；Go 单元测试离线使用固定夹具。

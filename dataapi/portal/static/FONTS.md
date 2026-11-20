# Fonts

Space Grotesk and JetBrains Mono, served from the docs site itself (no request to a font host). Both are under the SIL Open Font
License 1.1; the full licence text and copyright lines are in `LICENSE-space-grotesk.txt` and `LICENSE-jetbrains-mono.txt`, which sit
next to the fonts in `dataapi/portal/static/`. The copyright lines were checked against the `name` table (id 0) of the font files themselves.

Source: the npm packages `@fontsource/space-grotesk@5.3.0` and `@fontsource/jetbrains-mono@5.3.0` (licence field `OFL-1.1`; the
upstream projects are https://github.com/floriankarsten/space-grotesk and https://github.com/JetBrains/JetBrainsMono). Fetched
2026-11-20 with `npm pack` (no install), extracted from `package/files/`, unmodified. Package tarball sha256:

- `fontsource-space-grotesk-5.3.0.tgz`: `2adb650694b6387b1862fee8b086a3a47ce9f32c10f20b8ec109aa1fcdfd07b7`
- `fontsource-jetbrains-mono-5.3.0.tgz`: `1bbea47d1387406da5b6ccc4184cc61eae6851cc0db5c1d9bbd088ea9daa0b4a`

Subsets: `latin` and `latin-ext`, normal style, weights 400, 500, 600, 700 (Space Grotesk: all four are used by the design; JetBrains
Mono: 400, 500 and 600 as the design's font link asked, 700 for `.btn.primary`). The `unicode-range` of each subset is the one in the
package's `unicode.json`; `fonts.css` was generated once from the generator `sdk/site/lib/fonts.mjs` in `eve-cyno/plugins` and is committed. The generator writes `url("fonts/...")`; the urls were adapted to name the files beside `fonts.css`. `TestPortal_FontsAreSelfHosted` serves every file and checks it against the sha256 below.

| File | sha256 |
|---|---|
| `jetbrains-mono-latin-400-normal.woff2` | `14425ba9c695763c1547f48a206b7aa60350a33ae23de09f0407877f3fcd89eb` |
| `jetbrains-mono-latin-500-normal.woff2` | `cb182feeed4d798ff6961d3c79f7026279448fca0676438aaecb21f3fc39553a` |
| `jetbrains-mono-latin-600-normal.woff2` | `400c6bfda18d5d14acad1c15d6dcb9f8e13c015e7286317e0b9a482539bef147` |
| `jetbrains-mono-latin-700-normal.woff2` | `d0d4e818808f2a0ba39b2b09d1989366f63494e295f003c7ef436697378507e8` |
| `jetbrains-mono-latin-ext-400-normal.woff2` | `505dfba8ecbe77e82765f36d317ed7ef4ac42719dc5f4ae68d1c483fd22d0d14` |
| `jetbrains-mono-latin-ext-500-normal.woff2` | `879df9319f1cbf633bee1dd489e376a9e1e8c458f4abddcfe381cb83b5e6b027` |
| `jetbrains-mono-latin-ext-600-normal.woff2` | `90899aac1c15552c028fd205376bb537a2a5205164fe8626efabad77d7260c20` |
| `jetbrains-mono-latin-ext-700-normal.woff2` | `45a606d88f72bc0afae4d1a6988e3dba3a5e77ee51aa3b550e37c31c728f4d2d` |
| `space-grotesk-latin-400-normal.woff2` | `65fd17fcbd2e2f522940b5f67ead3d23329e02891aa5495e74d11a499c0b0673` |
| `space-grotesk-latin-500-normal.woff2` | `1b1a8131d9edf975d9decee81e2f2bf504812f7a4f498e5500f28a613e22e64c` |
| `space-grotesk-latin-600-normal.woff2` | `685bbbf69fa616df1ef81847c85fc76be097ddfb3468ff2257be54511ab3130f` |
| `space-grotesk-latin-700-normal.woff2` | `35f8aec56cfd5cbfdb03cc68733a54a0b05bb3617ffcd5fd332badc0b045ca55` |
| `space-grotesk-latin-ext-400-normal.woff2` | `a6658ce608a36f9ece7a65842725deebeb5f01df2debeb696b79c8536bf2ddc8` |
| `space-grotesk-latin-ext-500-normal.woff2` | `ed7e064eda944f88ce2f40101ec20e214004decdf5c0b9ad9219ae21be2dbf13` |
| `space-grotesk-latin-ext-600-normal.woff2` | `0bdc872e2806e0eb2ecca08221ea0b49d36efd9b9239a5744b81afbc0376e9cf` |
| `space-grotesk-latin-ext-700-normal.woff2` | `7ce350b706d79c955ebf91314295f39fa82999f980b7e9beee743c2f3580a15e` |
| `LICENSE-jetbrains-mono.txt` | `403581b69dac5cff4079205e01c6b467e56af449ecbd7247693ddb1baafa005b` |
| `LICENSE-space-grotesk.txt` | `18a4de52385f6b988782639d5d0cc1326e5a8c2de9a7f01d7b20d9aedcc60943` |

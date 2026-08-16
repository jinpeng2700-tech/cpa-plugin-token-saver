# Third-party notices

This project ports narrowly scoped, provider-facing behavior and prompt text.
It does not contain the Caveman engine, proxy, cacheengine, rewriter, browse,
MCP, shrink, cavemem Go core, or shared platform runtime.

## 9router 0.5.55

- Source: <https://github.com/decolua/9router>
- Snapshot used: 9router 0.5.55
- Files consulted: `open-sse/rtk/systemInject.js`,
  `open-sse/rtk/cavemanPrompts.js`, `open-sse/rtk/ponytailPrompt.js`, and the
  RTK filter files named in their source comments.
- Copyright: Copyright (c) 2024-2026 decolua and contributors.
- License: MIT; see `licenses/9router-MIT.txt`.
- Modifications: translated selected behavior to Go, restricted it to the
  CLIProxyAPI provider-facing eligibility matrix, and added stable idempotency
  markers and byte-preserving JSON edits.

## RTK (Rust Token Killer)

- Source: <https://github.com/rtk-ai/rtk>
- Upstream license copyright line: Copyright 2024 rtk-ai and rtk-ai Labs.
- Upstream package metadata names Patrick Szymkowiak as author.
- License: Apache License 2.0; see `licenses/rtk-Apache-2.0.txt`.
- Modifications: the plugin contains a Go port of selected output-filtering
  behavior exposed through 9router; it does not embed the RTK executable.

## Caveman prompt face

- Source: <https://github.com/JuliusBrussee/caveman>
- Prompt snapshot used: the six MIT prompt faces carried by 9router 0.5.55.
- Copyright: Copyright (c) 2026 Julius Brussee.
- License: MIT for the prompt/skill material; see
  `licenses/caveman-MIT.txt`, including its upstream scope note.
- Excluded: all upstream Business Source License 1.1 engine-linked runtime
  directories listed in the scope note.

## Ponytail prompt face

- Source: <https://github.com/DietrichGebert/ponytail>
- Prompt snapshot used: the three MIT prompt faces carried by 9router 0.5.55.
- Copyright: Copyright (c) 2026 DietrichGebert.
- License: MIT; see `licenses/ponytail-MIT.txt`.

The external repository licenses and metadata above were verified against the
official repositories on 2026-08-17. No third-party names or marks imply
endorsement of this project.
